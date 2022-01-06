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
	AppID = "AppID"
	// SecretID SecretID
	SecretID = "SecretID"
	// SecretKey SecretKey
	SecretKey = "SecretKey"
	// EngineModelType EngineModelType
	EngineModelType = "16k_zh"
	// SliceSize SliceSize
	SliceSize = 6400
)

// MySpeechRecognitionListener implementation of SpeechRecognitionListener
type MySpeechRecognitionListener struct {
	ID int
}

// OnRecognitionStart implementation of SpeechRecognitionListener
func (listener *MySpeechRecognitionListener) OnRecognitionStart(response *asr.SpeechRecognitionResponse) {
	fmt.Printf("%s|%s|OnRecognitionStart\n", time.Now().Format("2006-01-02 15:04:05"), response.VoiceID)
}

// OnSentenceBegin implementation of SpeechRecognitionListener
func (listener *MySpeechRecognitionListener) OnSentenceBegin(response *asr.SpeechRecognitionResponse) {
	fmt.Printf("%s|%s|OnSentenceBegin: %v\n", time.Now().Format("2006-01-02 15:04:05"), response.VoiceID, response)
}

// OnRecognitionResultChange implementation of SpeechRecognitionListener
func (listener *MySpeechRecognitionListener) OnRecognitionResultChange(response *asr.SpeechRecognitionResponse) {
	fmt.Printf("%s|%s|OnRecognitionResultChange: %v\n", time.Now().Format("2006-01-02 15:04:05"), response.VoiceID, response)
}

// OnSentenceEnd implementation of SpeechRecognitionListener
func (listener *MySpeechRecognitionListener) OnSentenceEnd(response *asr.SpeechRecognitionResponse) {
	fmt.Printf("%s|%s|OnSentenceEnd: %v\n", time.Now().Format("2006-01-02 15:04:05"), response.VoiceID, response)
}

// OnRecognitionComplete implementation of SpeechRecognitionListener
func (listener *MySpeechRecognitionListener) OnRecognitionComplete(response *asr.SpeechRecognitionResponse) {
	fmt.Printf("%s|%s|OnRecognitionComplete\n", time.Now().Format("2006-01-02 15:04:05"), response.VoiceID)
}

// OnFail implementation of SpeechRecognitionListener
func (listener *MySpeechRecognitionListener) OnFail(response *asr.SpeechRecognitionResponse, err error) {
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

	listener := &MySpeechRecognitionListener{
		ID: id,
	}
	credential := common.NewCredential(SecretID, SecretKey)
	recognizer := asr.NewSpeechRecognizer(AppID, credential, EngineModelType, listener)
	recognizer.ProxyURL = proxyURL
	recognizer.VoiceFormat = asr.AudioFormatPCM
	err = recognizer.Start()
	if err != nil {
		fmt.Printf("%s|recognizer start failed, error: %v\n", time.Now().Format("2006-01-02 15:04:05"), err)
		return err
	}
	data := make([]byte, SliceSize)
	for n, err := audio.Read(data); n > 0; n, err = audio.Read(data) {
		if err != nil {
			if err.Error() == "EOF" {
				break
			}
			fmt.Printf("read file error: %v\n", err)
			break
		}
		err = recognizer.Write(data)
		if err != nil {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	recognizer.Stop()
	return nil
}
