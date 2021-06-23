package asr

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha1"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/gorilla/websocket"

	"github.com/tencentcloud/tencentcloud-speech-sdk-go/common"
)

// SpeechRecognitionResponseResultWord SpeechRecognitionResponseResultWord
type SpeechRecognitionResponseResultWord struct {
	Word       string `json:"word"`
	StartTime  uint32 `json:"start_time"`
	EndTime    uint32 `json:"end_time"`
	StableFlag uint32 `json:"stable_flag"`
}

// SpeechRecognitionResponseResult SpeechRecognitionResponseResult
type SpeechRecognitionResponseResult struct {
	SliceType    uint32                                `json:"slice_type"`
	Index        int                                   `json:"index"`
	StartTime    uint32                                `json:"start_time"`
	EndTime      uint32                                `json:"end_time"`
	VoiceTextStr string                                `json:"voice_text_str"`
	WordSize     uint32                                `json:"word_size"`
	WordList     []SpeechRecognitionResponseResultWord `json:"word_list"`
}

// SpeechRecognitionResponse is the reponse of asr service
type SpeechRecognitionResponse struct {
	Code      int                             `json:"code"`
	Message   string                          `json:"message"`
	VoiceID   string                          `json:"voice_id,omitempty"`
	MessageID string                          `json:"message_id,omitempty"`
	Final     uint32                          `json:"final,omitempty"`
	Result    SpeechRecognitionResponseResult `json:"result,omitempty"`
}

func newSpeechRecognitionResponse(code int, message string, voiceID string, messageID string, final uint32) *SpeechRecognitionResponse {
	return &SpeechRecognitionResponse{
		Code:      code,
		Message:   message,
		VoiceID:   voiceID,
		MessageID: messageID,
		Final:     final,
	}
}

// SpeechRecognitionListener is the listener of
type SpeechRecognitionListener interface {
	OnRecognitionStart(*SpeechRecognitionResponse)
	OnSentenceBegin(*SpeechRecognitionResponse)
	OnRecognitionResultChange(*SpeechRecognitionResponse)
	OnSentenceEnd(*SpeechRecognitionResponse)
	OnRecognitionComplete(*SpeechRecognitionResponse)
	OnFail(*SpeechRecognitionResponse, error)
}

// SpeechRecognizer is the entry for ASR service
type SpeechRecognizer struct {
	AppID           string
	Credential      *common.Credential
	EngineModelType string
	VoiceFormat     uint32
	NeedVad         uint32
	HotwordId       string
	CustomizationId string
	FilterDirty     int
	FilterModal     int
	FilterPunc      int
	ConvertNumMode  int
	WordInfo        int
	VadSilenceTime  uint32

	ProxyURL string

	conn       *websocket.Conn
	dataChan   chan []byte
	sendEnd    chan int
	receiveEnd chan int
	voiceID    string
	mutex      sync.Mutex

	eventChan chan speechRecognitionEvent
	eventEnd  chan int

	listener SpeechRecognitionListener

	started bool
}

const (
	defaultVoiceFormat    = 1
	defaultNeedVad        = 1
	defaultWordInfo       = 0
	defaultFilterDirty    = 0
	defaultFilterModal    = 0
	defaultFilterPunc     = 0
	defaultConvertNumMode = 1

	protocol = "wss"
	host     = "asr.cloud.tencent.com"
	path     = ""
)

const (
	eventTypeRecognitionStart        = 0
	eventTypeSentenceBegin           = 1
	eventTypeRecognitionResultChange = 2
	eventTypeSentenceEnd             = 3
	eventTypeRecognitionComplete     = 4
	eventTypeFail                    = 5
)

type eventType int

type speechRecognitionEvent struct {
	t   eventType
	r   *SpeechRecognitionResponse
	err error
}

// NewSpeechRecognizer creates instance of SpeechRecognizer
func NewSpeechRecognizer(appID string, credential *common.Credential, engineModelType string, listener SpeechRecognitionListener) *SpeechRecognizer {
	return &SpeechRecognizer{
		AppID:           appID,
		Credential:      credential,
		EngineModelType: engineModelType,
		VoiceFormat:     defaultVoiceFormat,
		NeedVad:         defaultNeedVad,
		FilterDirty:     defaultFilterDirty,
		FilterModal:     defaultFilterModal,
		FilterPunc:      defaultFilterPunc,
		ConvertNumMode:  defaultConvertNumMode,
		WordInfo:        defaultWordInfo,

		dataChan:   make(chan []byte, 10),
		sendEnd:    make(chan int),
		receiveEnd: make(chan int),

		eventChan: make(chan speechRecognitionEvent, 10),
		eventEnd:  make(chan int),

		listener: listener,

		started: false,
	}
}

// Start connects to server and start a recognition session
func (recognizer *SpeechRecognizer) Start() error {
	recognizer.mutex.Lock()
	defer recognizer.mutex.Unlock()

	if recognizer.started {
		return fmt.Errorf("recognizer is already started")
	}
	voiceID := uuid.New().String()
	serverURL := recognizer.buildURL(voiceID)
	signature := recognizer.genSignature(serverURL)

	recognizer.voiceID = voiceID
	dialer := websocket.Dialer{}
	if len(recognizer.ProxyURL) > 0 {
		proxyURL, _ := url.Parse(recognizer.ProxyURL)
		dialer.Proxy = http.ProxyURL(proxyURL)
	}

	header := http.Header(make(map[string][]string))
	urlStr := fmt.Sprintf("%s://%s&signature=%s", protocol, serverURL, url.QueryEscape(signature))
	conn, _, err := dialer.Dial(urlStr, header)
	if err != nil {
		return fmt.Errorf("voice_id: %s, error: %s", recognizer.voiceID, err.Error())
	}
	_, data, err := conn.ReadMessage()
	if err != nil {
		conn.Close()
		return fmt.Errorf("voice_id: %s, error: %s", recognizer.voiceID, err.Error())
	}
	msg := SpeechRecognitionResponse{}
	err = json.Unmarshal(data, &msg)
	if err != nil {
		conn.Close()
		return fmt.Errorf("voice_id: %s, error: %s", recognizer.voiceID, err.Error())
	}
	if msg.Code != 0 {
		conn.Close()
		return fmt.Errorf("voice_id: %s, code: %d, message: %s", recognizer.voiceID, msg.Code, msg.Message)
	}

	recognizer.conn = conn
	go recognizer.send()
	go recognizer.receive()
	go recognizer.eventDispatch()
	recognizer.started = true

	recognizer.eventChan <- speechRecognitionEvent{
		t:   eventTypeRecognitionStart,
		r:   newSpeechRecognitionResponse(0, "sucess", voiceID, fmt.Sprintf("%s-RecognitionStart", voiceID), 0),
		err: nil,
	}
	return nil
}

func (recognizer *SpeechRecognizer) Write(data []byte) error {
	recognizer.mutex.Lock()
	defer recognizer.mutex.Unlock()
	if !recognizer.started {
		return fmt.Errorf("recognizer not running")
	}

	recognizer.dataChan <- data
	return nil
}

// Stop wait for the recognition process to complete
func (recognizer *SpeechRecognizer) Stop() error {
	err := recognizer.stopInternal()
	if err != nil {
		return err
	}
	return nil
}

func (recognizer *SpeechRecognizer) stopInternal() error {
	recognizer.mutex.Lock()
	defer recognizer.mutex.Unlock()
	if !recognizer.started {
		return fmt.Errorf("recognizer is not running")
	}
	close(recognizer.dataChan)
	<-recognizer.receiveEnd
	<-recognizer.sendEnd
	<-recognizer.eventEnd
	recognizer.started = false
	return nil
}

func (recognizer *SpeechRecognizer) onError(code int, message string, err error) {
	recognizer.mutex.Lock()
	if !recognizer.started {
		return
	}
	recognizer.eventChan <- speechRecognitionEvent{
		t:   eventTypeFail,
		r:   newSpeechRecognitionResponse(code, message, recognizer.voiceID, fmt.Sprintf("%s-Error", recognizer.voiceID), 0),
		err: err,
	}
	recognizer.mutex.Unlock()
	go recognizer.stopInternal()
}

func (recognizer *SpeechRecognizer) send() {
	defer func() {
		close(recognizer.sendEnd)
	}()
	for data := range recognizer.dataChan {
		if err := recognizer.conn.WriteMessage(websocket.BinaryMessage, data); err != nil {
			recognizer.onError(-1, "send error", fmt.Errorf("voice_id: %s, error: %s", recognizer.voiceID, err.Error()))
			return
		}
	}
	if err := recognizer.conn.WriteMessage(websocket.TextMessage, []byte("{\"type\":\"end\"}")); err != nil {
		recognizer.onError(-1, "send error", fmt.Errorf("voice_id: %s, error: %s", recognizer.voiceID, err.Error()))
	}
}

func (recognizer *SpeechRecognizer) eventDispatch() {
	defer func() {
		close(recognizer.eventEnd)
	}()
	for e := range recognizer.eventChan {
		switch e.t {
		case eventTypeRecognitionStart:
			recognizer.listener.OnRecognitionStart(e.r)
		case eventTypeSentenceBegin:
			recognizer.listener.OnSentenceBegin(e.r)
		case eventTypeRecognitionResultChange:
			recognizer.listener.OnRecognitionResultChange(e.r)
		case eventTypeSentenceEnd:
			recognizer.listener.OnSentenceEnd(e.r)
		case eventTypeRecognitionComplete:
			recognizer.listener.OnRecognitionComplete(e.r)
		case eventTypeFail:
			recognizer.listener.OnFail(e.r, e.err)
		}
	}
}

func (recognizer *SpeechRecognizer) receive() {
	defer func() {
		close(recognizer.eventChan)
		close(recognizer.receiveEnd)
	}()
	index := -1
	for {
		_, data, err := recognizer.conn.ReadMessage()
		if err != nil {
			recognizer.onError(-1, "receive error", fmt.Errorf("voice_id: %s, error: %s", recognizer.voiceID, err.Error()))
			break
		}

		//fmt.Printf("%s", data)
		msg := SpeechRecognitionResponse{}
		err = json.Unmarshal(data, &msg)
		if err != nil {
			recognizer.onError(-1, "receive error", fmt.Errorf("voice_id: %s, error: %s", recognizer.voiceID, err.Error()))
			break
		}
		if msg.Code != 0 {
			recognizer.onError(msg.Code, msg.Message, fmt.Errorf("voiceID: %s, error code %d, message: %s", recognizer.voiceID, msg.Code, msg.Message))
			break
		}

		if msg.Final == 1 {
			recognizer.eventChan <- speechRecognitionEvent{
				t:   eventTypeRecognitionComplete,
				r:   &msg,
				err: nil,
			}
			break
		}

		beginOrEnd := false
		if msg.Result.Index != index || msg.Result.SliceType == 0 {
			index = msg.Result.Index
			recognizer.eventChan <- speechRecognitionEvent{
				t:   eventTypeSentenceBegin,
				r:   &msg,
				err: nil,
			}
			beginOrEnd = true
		}
		if msg.Result.SliceType == 2 {
			recognizer.eventChan <- speechRecognitionEvent{
				t:   eventTypeSentenceEnd,
				r:   &msg,
				err: nil,
			}
			beginOrEnd = true
		}
		if !beginOrEnd {
			recognizer.eventChan <- speechRecognitionEvent{
				t:   eventTypeRecognitionResultChange,
				r:   &msg,
				err: nil,
			}
		}
	}
}

func (recognizer *SpeechRecognizer) buildURL(voiceID string) string {
	var queryMap = make(map[string]string)
	queryMap["secretid"] = recognizer.Credential.SecretId
	queryMap["engine_model_type"] = recognizer.EngineModelType
	queryMap["voice_format"] = strconv.FormatInt(int64(recognizer.VoiceFormat), 10)
	queryMap["voice_id"] = voiceID

	var timestamp = time.Now().Unix()
	var timestampStr = strconv.FormatInt(timestamp, 10)
	queryMap["timestamp"] = timestampStr
	queryMap["expired"] = strconv.FormatInt(timestamp+24*60*60, 10)
	queryMap["nonce"] = timestampStr
	queryMap["needvad"] = strconv.FormatInt(int64(recognizer.NeedVad), 10)
	queryMap["word_info"] = strconv.FormatInt(int64(recognizer.WordInfo), 10)
	if recognizer.VadSilenceTime > 0 {
		queryMap["vad_silence_time"] = strconv.FormatInt(int64(recognizer.VadSilenceTime), 10)
	}
	queryMap["hotword_id"] = recognizer.HotwordId
	queryMap["customization_id"] = recognizer.CustomizationId
	queryMap["filter_dirty"] = strconv.FormatInt(int64(recognizer.FilterDirty), 10)
	queryMap["filter_modal"] = strconv.FormatInt(int64(recognizer.FilterModal), 10)
	queryMap["filter_punc"] = strconv.FormatInt(int64(recognizer.FilterPunc), 10)
	queryMap["convert_num_mode"] = strconv.FormatInt(int64(recognizer.ConvertNumMode), 10)

	var keys []string
	for k := range queryMap {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	var queryStrBuffer bytes.Buffer
	for _, k := range keys {
		queryStrBuffer.WriteString(k)
		queryStrBuffer.WriteString("=")
		queryStrBuffer.WriteString(queryMap[k])
		queryStrBuffer.WriteString("&")
	}

	rs := []rune(queryStrBuffer.String())
	rsLen := len(rs)
	queryStr := string(rs[0 : rsLen-1])

	url := fmt.Sprintf("%s/asr/v2/%s?%s", host, recognizer.AppID, queryStr)
	return url
}

func (recognizer *SpeechRecognizer) genSignature(url string) string {
	hmac := hmac.New(sha1.New, []byte(recognizer.Credential.SecretKey))
	signURL := url
	hmac.Write([]byte(signURL))
	encryptedStr := hmac.Sum([]byte(nil))
	var signature = base64.StdEncoding.EncodeToString(encryptedStr)

	return signature
}
