package main

import (
	"flag"
	"fmt"
	"os"
	"sync"
	"time"

	"github.com/tencentcloud/tencentcloud-speech-sdk-go/asr"
	"github.com/tencentcloud/tencentcloud-speech-sdk-go/common"
)

var (
	// AppID AppID
	AppID = ""
	// SecretID SecretID
	SecretID = ""
	// SecretKey SecretKey
	SecretKey = ""
	// Source 源语言
	Source = "zh"
	// Target 目标语言
	Target = "en"
	// TransModel 翻译模型
	TransModel = "hunyuan-translation-lite"
	// SliceSize 每次发送的音频数据大小
	SliceSize = 6400
)

// MySpeechTranslateListener 语音翻译监听器实现
type MySpeechTranslateListener struct {
	ID int
}

// OnTranslateStart 翻译开始回调
func (listener *MySpeechTranslateListener) OnTranslateStart(response *asr.SpeechTranslateResponse) {
	fmt.Printf("%s|%s|OnTransla\n", time.Now().Format("2006-01-02 15:04:05"), response.VoiceID)
}

// OnSentenceBegin 句子开始回调
func (listener *MySpeechTranslateListener) OnSentenceBegin(response *asr.SpeechTranslateResponse) {
	fmt.Printf("%s|%s|%s|OnSentenceBegin: %v\n", time.Now().Format("2006-01-02 15:04:05"), response.VoiceID, response.SentenceID, response)
}

// OnTranslateResultChange 翻译结果变化回调
func (listener *MySpeechTranslateListener) OnTranslateResultChange(response *asr.SpeechTranslateResponse) {
	fmt.Printf("%s|%s|%s|OnTranslateResultChange: %v\n", time.Now().Format("2006-01-02 15:04:05"), response.VoiceID, response.SentenceID, response)
}

// OnSentenceEnd 句子结束回调
func (listener *MySpeechTranslateListener) OnSentenceEnd(response *asr.SpeechTranslateResponse) {
	fmt.Printf("%s|%s|%s|OnSentenceEnd: %v\n", time.Now().Format("2006-01-02 15:04:05"), response.VoiceID, response.SentenceID, response)
}

// OnTranslateComplete 翻译完成回调
func (listener *MySpeechTranslateListener) OnTranslateComplete(response *asr.SpeechTranslateResponse) {
	fmt.Printf("%s|%s|OnTranslateComplete\n", time.Now().Format("2006-01-02 15:04:05"), response.VoiceID)
}

// OnFail 翻译失败回调
func (listener *MySpeechTranslateListener) OnFail(response *asr.SpeechTranslateResponse, err error) {
	fmt.Printf("%s|%s|OnFail: %v\n", time.Now().Format("2006-01-02 15:04:05"), response.VoiceID, err)
}

var proxyURL string

func main() {
	var c = flag.Int("c", 1, "concurrency")
	var l = flag.Bool("l", false, "loop or not")
	var f = flag.String("f", "test.pcm", "audio file")
	var p = flag.String("p", "", "proxy url")

	flag.Parse()

	proxyURL = *p
	var wg sync.WaitGroup
	for i := 0; i < *c; i++ {
		fmt.Println("Main: Starting worker", i)
		wg.Add(1)
		if *l {
			go processLoop(i, &wg, *f)
		} else {
			go processOnce(i, &wg, *f)
		}
	}

	fmt.Println("Main: Waiting for workers to finish")
	wg.Wait()
	fmt.Println("Main: Completed")
}

func processLoop(id int, wg *sync.WaitGroup, file string) {
	defer wg.Done()
	for {
		err := process(id, file)
		if err != nil {
			return
		}
	}
}

func processOnce(id int, wg *sync.WaitGroup, file string) {
	defer wg.Done()
	process(id, file)
}

func process(id int, file string) error {
	audio, err := os.Open(file)
	defer audio.Close()
	if err != nil {
		fmt.Printf("open file error: %v\n", err)
		return err
	}

	listener := &MySpeechTranslateListener{
		ID: id,
	}
	credential := common.NewCredential(SecretID, SecretKey)
	translator := asr.NewSpeechTranslator(AppID, credential, Source, Target, TransModel, listener)
	translator.ProxyURL = proxyURL
	translator.VoiceFormat = asr.AudioFormatPCM

	err = translator.Start()
	if err != nil {
		fmt.Printf("%s|translator start failed, error: %v\n", time.Now().Format("2006-01-02 15:04:05"), err)
		return err
	}

	for {
		data := make([]byte, SliceSize)
		n, err := audio.Read(data)
		if err != nil {
			if err.Error() == "EOF" {
				break
			}
			fmt.Printf("read file error: %v\n", err)
			break
		}
		if n <= 0 {
			break
		}
		err = translator.Write(data[:n])
		if err != nil {
			break
		}
		time.Sleep(200 * time.Millisecond)
	}

	translator.Stop()
	return nil
}
