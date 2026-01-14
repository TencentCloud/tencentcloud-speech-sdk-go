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

// SpeechTranslateListener 用户必须实现此接口以获取翻译结果
type SpeechTranslateListener interface {
	OnTranslateStart(*SpeechTranslateResponse)
	OnSentenceBegin(*SpeechTranslateResponse)
	OnTranslateResultChange(*SpeechTranslateResponse)
	OnSentenceEnd(*SpeechTranslateResponse)
	OnTranslateComplete(*SpeechTranslateResponse)
	OnFail(*SpeechTranslateResponse, error)
}

// SpeechTranslateRequest 语音翻译请求参数
type SpeechTranslateRequest struct {
	AppID       uint64 `json:"appid"`
	SecretID    string `json:"secretid"`
	Timestamp   int64  `json:"timestamp"`
	Expired     int64  `json:"expired"`
	Nonce       int    `json:"nonce"`
	VoiceID     string `json:"voice_id"`
	VoiceFormat int    `json:"voice_format"`
	Source      string `json:"source"`
	Target      string `json:"target"`
	TransModel  string `json:"trans_model"`
	Region      string `json:"region"`
	Token       string `json:"token"`
	Signature   string `json:"signature"`
}

// SpeechTranslateResponse 语音翻译响应
type SpeechTranslateResponse struct {
	Code       int          `json:"code"`
	Message    string       `json:"message"`
	VoiceID    string       `json:"voice_id"`
	SentenceID string       `json:"sentence_id"`
	Result     SentenceInfo `json:"result"`
	Final      uint32       `json:"final"`
}

// SentenceInfo 句子翻译信息
type SentenceInfo struct {
	Source      string `json:"Source"`
	Target      string `json:"Target"`
	SourceText  string `json:"SourceText"`
	TargetText  string `json:"TargetText"`
	StartTime   uint32 `json:"StartTime"`
	EndTime     uint32 `json:"EndTime"`
	SentenceEnd bool   `json:"SentenceEnd"`
}

// SpeechTranslator 语音翻译器，是语音翻译服务的入口
type SpeechTranslator struct {
	// 基础配置
	AppID      string
	Credential *common.Credential

	// 翻译参数
	VoiceFormat int    // 音频格式：1-pcm, 8-mp3, 12-wav
	Source      string // 源语言
	Target      string // 目标语言
	TransModel  string // 翻译模型
	Region      string // 地域信息
	Token       string // 临时证书token

	// 监听器
	listener SpeechTranslateListener

	// 语音ID
	VoiceID string

	// 代理配置
	ProxyURL string

	// WebSocket连接
	conn *websocket.Conn

	// 数据通道
	dataChan chan []byte
	// 事件通道
	eventChan chan speechTranslateEvent

	// 用于停止所有goroutine
	sendEnd    chan int
	receiveEnd chan int
	eventEnd   chan int

	// 状态管理
	mutex   sync.Mutex
	started bool
}

const (
	defaultTranslateVoiceFormat = 1 // pcm格式

	translateProtocol = "wss"
	translateHost     = "asr.cloud.tencent.com"
	translatePath     = "/asr/speech_translate"
)

const (
	eventTypeTranslateStart         = 0
	eventTypeTranslateSentenceBegin = 1
	eventTypeTranslateResultChange  = 2
	eventTypeTranslateSentenceEnd   = 3
	eventTypeTranslateComplete      = 4
	eventTypeTranslateFail          = 5
)

type translateEventType int

type speechTranslateEvent struct {
	t   translateEventType
	r   *SpeechTranslateResponse
	err error
}

// NewSpeechTranslator 创建语音翻译器实例
func NewSpeechTranslator(appID string, credential *common.Credential, source, target, model string,
	listener SpeechTranslateListener) *SpeechTranslator {

	translator := &SpeechTranslator{
		AppID:       appID,
		Credential:  credential,
		Source:      source,
		Target:      target,
		TransModel:  model,
		VoiceFormat: defaultTranslateVoiceFormat,

		dataChan:  make(chan []byte, 6400),
		eventChan: make(chan speechTranslateEvent, 10),

		sendEnd:    make(chan int),
		receiveEnd: make(chan int),
		eventEnd:   make(chan int),

		listener: listener,
		started:  false,
	}
	return translator
}

// Start 连接服务器并开始翻译会话
func (translator *SpeechTranslator) Start() error {
	translator.mutex.Lock()
	defer translator.mutex.Unlock()

	if translator.started {
		return fmt.Errorf("translator is already started")
	}
	if translator.VoiceID == "" {
		voiceID := uuid.New().String()
		translator.VoiceID = voiceID
	}
	serverURL := translator.buildURL(translator.VoiceID)
	signature := translator.genSignature(serverURL)

	dialer := websocket.Dialer{}
	if len(translator.ProxyURL) > 0 {
		proxyURL, _ := url.Parse(translator.ProxyURL)
		dialer.Proxy = http.ProxyURL(proxyURL)
	}

	header := http.Header(make(map[string][]string))
	urlStr := fmt.Sprintf("%s://%s&signature=%s", translateProtocol, serverURL, url.QueryEscape(signature))
	conn, _, err := dialer.Dial(urlStr, header)
	if err != nil {
		return fmt.Errorf("voice_id: %s, error: %s", translator.VoiceID, err.Error())
	}
	_, data, err := conn.ReadMessage()
	if err != nil {
		conn.Close()
		return fmt.Errorf("voice_id: %s, error: %s", translator.VoiceID, err.Error())
	}
	msg := SpeechTranslateResponse{}
	err = json.Unmarshal(data, &msg)
	if err != nil {
		conn.Close()
		return fmt.Errorf("voice_id: %s, error: %s", translator.VoiceID, err.Error())
	}
	if msg.Code != 0 {
		conn.Close()
		return fmt.Errorf("voice_id: %s, code: %d, message: %s",
			translator.VoiceID, msg.Code, msg.Message)
	}

	translator.conn = conn
	go translator.send()
	go translator.receive()
	go translator.eventDispatch()
	translator.started = true

	translator.eventChan <- speechTranslateEvent{
		t: eventTypeTranslateStart,
		r: newSpeechTranslateResponse(0, "success", translator.VoiceID,
			fmt.Sprintf("%s-TranslateStart", translator.VoiceID), 0),
		err: nil,
	}
	return nil
}

// Write 写入音频数据到通道
func (translator *SpeechTranslator) Write(data []byte) error {
	translator.mutex.Lock()
	defer translator.mutex.Unlock()
	if !translator.started {
		return fmt.Errorf("translator not running")
	}

	translator.dataChan <- data
	return nil
}

// Stop 等待翻译过程完成
func (translator *SpeechTranslator) Stop() error {
	err := translator.stopInternal()
	if err != nil {
		return err
	}
	return nil
}

func (translator *SpeechTranslator) stopInternal() error {
	translator.mutex.Lock()
	defer translator.mutex.Unlock()
	if !translator.started {
		return fmt.Errorf("translator is not running")
	}
	close(translator.dataChan)
	<-translator.receiveEnd
	<-translator.sendEnd
	<-translator.eventEnd
	translator.started = false
	return nil
}

func (translator *SpeechTranslator) onError(code int, message string, err error) {
	if !translator.started {
		return
	}

	translator.listener.OnFail(newSpeechTranslateResponse(code, message, translator.VoiceID,
		fmt.Sprintf("%s-Error", translator.VoiceID), 0), err)

	go translator.stopInternal()
}

func (translator *SpeechTranslator) send() {
	defer func() {
		// 处理panic
		translator.genRecoverFunc()()
		close(translator.sendEnd)
	}()
	// 发送音频数据
	for data := range translator.dataChan {
		if err := translator.conn.WriteMessage(websocket.BinaryMessage, data); err != nil {
			translator.onError(-1, "send error", fmt.Errorf("voice_id: %s, error: %s",
				translator.VoiceID, err.Error()))
			return
		}
	}
	// 发送结束消息
	if err := translator.conn.WriteMessage(websocket.TextMessage, []byte("{\"end\":\"1\"}")); err != nil {
		translator.onError(-1, "send error", fmt.Errorf("voice_id: %s, error: %s",
			translator.VoiceID, err.Error()))
	}
}

func (translator *SpeechTranslator) eventDispatch() {
	defer func() {
		// 处理panic
		translator.genRecoverFunc()()
		close(translator.eventEnd)
	}()
	for e := range translator.eventChan {
		switch e.t {
		case eventTypeTranslateStart:
			translator.listener.OnTranslateStart(e.r)
		case eventTypeTranslateSentenceBegin:
			translator.listener.OnSentenceBegin(e.r)
		case eventTypeTranslateResultChange:
			translator.listener.OnTranslateResultChange(e.r)
		case eventTypeTranslateSentenceEnd:
			translator.listener.OnSentenceEnd(e.r)
		case eventTypeTranslateComplete:
			translator.listener.OnTranslateComplete(e.r)
		case eventTypeTranslateFail:
			translator.listener.OnFail(e.r, e.err)
		}
	}
}

func (translator *SpeechTranslator) receive() {
	defer func() {
		// 处理panic
		translator.genRecoverFunc()()
		close(translator.eventChan)
		close(translator.receiveEnd)
	}()
	lastSentenceID := ""
	for {
		_, data, err := translator.conn.ReadMessage()
		if err != nil {
			translator.onError(-1, "receive error", fmt.Errorf("voice_id: %s, error: %s", translator.VoiceID, err.Error()))
			break
		}

		msg := SpeechTranslateResponse{}
		err = json.Unmarshal(data, &msg)
		if err != nil {
			translator.onError(-1, "receive error",
				fmt.Errorf("voice_id: %s, error: %s",
					translator.VoiceID, err.Error()))
			break
		}
		if msg.Code != 0 {
			translator.onError(msg.Code, msg.Message,
				fmt.Errorf("VoiceID: %s, error code %d, message: %s",
					translator.VoiceID, msg.Code, msg.Message))
			break
		}

		// 如果是最终结果
		if msg.Final == 1 {
			translator.eventChan <- speechTranslateEvent{
				t:   eventTypeTranslateComplete,
				r:   &msg,
				err: nil,
			}
			break
		}

		// 判断句子开始和结束
		beginOrEnd := false
		if msg.SentenceID != lastSentenceID && msg.SentenceID != "" {
			lastSentenceID = msg.SentenceID
			translator.eventChan <- speechTranslateEvent{
				t:   eventTypeTranslateSentenceBegin,
				r:   &msg,
				err: nil,
			}
			beginOrEnd = true
		}
		if msg.Result.SentenceEnd {
			translator.eventChan <- speechTranslateEvent{
				t:   eventTypeTranslateSentenceEnd,
				r:   &msg,
				err: nil,
			}
			beginOrEnd = true
		}
		if !beginOrEnd {
			translator.eventChan <- speechTranslateEvent{
				t:   eventTypeTranslateResultChange,
				r:   &msg,
				err: nil,
			}
		}
	}
}

func (translator *SpeechTranslator) buildURL(voiceID string) string {
	var queryMap = make(map[string]string)
	queryMap["secretid"] = translator.Credential.SecretId
	var timestamp = time.Now().Unix()
	var timestampStr = strconv.FormatInt(timestamp, 10)
	queryMap["timestamp"] = timestampStr
	queryMap["expired"] = strconv.FormatInt(timestamp+24*60*60, 10)
	queryMap["nonce"] = timestampStr

	// 翻译参数
	queryMap["voice_id"] = voiceID
	queryMap["voice_format"] = strconv.FormatInt(int64(translator.VoiceFormat), 10)
	queryMap["source"] = translator.Source
	queryMap["target"] = translator.Target
	queryMap["trans_model"] = translator.TransModel
	if translator.Region != "" {
		queryMap["region"] = translator.Region
	}
	if translator.Token != "" {
		queryMap["token"] = translator.Token
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

	// 生成URL
	url := fmt.Sprintf("%s%s/%s?%s", translateHost, translatePath, translator.AppID, queryStr)
	return url
}

func (translator *SpeechTranslator) genSignature(url string) string {
	hmac := hmac.New(sha1.New, []byte(translator.Credential.SecretKey))
	signURL := url
	hmac.Write([]byte(signURL))
	encryptedStr := hmac.Sum([]byte(nil))
	var signature = base64.StdEncoding.EncodeToString(encryptedStr)

	return signature
}

func newSpeechTranslateResponse(code int, message string, voiceID string,
	sentenceID string, final uint32) *SpeechTranslateResponse {
	return &SpeechTranslateResponse{
		Code:       code,
		Message:    message,
		VoiceID:    voiceID,
		SentenceID: sentenceID,
		Final:      final,
	}
}

func (translator *SpeechTranslator) genRecoverFunc() func() {
	return func() {
		if r := recover(); r != nil {
			var err error
			switch r := r.(type) {
			case error:
				err = r
			default:
				err = fmt.Errorf("%v", r)
			}
			retErr := fmt.Errorf("panic error occurred! [err: %s] [stack: %s]",
				err.Error(), string(debug.Stack()))
			translator.eventChan <- speechTranslateEvent{
				t: eventTypeTranslateFail,
				r: newSpeechTranslateResponse(-1, "panic error", translator.VoiceID,
					fmt.Sprintf("%s-Error", translator.VoiceID), 0),
				err: retErr,
			}
		}
	}
}
