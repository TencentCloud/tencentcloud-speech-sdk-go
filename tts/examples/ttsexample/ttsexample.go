package main

import (
	"flag"
	"fmt"
	"sync"
	"time"

	"github.com/tencentcloud/tencentcloud-speech-sdk-go/common"
	"github.com/tencentcloud/tencentcloud-speech-sdk-go/tts"
)

var (
	// AppID AppID
	AppID = 0
	// SecretID SecretID
	SecretID = "SecretID"
	// SecretKey SecretKey
	SecretKey = "SecretKey"
)

// MySpeechSynthesisListener implementation of SpeechSynthesisListener
type MySpeechSynthesisListener struct {
	ID int
}

// OnMessage implementation of SpeechSynthesisListener
func (listener *MySpeechSynthesisListener) OnMessage(response *tts.SpeechSynthesisResponse) {
	fmt.Printf("%s|%d|OnMessage, size: %d\n", time.Now().Format("2006-01-02 15:04:05"), listener.ID, len(response.Data))
}

// OnComplete implementation of SpeechSynthesisListener
func (listener *MySpeechSynthesisListener) OnComplete(response *tts.SpeechSynthesisResponse) {
	fmt.Printf("%s|%d|OnComplete: %v\n", time.Now().Format("2006-01-02 15:04:05"), listener.ID, response)
}

// OnCancel implementation of SpeechSynthesisListener
func (listener *MySpeechSynthesisListener) OnCancel(response *tts.SpeechSynthesisResponse) {
	fmt.Printf("%s|%d|OnCancel: %v\n", time.Now().Format("2006-01-02 15:04:05"), listener.ID, response)
}

// OnFail implementation of SpeechSynthesisListener
func (listener *MySpeechSynthesisListener) OnFail(response *tts.SpeechSynthesisResponse, err error) {
	fmt.Printf("%s|%d|OnFail: %v, %v\n", time.Now().Format("2006-01-02 15:04:05"), listener.ID, response, err)
}

var proxyURL string

func main() {
	var c = flag.Int("c", 1, "concurrency")
	var p = flag.String("p", "", "proxy url")
	flag.Parse()

	proxyURL = *p
	var wg sync.WaitGroup
	for i := 0; i < *c; i++ {
		fmt.Println("Main: Starting worker", i)
		wg.Add(1)
		go process(i, &wg)
	}

	fmt.Println("Main: Waiting for workers to finish")
	wg.Wait()
	fmt.Println("Main: Completed")

}

func process(id int, wg *sync.WaitGroup) {
	defer wg.Done()

	listener := &MySpeechSynthesisListener{
		ID: id,
	}
	credential := common.NewCredential(SecretID, SecretKey)
	synthesizer := tts.NewSpeechSynthesizer(int64(AppID), credential, listener)
	synthesizer.VoiceType = 101000
	text := "语音合成可自定义音量和语速，让发音更自然、更专业、更符合场景需求。满足将文本转化成拟人化语音的需求，打通人机交互闭环。支持多种音色选择，语音合成可广泛应用于语音导航、有声读物、机器人、语音助手、自动新闻播报等场景，提升人机交互体验，提高语音类应用构建效率。"
	synthesizer.ProxyURL = proxyURL
	synthesizer.Synthesis(text)
	synthesizer.Wait()
}
