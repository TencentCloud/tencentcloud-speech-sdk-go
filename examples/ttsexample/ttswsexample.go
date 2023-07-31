package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"github.com/google/uuid"
	"github.com/tencentcloud/tencentcloud-speech-sdk-go/common"
	"github.com/tencentcloud/tencentcloud-speech-sdk-go/tts"
	"io/ioutil"
	"log"
	"path"
	"strconv"
	"sync"
	"time"
)

// MainConfig 密钥配置
type MainConfig struct {
	SecretId  string `json:"SecretId"`
	SecretKey string `json:"SecretKey"`
	AppId     int    `json:"AppId"`
}

// getMainConf 获取配置信息
func getMainConf(path string) *MainConfig {
	buf, err := ioutil.ReadFile(path)
	if err != nil {
		log.Panicln("load config conf failed: ", err)
	}
	mainConfig := &MainConfig{}
	json.Unmarshal(buf, mainConfig)
	return mainConfig
}

type MySpeechWsSynthesisListener struct {
	SessionId string
	Data      []byte
	Index     int
}

func (l *MySpeechWsSynthesisListener) OnSynthesisStart(r *tts.SpeechWsSynthesisResponse) {
	fmt.Printf("%s|OnSynthesisStart,sessionId:%s response: %s\n", time.Now().Format("2006-01-02 15:04:05"), l.SessionId, r.ToString())
}

func (l *MySpeechWsSynthesisListener) OnSynthesisEnd(r *tts.SpeechWsSynthesisResponse) {
	fileName := fmt.Sprintf("test.mp3")
	tts.WriteFile(path.Join("./", fileName), l.Data)
	fmt.Printf("%s|OnSynthesisEnd,sessionId:%s response: %s\n", time.Now().Format("2006-01-02 15:04:05"), l.SessionId, r.ToString())
}
func (l *MySpeechWsSynthesisListener) OnAudioResult(data []byte) {
	fmt.Printf("%s|OnAudioResult,sessionId:%s index:%d\n", time.Now().Format("2006-01-02 15:04:05"), l.SessionId, l.Index)
	l.Index = l.Index + 1
	l.Data = append(l.Data, data...)
}
func (l *MySpeechWsSynthesisListener) OnTextResult(r *tts.SpeechWsSynthesisResponse) {
	fmt.Printf("%s|OnTextResult,sessionId:%s response: %s\n", time.Now().Format("2006-01-02 15:04:05"), l.SessionId, r.ToString())
}
func (l *MySpeechWsSynthesisListener) OnSynthesisFail(r *tts.SpeechWsSynthesisResponse, err error) {
	fmt.Printf("%s|OnSynthesisFail,sessionId:%s response: %s err:%s\n", time.Now().Format("2006-01-02 15:04:05"), l.SessionId, r.ToString(), err.Error())
}

func main() {
	var c = flag.Int("c", 1, "concurrency")
	flag.Parse()
	var wg sync.WaitGroup
	for i := 0; i < *c; i++ {
		fmt.Println("Main: Starting worker", i)
		wg.Add(1)
		go processWs(i, &wg)
	}

	fmt.Println("Main: Waiting for workers to finish")
	wg.Wait()
	fmt.Println("Main: Completed")

}

func processWs(id int, wg *sync.WaitGroup) {
	defer wg.Done()
	mainConfig := getMainConf("../../config.json") //配置文件读取密钥避免泄漏
	sessionId := fmt.Sprintf("%s_%s", strconv.Itoa(id), uuid.New().String())
	listener := &MySpeechWsSynthesisListener{Data: make([]byte, 0), SessionId: sessionId}
	credential := common.NewCredential(mainConfig.SecretId, mainConfig.SecretKey)
	synthesizer := tts.NewSpeechWsSynthesizer(int64(mainConfig.AppId), credential, listener)
	synthesizer.SessionId = sessionId
	synthesizer.VoiceType = 1001
	synthesizer.Codec = "mp3"
	synthesizer.Text = "<speak>\n现状是各地的经济水平是<phoneme alphabet=\"py\" ph=\"cen1 ci1 bu4 qi2\">参差不齐</phoneme>的。需要缩小较弱地域和较强地域的<phoneme alphabet=\"py\" ph=\"cha1 ju4\">差距</phoneme>。要做好这个<phoneme alphabet=\"py\" ph=\"chai1 shi4\">差事</phoneme>可不容易啊。\n</speak>\n"
	synthesizer.EnableSubtitle = true
	//synthesizer.EmotionCategory = "happy"
	//synthesizer.EmotionIntensity = 200
	//synthesizer.Debug = true
	//synthesizer.DebugFunc = func(message string) { fmt.Println(message) }
	err := synthesizer.Synthesis()
	if err != nil {
		fmt.Println(err.Error())
		return
	}
	synthesizer.Wait()
}
