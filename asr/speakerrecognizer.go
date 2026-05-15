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
	"runtime/debug"
	"sort"
	"strconv"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/gorilla/websocket"

	"github.com/tencentcloud/tencentcloud-speech-sdk-go/common"
)

// SpeakerRecognitionListener User must implement it. Get sentence recognition result
type SpeakerRecognitionListener interface {
	OnRecognitionStart(*SpeakerRecognitionResponse)
	OnRecognitionSentences(*SpeakerRecognitionResponse)
	OnSentenceEnd(*SpeakerRecognitionResponse)
	OnFail(*SpeakerRecognitionResponse, error)
}

// SpeakerRecognitionResponse is the response of sentence mode asr service
type SpeakerRecognitionResponse struct {
	Code             int               `json:"code"`
	Message          string            `json:"message"`
	VoiceID          string            `json:"voice_id,omitempty"`
	MessageID        string            `json:"message_id,omitempty"`
	SpeakerContextId string            `json:"speaker_context_id,omitempty"`
	Final            uint32            `json:"final,omitempty"`
	Sentences        *SpeakerSentences `json:"sentences,omitempty"`
}

// SpeakerSentences wraps sentence list from server
type SpeakerSentences struct {
	SentenceList []SpeakerSentenceItem `json:"sentence_list"`
}

// SpeakerSentenceItem single sentence item
type SpeakerSentenceItem struct {
	Sentence     string `json:"sentence"`
	SentenceType int    `json:"sentence_type"`
	SentenceId   int32  `json:"sentence_id"`
	SpeakerId    int32  `json:"speaker_id"`
	StartTime    uint32 `json:"start_time"`
	EndTime      uint32 `json:"end_time"`
}

// SpeakerRecognizer is the entry for sentence mode ASR service with speaker context
type SpeakerRecognizer struct {
	//request params
	AppID            string
	EngineModelType  string
	VoiceFormat      int
	NeedVad          int
	HotwordId        string
	HotwordList      string
	CustomizationId  string
	ConvertNumMode   int
	VadSilenceTime   int
	ReinforceHotword int
	NoiseThreshold   float64
	ReplaceTextId    string
	SentenceStrategy int
	Domain           int

	//speaker context
	SpeakerDiarization   int
	EnableSpeakerContext int
	SpeakerContextId     string
	LanguageJudgment     int
	EmotionRecognition   int

	Credential *common.Credential
	//listener
	listener SpeakerRecognitionListener
	//uuid for voice
	VoiceID string

	//for proxy
	ProxyURL string

	//for websocket connection
	conn *websocket.Conn

	//send data channel
	dataChan chan []byte
	//for listener get response message
	eventChan chan speakerRecognitionEvent

	//used in stop function, waiting for stop all goroutines
	sendEnd    chan int
	receiveEnd chan int
	eventEnd   chan int

	mutex   sync.Mutex
	started bool
}

const (
	speakerEventTypeRecognitionStart    = 0
	speakerEventTypeRecognitionSentence = 1
	speakerEventTypeSentenceEnd         = 2
	speakerEventTypeFail                = 3
)

// speaker recognizer default values (self-contained, no dependency on speechrecognizer.go)
const (
	speakerDefaultVoiceFormat      = 1
	speakerDefaultNeedVad          = 1
	speakerDefaultConvertNumMode   = 1
	speakerDefaultReinforceHotword = 0

	speakerProtocol = "wss"
	speakerHost     = "asr.cloud.tencent.com"
)

// SpeakerAudioFormat type
const (
	SpeakerAudioFormatPCM   = 1
	SpeakerAudioFormatSpeex = 4
	SpeakerAudioFormatSilk  = 6
	SpeakerAudioFormatMp3   = 8
	SpeakerAudioFormatOpus  = 10
	SpeakerAudioFormatWav   = 12
	SpeakerAudioFormatM4A   = 14
	SpeakerAudioFormatAAC   = 16
)

type speakerEventType int

type speakerRecognitionEvent struct {
	t   speakerEventType
	r   *SpeakerRecognitionResponse
	err error
}

// NewSpeakerRecognizer creates instance of SpeakerRecognizer
func NewSpeakerRecognizer(appID string, credential *common.Credential, engineModelType string,
	listener SpeakerRecognitionListener) *SpeakerRecognizer {

	reco := &SpeakerRecognizer{
		AppID:            appID,
		Credential:       credential,
		EngineModelType:  engineModelType,
		VoiceFormat:      speakerDefaultVoiceFormat,
		NeedVad:          speakerDefaultNeedVad,
		ConvertNumMode:   speakerDefaultConvertNumMode,
		ReinforceHotword: speakerDefaultReinforceHotword,
		SentenceStrategy: 1,
		SpeakerDiarization: 1,

		dataChan:  make(chan []byte, 6400),
		eventChan: make(chan speakerRecognitionEvent, 10),

		sendEnd:    make(chan int),
		receiveEnd: make(chan int),
		eventEnd:   make(chan int),

		listener: listener,
		started:  false,
	}
	return reco
}

// Start connects to server and start a sentence recognition session
func (recognizer *SpeakerRecognizer) Start() error {
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

	dialer := websocket.Dialer{}
	if len(recognizer.ProxyURL) > 0 {
		proxyURL, _ := url.Parse(recognizer.ProxyURL)
		dialer.Proxy = http.ProxyURL(proxyURL)
	}

	header := http.Header(make(map[string][]string))
	urlStr := fmt.Sprintf("%s://%s&signature=%s", speakerProtocol, serverURL, url.QueryEscape(signature))
	conn, _, err := dialer.Dial(urlStr, header)
	if err != nil {
		return fmt.Errorf("voice_id: %s, error: %s", recognizer.VoiceID, err.Error())
	}
	_, data, err := conn.ReadMessage()
	if err != nil {
		conn.Close()
		return fmt.Errorf("voice_id: %s, error: %s", recognizer.VoiceID, err.Error())
	}
	msg := SpeakerRecognitionResponse{}
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

	// 回写服务端返回的 speaker_context_id
	if msg.SpeakerContextId != "" {
		recognizer.SpeakerContextId = msg.SpeakerContextId
	}

	recognizer.conn = conn
	go recognizer.send()
	go recognizer.receive()
	go recognizer.eventDispatch()
	recognizer.started = true

	startResp := newSpeakerRecognitionResponse(0, "success", recognizer.VoiceID,
		fmt.Sprintf("%s-RecognitionStart", recognizer.VoiceID), 0)
	startResp.SpeakerContextId = recognizer.SpeakerContextId
	recognizer.eventChan <- speakerRecognitionEvent{
		t:   speakerEventTypeRecognitionStart,
		r:   startResp,
		err: nil,
	}
	return nil
}

// Write : write data in channel
func (recognizer *SpeakerRecognizer) Write(data []byte) error {
	recognizer.mutex.Lock()
	defer recognizer.mutex.Unlock()
	if !recognizer.started {
		return fmt.Errorf("recognizer not running")
	}

	recognizer.dataChan <- data
	return nil
}

// Stop wait for the recognition process to complete
func (recognizer *SpeakerRecognizer) Stop() error {
	err := recognizer.stopInternal()
	if err != nil {
		return err
	}
	return nil
}

func (recognizer *SpeakerRecognizer) stopInternal() error {
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

func (recognizer *SpeakerRecognizer) onError(code int, message string, err error) {
	if !recognizer.started {
		return
	}

	recognizer.listener.OnFail(newSpeakerRecognitionResponse(code,
		fmt.Sprintf("%s: %v", message, err), recognizer.VoiceID,
		fmt.Sprintf("%s-Error", recognizer.VoiceID), 0), err)
	go recognizer.stopInternal()
}

func (recognizer *SpeakerRecognizer) send() {
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

func (recognizer *SpeakerRecognizer) eventDispatch() {
	defer func() {
		// handle panic
		recognizer.genRecoverFunc()()
		close(recognizer.eventEnd)
	}()
	for e := range recognizer.eventChan {
		switch e.t {
		case speakerEventTypeRecognitionStart:
			recognizer.listener.OnRecognitionStart(e.r)
		case speakerEventTypeRecognitionSentence:
			recognizer.listener.OnRecognitionSentences(e.r)
		case speakerEventTypeSentenceEnd:
			recognizer.listener.OnSentenceEnd(e.r)
		case speakerEventTypeFail:
			recognizer.listener.OnFail(e.r, e.err)
		}
	}
}

func (recognizer *SpeakerRecognizer) receive() {
	defer func() {
		// handle panic
		recognizer.genRecoverFunc()()
		close(recognizer.eventChan)
		close(recognizer.receiveEnd)
	}()
	for {
		_, data, err := recognizer.conn.ReadMessage()
		if err != nil {
			recognizer.onError(-1, "receive error", fmt.Errorf("voice_id: %s, error: %s",
				recognizer.VoiceID, err.Error()))
			break
		}

		msg := SpeakerRecognitionResponse{}
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
			recognizer.eventChan <- speakerRecognitionEvent{
				t:   speakerEventTypeSentenceEnd,
				r:   &msg,
				err: nil,
			}
			break
		}

		// 句子模式：每条消息都是句子列表
		recognizer.eventChan <- speakerRecognitionEvent{
			t:   speakerEventTypeRecognitionSentence,
			r:   &msg,
			err: nil,
		}
	}
}

func (recognizer *SpeakerRecognizer) buildURL(voiceID string) string {
	var queryMap = make(map[string]string)
	queryMap["secretid"] = recognizer.Credential.SecretId
	var timestamp = time.Now().Unix()
	var timestampStr = strconv.FormatInt(timestamp, 10)
	queryMap["timestamp"] = timestampStr
	queryMap["expired"] = strconv.FormatInt(timestamp+24*60*60, 10)
	queryMap["nonce"] = timestampStr

	//params
	queryMap["engine_model_type"] = recognizer.EngineModelType
	queryMap["voice_id"] = voiceID
	queryMap["voice_format"] = strconv.FormatInt(int64(recognizer.VoiceFormat), 10)
	queryMap["needvad"] = strconv.FormatInt(int64(recognizer.NeedVad), 10)

	// 强制句子模式
	queryMap["result_mod"] = "1"
	queryMap["sentence_strategy"] = strconv.FormatInt(int64(recognizer.SentenceStrategy), 10)

	// speaker context
	queryMap["speaker_diarization"] = strconv.FormatInt(int64(recognizer.SpeakerDiarization), 10)
	queryMap["enable_speaker_context"] = strconv.FormatInt(int64(recognizer.EnableSpeakerContext), 10)
	queryMap["speaker_context_id"] = recognizer.SpeakerContextId
	queryMap["language_judgment"] = strconv.FormatInt(int64(recognizer.LanguageJudgment), 10)
	queryMap["emotion_recognition"] = strconv.FormatInt(int64(recognizer.EmotionRecognition), 10)

	if recognizer.HotwordId != "" {
		queryMap["hotword_id"] = recognizer.HotwordId
	}
	if recognizer.HotwordList != "" {
		queryMap["hotword_list"] = recognizer.HotwordList
	}
	if recognizer.CustomizationId != "" {
		queryMap["customization_id"] = recognizer.CustomizationId
	}
	if recognizer.ReplaceTextId != "" {
		queryMap["replace_text_id"] = recognizer.ReplaceTextId
	}
	if recognizer.Domain > 0 {
		queryMap["domain"] = strconv.FormatInt(int64(recognizer.Domain), 10)
	}

	queryMap["convert_num_mode"] = strconv.FormatInt(int64(recognizer.ConvertNumMode), 10)
	queryMap["reinforce_hotword"] = strconv.FormatInt(int64(recognizer.ReinforceHotword), 10)
	if recognizer.VadSilenceTime > 0 {
		queryMap["vad_silence_time"] = strconv.FormatInt(int64(recognizer.VadSilenceTime), 10)
	}
	if recognizer.NoiseThreshold != 0 {
		queryMap["noise_threshold"] = strconv.FormatFloat(recognizer.NoiseThreshold, 'f', 3, 64)
	}

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
	url := fmt.Sprintf("%s/asr/v2/%s?%s", speakerHost, recognizer.AppID, queryStr)
	return url
}

func (recognizer *SpeakerRecognizer) genSignature(url string) string {
	hmac := hmac.New(sha1.New, []byte(recognizer.Credential.SecretKey))
	signURL := url
	hmac.Write([]byte(signURL))
	encryptedStr := hmac.Sum([]byte(nil))
	var signature = base64.StdEncoding.EncodeToString(encryptedStr)

	return signature
}

func newSpeakerRecognitionResponse(code int, message string, voiceID string,
	messageID string, final uint32) *SpeakerRecognitionResponse {
	return &SpeakerRecognitionResponse{
		Code:      code,
		Message:   message,
		VoiceID:   voiceID,
		MessageID: messageID,
		Final:     final,
	}
}

func (recognizer *SpeakerRecognizer) genRecoverFunc() func() {
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
			recognizer.eventChan <- speakerRecognitionEvent{
				t: speakerEventTypeFail,
				r: newSpeakerRecognitionResponse(-1, "panic error", recognizer.VoiceID,
					fmt.Sprintf("%s-Error", recognizer.VoiceID), 0),
				err: retErr,
			}
		}
	}
}
