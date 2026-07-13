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

// RealtimeRecognitionListenerV2 User must implement it. Get sentence recognition result
type RealtimeRecognitionListenerV2 interface {
	OnRecognitionStart(*RealtimeRecognitionResponseV2)
	OnRecognitionSentences(*RealtimeRecognitionResponseV2)
	OnSentenceEnd(*RealtimeRecognitionResponseV2)
	OnFail(*RealtimeRecognitionResponseV2, error)
}

// RealtimeRecognitionResponseV2 is the response of realtime v2 asr service
type RealtimeRecognitionResponseV2 struct {
	Code             int                  `json:"code"`
	Message          string               `json:"message"`
	VoiceID          string               `json:"voice_id,omitempty"`
	MessageID        string               `json:"message_id,omitempty"`
	SpeakerContextId string               `json:"speaker_context_id,omitempty"`
	Final            uint32               `json:"final,omitempty"`
	Sentences        *RealtimeSentencesV2 `json:"sentences,omitempty"`

	// RawMessage 服务端返回的原始 JSON 字节。
	// 仅当用户通过 SetExtraParam/SetExtraParams 设置了扩展参数时填充，
	// 用于业务侧解析 SDK 未显式定义的扩展响应字段。
	// 未使用扩展参数的调用方该字段恒为 nil。
	// 不参与 JSON 序列化（json:"-"）。
	RawMessage []byte `json:"-"`
}

// RealtimeSentencesV2 wraps sentence list from server
type RealtimeSentencesV2 struct {
	SentenceList []RealtimeSentenceItemV2 `json:"sentence_list"`
}

// RealtimeSentenceItemV2 single sentence item
type RealtimeSentenceItemV2 struct {
	Sentence     string `json:"sentence"`
	SentenceType int    `json:"sentence_type"`
	SentenceId   int32  `json:"sentence_id"`
	SpeakerId    int32  `json:"speaker_id"`
	StartTime    uint32 `json:"start_time"`
	EndTime      uint32 `json:"end_time"`
}

// RealtimeRecognizerV2 is the entry for realtime v2 ASR service.
// 首版功能与 speaker 版等价：句子模式实时识别，话者分离作为可选开关。
type RealtimeRecognizerV2 struct {
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

	//speaker context（话者分离相关能力，默认关闭）
	SpeakerDiarization   int
	EnableSpeakerContext int
	SpeakerContextId     string
	LanguageJudgment     int
	EmotionRecognition   int

	// ExtraParams 扩展参数：用于支持隐藏参数或后续新增参数，无需升级 SDK。
	// 这里的 key/value 会以 query string 形式拼接到 ws URL 上，并参与签名计算。
	// 同名 key 会覆盖 SDK 默认填充的字段（如 voice_format、needvad 等），
	// 这是为隐藏参数的"覆盖默认值"场景刻意保留的能力。
	ExtraParams map[string]string

	Credential *common.Credential
	//listener
	listener RealtimeRecognitionListenerV2
	//uuid for voice
	VoiceID string

	//for proxy
	ProxyURL string

	//for websocket connection
	conn *websocket.Conn

	//send data channel
	dataChan chan []byte
	//for listener get response message
	eventChan chan realtimeV2RecognitionEvent

	//used in stop function, waiting for stop all goroutines
	sendEnd    chan int
	receiveEnd chan int
	eventEnd   chan int

	mutex   sync.Mutex
	started bool
	// closed 表示该实例已被 Stop 过，进入终止态后不允许再次 Start。
	// 这个 SDK 的设计是每个实例对应一次 ws 会话，复用实例需要新建。
	closed bool
	// errOnce 用于保证 OnFail 回调只触发一次。
	// 网络断开时 send/receive 会并发报错，没有 Once 会造成同一错误被回调多次。
	errOnce sync.Once
}

const (
	realtimeV2EventTypeRecognitionStart    = 0
	realtimeV2EventTypeRecognitionSentence = 1
	realtimeV2EventTypeSentenceEnd         = 2
	realtimeV2EventTypeFail                = 3
)

// realtime v2 recognizer default values (self-contained, no dependency on other recognizers)
const (
	realtimeV2DefaultVoiceFormat      = 1
	realtimeV2DefaultNeedVad          = 1
	realtimeV2DefaultConvertNumMode   = 1
	realtimeV2DefaultReinforceHotword = 0

	realtimeV2Protocol = "wss"
	realtimeV2Host     = "asr.cloud.tencent.com"
)

// RealtimeV2AudioFormat type
const (
	RealtimeV2AudioFormatPCM   = 1
	RealtimeV2AudioFormatSpeex = 4
	RealtimeV2AudioFormatSilk  = 6
	RealtimeV2AudioFormatMp3   = 8
	RealtimeV2AudioFormatOpus  = 10
	RealtimeV2AudioFormatWav   = 12
	RealtimeV2AudioFormatM4A   = 14
	RealtimeV2AudioFormatAAC   = 16
)

type realtimeV2EventType int

type realtimeV2RecognitionEvent struct {
	t   realtimeV2EventType
	r   *RealtimeRecognitionResponseV2
	err error
}

// NewRealtimeRecognizerV2 creates instance of RealtimeRecognizerV2.
// 默认关闭话者分离（SpeakerDiarization=0），需要时由调用方显式打开。
func NewRealtimeRecognizerV2(appID string, credential *common.Credential, engineModelType string,
	listener RealtimeRecognitionListenerV2) *RealtimeRecognizerV2 {

	reco := &RealtimeRecognizerV2{
		AppID:              appID,
		Credential:         credential,
		EngineModelType:    engineModelType,
		VoiceFormat:        realtimeV2DefaultVoiceFormat,
		NeedVad:            realtimeV2DefaultNeedVad,
		ConvertNumMode:     realtimeV2DefaultConvertNumMode,
		ReinforceHotword:   realtimeV2DefaultReinforceHotword,
		SentenceStrategy:   1,
		SpeakerDiarization: 0,

		dataChan:  make(chan []byte, 6400),
		eventChan: make(chan realtimeV2RecognitionEvent, 10),

		sendEnd:    make(chan int),
		receiveEnd: make(chan int),
		eventEnd:   make(chan int),

		listener: listener,
		started:  false,
	}
	return reco
}

// Start connects to server and start a sentence recognition session
func (recognizer *RealtimeRecognizerV2) Start() error {
	recognizer.mutex.Lock()
	defer recognizer.mutex.Unlock()

	if recognizer.closed {
		return fmt.Errorf("recognizer has been stopped, please create a new instance")
	}
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
	urlStr := fmt.Sprintf("%s://%s&signature=%s", realtimeV2Protocol, serverURL, url.QueryEscape(signature))
	conn, _, err := dialer.Dial(urlStr, header)
	if err != nil {
		return fmt.Errorf("voice_id: %s, error: %s", recognizer.VoiceID, err.Error())
	}
	_, data, err := conn.ReadMessage()
	if err != nil {
		conn.Close()
		return fmt.Errorf("voice_id: %s, error: %s", recognizer.VoiceID, err.Error())
	}
	msg := RealtimeRecognitionResponseV2{}
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

	startResp := newRealtimeRecognitionResponseV2(0, "success", recognizer.VoiceID,
		fmt.Sprintf("%s-RecognitionStart", recognizer.VoiceID), 0)
	startResp.SpeakerContextId = recognizer.SpeakerContextId
	recognizer.eventChan <- realtimeV2RecognitionEvent{
		t:   realtimeV2EventTypeRecognitionStart,
		r:   startResp,
		err: nil,
	}
	return nil
}

// Write : write data in channel
func (recognizer *RealtimeRecognizerV2) Write(data []byte) error {
	recognizer.mutex.Lock()
	defer recognizer.mutex.Unlock()
	if !recognizer.started {
		return fmt.Errorf("recognizer not running")
	}

	recognizer.dataChan <- data
	return nil
}

// Stop wait for the recognition process to complete
func (recognizer *RealtimeRecognizerV2) Stop() error {
	err := recognizer.stopInternal()
	if err != nil {
		return err
	}
	return nil
}

func (recognizer *RealtimeRecognizerV2) stopInternal() error {
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
	// 置为终止态：禁止再次 Start，避免向已 close 的 channel 发送/重复 close 导致 panic。
	recognizer.closed = true
	return nil
}

func (recognizer *RealtimeRecognizerV2) onError(code int, message string, err error) {
	// 只触发一次：避免 send/receive 同时报错导致 OnFail 被回调多次。
	recognizer.errOnce.Do(func() {
		if !recognizer.started {
			return
		}
		recognizer.listener.OnFail(newRealtimeRecognitionResponseV2(code,
			fmt.Sprintf("%s: %v", message, err), recognizer.VoiceID,
			fmt.Sprintf("%s-Error", recognizer.VoiceID), 0), err)
		go recognizer.stopInternal()
	})
}

func (recognizer *RealtimeRecognizerV2) send() {
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

func (recognizer *RealtimeRecognizerV2) eventDispatch() {
	defer func() {
		// handle panic
		recognizer.genRecoverFunc()()
		close(recognizer.eventEnd)
	}()
	for e := range recognizer.eventChan {
		switch e.t {
		case realtimeV2EventTypeRecognitionStart:
			recognizer.listener.OnRecognitionStart(e.r)
		case realtimeV2EventTypeRecognitionSentence:
			recognizer.listener.OnRecognitionSentences(e.r)
		case realtimeV2EventTypeSentenceEnd:
			recognizer.listener.OnSentenceEnd(e.r)
		case realtimeV2EventTypeFail:
			recognizer.listener.OnFail(e.r, e.err)
		}
	}
}

func (recognizer *RealtimeRecognizerV2) receive() {
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

		msg := RealtimeRecognitionResponseV2{}
		err = json.Unmarshal(data, &msg)
		if err != nil {
			recognizer.onError(-1, "receive error",
				fmt.Errorf("voice_id: %s, error: %s",
					recognizer.VoiceID, err.Error()))
			break
		}
		// 仅当用户使用了扩展参数时回填原始消息，便于业务解析 SDK 未显式定义的字段。
		// 未用 ExtraParams 的调用方此字段恒为 nil。
		if len(recognizer.ExtraParams) > 0 {
			msg.RawMessage = data
		}
		if msg.Code != 0 {
			recognizer.onError(msg.Code, msg.Message,
				fmt.Errorf("VoiceID: %s, error code %d, message: %s",
					recognizer.VoiceID, msg.Code, msg.Message))
			break
		}

		if msg.Final == 1 {
			recognizer.eventChan <- realtimeV2RecognitionEvent{
				t:   realtimeV2EventTypeSentenceEnd,
				r:   &msg,
				err: nil,
			}
			break
		}

		// 句子模式：每条消息都是句子列表
		recognizer.eventChan <- realtimeV2RecognitionEvent{
			t:   realtimeV2EventTypeRecognitionSentence,
			r:   &msg,
			err: nil,
		}
	}
}

func (recognizer *RealtimeRecognizerV2) buildURL(voiceID string) string {
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

	// 合并扩展参数：放在最后，允许覆盖 SDK 默认填充的字段；
	// 空 key 直接忽略，避免污染 query 串。
	for k, v := range recognizer.ExtraParams {
		if k == "" {
			continue
		}
		queryMap[k] = v
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
	url := fmt.Sprintf("%s/asr/v2/%s?%s", realtimeV2Host, recognizer.AppID, queryStr)
	return url
}

func (recognizer *RealtimeRecognizerV2) genSignature(url string) string {
	hmac := hmac.New(sha1.New, []byte(recognizer.Credential.SecretKey))
	signURL := url
	hmac.Write([]byte(signURL))
	encryptedStr := hmac.Sum([]byte(nil))
	var signature = base64.StdEncoding.EncodeToString(encryptedStr)

	return signature
}

func newRealtimeRecognitionResponseV2(code int, message string, voiceID string,
	messageID string, final uint32) *RealtimeRecognitionResponseV2 {
	return &RealtimeRecognitionResponseV2{
		Code:      code,
		Message:   message,
		VoiceID:   voiceID,
		MessageID: messageID,
		Final:     final,
	}
}

func (recognizer *RealtimeRecognizerV2) genRecoverFunc() func() {
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
			recognizer.eventChan <- realtimeV2RecognitionEvent{
				t: realtimeV2EventTypeFail,
				r: newRealtimeRecognitionResponseV2(-1, "panic error", recognizer.VoiceID,
					fmt.Sprintf("%s-Error", recognizer.VoiceID), 0),
				err: retErr,
			}
		}
	}
}

// SetExtraParam 设置单个扩展参数，便于调用方动态追加隐藏/新增参数。
// 同名 key 会覆盖 SDK 默认填充的字段。
func (recognizer *RealtimeRecognizerV2) SetExtraParam(key, value string) {
	if key == "" {
		return
	}
	if recognizer.ExtraParams == nil {
		recognizer.ExtraParams = make(map[string]string)
	}
	recognizer.ExtraParams[key] = value
}

// SetExtraParams 批量设置扩展参数，会与已有的 ExtraParams 合并（同名覆盖）。
func (recognizer *RealtimeRecognizerV2) SetExtraParams(params map[string]string) {
	if len(params) == 0 {
		return
	}
	if recognizer.ExtraParams == nil {
		recognizer.ExtraParams = make(map[string]string, len(params))
	}
	for k, v := range params {
		if k == "" {
			continue
		}
		recognizer.ExtraParams[k] = v
	}
}
