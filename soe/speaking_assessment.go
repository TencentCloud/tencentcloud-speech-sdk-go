package soe

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha1"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"runtime/debug"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/gorilla/websocket"

	"github.com/tencentcloud/tencentcloud-speech-sdk-go/common"
)

// SpeakingAssessmentListener User must impletement it. Get recognition result
type SpeakingAssessmentListener interface {
	OnRecognitionStart(*SpeakingAssessmentResponse)
	OnRecognitionComplete(*SpeakingAssessmentResponse)
	OnIntermediateResults(*SpeakingAssessmentResponse)
	OnFail(*SpeakingAssessmentResponse, error)
}

// SpeakingAssessmentResponse is the reponse of asr service
type SpeakingAssessmentResponse struct {
	Code      int          `json:"code"`
	Message   string       `json:"message"`
	VoiceID   string       `json:"voice_id,omitempty"`
	MessageID string       `json:"message_id,omitempty"`
	Final     uint32       `json:"final,omitempty"`
	Result    SentenceInfo `json:"result"`
}

// SentenceInfo ...
type SentenceInfo struct {
	SuggestedScore float64   `json:"SuggestedScore"`
	PronAccuracy   float64   `json:"PronAccuracy"`
	PronFluency    float64   `json:"PronFluency"`
	PronCompletion float64   `json:"PronCompletion"`
	Words          []WordRsp `json:"Words"`
	SentenceId     int64     `json:"SentenceId"`
	RefTextId      int64     `json:"RefTextId"`
	KeyWordHits    []float32 `json:"KeyWordHits"`
	UnKeyWordHits  []float32 `json:"UnKeyWordHits"`
}

// PhoneInfoTypeRsp is a struct/interface
type PhoneInfoTypeRsp struct {
	Mbtm            int64   `json:"MemBeginTime"`
	Metm            int64   `json:"MemEndTime"`
	PronAccuracy    float64 `json:"PronAccuracy"`
	DetectedStress  bool    `json:"DetectedStress"`
	Phone           string  `json:"Phone"`
	ReferencePhone  string  `json:"ReferencePhone"`
	ReferenceLetter string  `json:"ReferenceLetter"`
	Stress          bool    `json:"Stress"`
	Tag             int64   `json:"MatchTag"`
}

// Tone 中文声调检测结果
type Tone struct {
	Valid   bool `json:"Valid"`
	RefTone int  `json:"RefTone"`
	HypTone int  `json:"HypothesisTone"`
	// Confidence float32 `json:"Confidence"`
}

// WordRsp is a struct/interface
type WordRsp struct {
	Mbtm          int64              `json:"MemBeginTime"`
	Metm          int64              `json:"MemEndTime"`
	PronAccuracy  float64            `json:"PronAccuracy"`
	PronFluency   float64            `json:"PronFluency"`
	ReferenceWord string             `json:"ReferenceWord"`
	Word          string             `json:"Word"`
	Tag           int64              `json:"MatchTag"`
	KeywordTag    int64              `json:"KeywordTag"`
	PhoneInfo     []PhoneInfoTypeRsp `json:"PhoneInfos"`
	Tone          Tone               `json:"Tone"`
}

// AudioFormat type
const (
	AudioFormatPCM   = 0
	AudioFormatWav   = 1
	AudioFormatMp3   = 2
	AudioFormatSilk  = 3
	AudioFormatSpeex = 4
)

// SpeechRecognizer is the entry for ASR service
type SpeechRecognizer struct {
	//request params
	AppID               string
	VoiceFormat         int
	End                 int
	Timestamp           int
	Nonce               int
	Signature           string
	VoiceData           []byte
	Expired             int
	TextMode            int64
	RefText             string
	Keyword             string
	EvalMode            int64
	ScoreCoeff          float64
	ServerEngineType    string
	SentenceInfoEnabled int64

	Credential *common.Credential
	//listener
	listener SpeakingAssessmentListener
	//uuid for voice
	VoiceID string
	//for proxy
	ProxyURL string
	//for websocet connection
	conn *websocket.Conn
	//send data channel
	dataChan chan []byte
	//for listener get response message
	eventChan chan speechRecognitionEvent

	//used in stop function, waiting for stop all goroutines
	sendEnd    chan int
	receiveEnd chan int
	eventEnd   chan int

	mutex   sync.Mutex
	started bool
	hasEnd  bool
}

const (
	defaultVoiceFormat = 1

	protocol = "wss"
	host     = "soe.cloud.tencent.com"
	path     = "soe/api"
)

const (
	eventTypeRecognitionStart    = 1
	eventTypeIntermediateResults = 2
	eventTypeRecognitionComplete = 3
	eventTypeFail                = 4
)

type eventType int

type speechRecognitionEvent struct {
	t   eventType
	r   *SpeakingAssessmentResponse
	err error
}

// NewSpeechRecognizer creates instance of SpeechRecognizer
func NewSpeechRecognizer(appID string, credential *common.Credential,
	listener SpeakingAssessmentListener) *SpeechRecognizer {

	reco := &SpeechRecognizer{
		AppID:               appID,
		VoiceFormat:         defaultVoiceFormat,
		End:                 0,
		Timestamp:           0,
		Nonce:               0,
		Signature:           "",
		VoiceData:           nil,
		Expired:             0,
		TextMode:            0,
		RefText:             "",
		Keyword:             "",
		EvalMode:            0,
		ScoreCoeff:          1.0,
		ServerEngineType:    "16k_en",
		SentenceInfoEnabled: 0,
		Credential:          credential,
		listener:            listener,
		VoiceID:             "",
		ProxyURL:            "",
		conn:                nil,
		dataChan:            make(chan []byte, 6400),
		eventChan:           make(chan speechRecognitionEvent, 10),
		sendEnd:             make(chan int),
		receiveEnd:          make(chan int),
		eventEnd:            make(chan int),
		mutex:               sync.Mutex{},
		started:             false,
		hasEnd:              false,
	}
	return reco
}

// Start connects to server and start a recognition session
func (recognizer *SpeechRecognizer) Start() error {
	recognizer.mutex.Lock()
	defer recognizer.mutex.Unlock()

	if recognizer.started {
		return fmt.Errorf("recognizer is already started")
	}
	if recognizer.VoiceID == "" {
		voiceID := uuid.New().String()
		recognizer.VoiceID = voiceID
	}
	serverURL := recognizer.buildURL(recognizer.VoiceID)
	signature := recognizer.genSignature(serverURL)
	serverURL = serverURL[strings.Index(serverURL, "?")+1:]
	//请求参数进行转义
	serverURL = fmt.Sprintf("%s/%s/%s?%s", host, path, recognizer.AppID, url.PathEscape(serverURL))
	dialer := websocket.Dialer{}
	if len(recognizer.ProxyURL) > 0 {
		proxyURL, _ := url.Parse(recognizer.ProxyURL)
		dialer.Proxy = http.ProxyURL(proxyURL)
	}

	header := http.Header(make(map[string][]string))
	urlStr := fmt.Sprintf("%s://%s&signature=%s", protocol, serverURL, url.QueryEscape(signature))

	conn, _, err := dialer.Dial(urlStr, header)
	if err != nil {
		return fmt.Errorf("voice_id: %s, error: %s", recognizer.VoiceID, err.Error())
	}
	_, data, err := conn.ReadMessage()
	if err != nil {
		conn.Close()
		return fmt.Errorf("voice_id: %s, error: %s", recognizer.VoiceID, err.Error())
	}
	msg := SpeakingAssessmentResponse{}
	err = json.Unmarshal(data, &msg)
	if err != nil {
		conn.Close()
		return fmt.Errorf("voice_id: %s, error: %s", recognizer.VoiceID, err.Error())
	}
	if msg.Code != 0 {
		conn.Close()
		return fmt.Errorf("voice_id: %s, code: %d, message: %s",
			recognizer.VoiceID, msg.Code, msg.Message)
	}

	recognizer.conn = conn
	go recognizer.send()
	go recognizer.receive()
	go recognizer.eventDispatch()
	recognizer.started = true

	recognizer.eventChan <- speechRecognitionEvent{
		t: eventTypeRecognitionStart,
		r: newSpeechRecognitionResponse(0, "success", recognizer.VoiceID,
			fmt.Sprintf("%s-RecognitionStart", recognizer.VoiceID), 0),
		err: nil,
	}
	return nil
}

// Write : write data in channel
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
	err := recognizer.conn.Close()
	if err != nil {
		return err
	}
	return nil
}

func (recognizer *SpeechRecognizer) onError(code int, message string, err error) {
	if !recognizer.started {
		return
	}
	recognizer.listener.OnFail(newSpeechRecognitionResponse(code, message, recognizer.VoiceID,
		fmt.Sprintf("%s-Error", recognizer.VoiceID), 0), err)
	go recognizer.stopInternal()
}

func (recognizer *SpeechRecognizer) send() {
	defer func() {
		// handle panic
		recognizer.genRecoverFunc()()
		close(recognizer.sendEnd)
	}()
	//send data
	for data := range recognizer.dataChan {
		if err := recognizer.conn.WriteMessage(websocket.BinaryMessage, data); err != nil {
			recognizer.onError(-1, "send error", fmt.Errorf("voice_id: %s, error: %s",
				recognizer.VoiceID, err.Error()))
			return
		}
	}
	//send stop msg
	if err := recognizer.conn.WriteMessage(websocket.TextMessage, []byte("{\"type\":\"end\"}")); err != nil {
		recognizer.onError(-1, "send error", fmt.Errorf("voice_id: %s, error: %s",
			recognizer.VoiceID, err.Error()))
	}
}

func (recognizer *SpeechRecognizer) eventDispatch() {
	defer func() {
		// handle panic
		recognizer.genRecoverFunc()()
		close(recognizer.eventEnd)
	}()
	for e := range recognizer.eventChan {
		switch e.t {
		case eventTypeRecognitionStart:
			recognizer.listener.OnRecognitionStart(e.r)
		case eventTypeIntermediateResults:
			recognizer.listener.OnIntermediateResults(e.r)
		case eventTypeRecognitionComplete:
			recognizer.listener.OnRecognitionComplete(e.r)
		case eventTypeFail:
			recognizer.listener.OnFail(e.r, e.err)
		}
	}
}

func (recognizer *SpeechRecognizer) receive() {
	defer func() {
		// handle panic
		recognizer.genRecoverFunc()()
		close(recognizer.eventChan)
		close(recognizer.receiveEnd)
	}()
	for {
		_, data, err := recognizer.conn.ReadMessage()
		if err != nil {
			recognizer.onError(-1, "receive error", fmt.Errorf("voice_id: %s, error: %s", recognizer.VoiceID, err.Error()))
			break
		}

		//fmt.Printf("%s", data)
		msg := SpeakingAssessmentResponse{}
		err = json.Unmarshal(data, &msg)
		if err != nil {
			recognizer.onError(-1, "receive error",
				fmt.Errorf("voice_id: %s, error: %s",
					recognizer.VoiceID, err.Error()))
			break
		}
		if msg.Code != 0 {
			recognizer.onError(msg.Code, msg.Message,
				fmt.Errorf("VoiceID: %s, error code %d, message: %s",
					recognizer.VoiceID, msg.Code, msg.Message))
			break
		}
		if msg.Final == 1 {
			recognizer.hasEnd = true
			recognizer.eventChan <- speechRecognitionEvent{
				t:   eventTypeRecognitionComplete,
				r:   &msg,
				err: nil,
			}
			break
		} else {
			recognizer.eventChan <- speechRecognitionEvent{
				t:   eventTypeIntermediateResults,
				r:   &msg,
				err: nil,
			}
		}
	}
}

func (recognizer *SpeechRecognizer) buildURL(voiceID string) string {
	var queryMap = make(map[string]string)
	queryMap["secretid"] = recognizer.Credential.SecretId
	var timestamp = time.Now().Unix()
	var timestampStr = strconv.FormatInt(timestamp, 10)
	queryMap["timestamp"] = timestampStr
	queryMap["expired"] = strconv.FormatInt(timestamp+24*60*60, 10)
	queryMap["nonce"] = timestampStr
	//params
	queryMap["voice_id"] = voiceID
	queryMap["voice_format"] = strconv.FormatInt(int64(recognizer.VoiceFormat), 10)
	queryMap["text_mode"] = strconv.FormatInt(recognizer.TextMode, 10)
	queryMap["ref_text"] = recognizer.RefText
	queryMap["keyword"] = recognizer.Keyword
	queryMap["eval_mode"] = strconv.FormatInt(recognizer.EvalMode, 10)
	queryMap["score_coeff"] = fmt.Sprintf("%1f", recognizer.ScoreCoeff)
	queryMap["server_engine_type"] = recognizer.ServerEngineType
	queryMap["sentence_info_enabled"] = strconv.FormatInt(int64(recognizer.SentenceInfoEnabled), 10)

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

	//gen url
	url := fmt.Sprintf("%s/%s/%s?%s", host, path, recognizer.AppID, queryStr)
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

func newSpeechRecognitionResponse(code int, message string, voiceID string,
	messageID string, final uint32) *SpeakingAssessmentResponse {
	return &SpeakingAssessmentResponse{
		Code:      code,
		Message:   message,
		VoiceID:   voiceID,
		MessageID: messageID,
		Final:     final,
	}
}

func (recognizer *SpeechRecognizer) genRecoverFunc() func() {
	return func() {
		if r := recover(); r != nil {
			var err error
			switch r := r.(type) {
			case error:
				err = r
			default:
				err = fmt.Errorf("%v", r)
			}
			retErr := fmt.Errorf("panic error ocurred! [err: %s] [stack: %s]",
				err.Error(), string(debug.Stack()))
			recognizer.eventChan <- speechRecognitionEvent{
				t: eventTypeFail,
				r: newSpeechRecognitionResponse(-1, "panic error", recognizer.VoiceID,
					fmt.Sprintf("%s-Error", recognizer.VoiceID), 0),
				err: retErr,
			}
		}
	}
}
