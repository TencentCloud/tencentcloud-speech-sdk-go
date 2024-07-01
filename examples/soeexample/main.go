package main

import (
	"flag"
	"fmt"
	"github.com/tencentcloud/tencentcloud-speech-sdk-go/common"
	"github.com/tencentcloud/tencentcloud-speech-sdk-go/soe"
	"os"
	"sync"
	"time"
)

var (
	//TODO 补充信息
	// AppID AppID
	AppID = ""
	//SecretID SecretID
	SecretID = ""
	//SecretKey SecretKey
	SecretKey = ""
	// Token 只有临时秘钥鉴权需要
	Token = ""

	// SliceSize SliceSize
	SliceSize = 1600
)

// MySpeakingAssessmentListener implementation of SpeakingAssessmentListener
type MySpeakingAssessmentListener struct {
	ID int
}

// OnRecognitionStart implementation of SpeakingAssessmentListener
func (listener *MySpeakingAssessmentListener) OnRecognitionStart(response *soe.SpeakingAssessmentResponse) {
	fmt.Printf("%s|%s|OnRecognitionStart\n", time.Now().Format("2006-01-02 15:04:05"), response.VoiceID)
}

// OnIntermediateResults implementation of SpeakingAssessmentListener
func (listener *MySpeakingAssessmentListener) OnIntermediateResults(response *soe.SpeakingAssessmentResponse) {
	fmt.Printf("%s|%s|OnIntermediateResults｜result:%+v\n", time.Now().Format("2006-01-02 15:04:05"), response.VoiceID, response.Result)
}

// OnRecognitionComplete implementation of SpeakingAssessmentListener
func (listener *MySpeakingAssessmentListener) OnRecognitionComplete(response *soe.SpeakingAssessmentResponse) {
	fmt.Printf("%s|%s|OnRecognitionComplete｜result:%+v\n", time.Now().Format("2006-01-02 15:04:05"), response.VoiceID, response.Result)
}

// OnFail implementation of SpeakingAssessmentListener
func (listener *MySpeakingAssessmentListener) OnFail(response *soe.SpeakingAssessmentResponse, err error) {
	fmt.Printf("%s|%s|OnFail: %v\n", time.Now().Format("2006-01-02 15:04:05"), response.VoiceID, err)
}

var proxyURL string

func main() {
	var c = flag.Int("c", 1, "concurrency")
	var l = flag.Bool("l", false, "loop or not")
	var f = flag.String("f", "english.wav", "audio file")
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

	listener := &MySpeakingAssessmentListener{
		ID: id,
	}
	// 临时秘钥鉴权需要使用带token的方式 credential := common.NewTokenCredential(SecretID, SecretKey, Token)
	credential := common.NewCredential(SecretID, SecretKey)
	recognizer := soe.NewSpeechRecognizer(AppID, credential, listener)
	recognizer.ProxyURL = proxyURL
	recognizer.VoiceFormat = soe.AudioFormatWav
	recognizer.RefText = "beautiful"
	recognizer.ServerEngineType = "16k_en"
	recognizer.ScoreCoeff = 1.1
	recognizer.EvalMode = 0
	recognizer.Keyword = ""
	recognizer.SentenceInfoEnabled = 0
	recognizer.TextMode = 0
	//握手阶段
	err = recognizer.Start()
	if err != nil {
		fmt.Printf("%s|recognizer start failed, error: %v\n", time.Now().Format("2006-01-02 15:04:05"), err)
		return err
	}
	seq := 0
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
		err = recognizer.Write(data)
		if err != nil {
			break
		}
		//模拟真实场景，200ms产生200ms数据
		time.Sleep(200 * time.Millisecond)
		seq++
	}
	recognizer.Stop()
	return nil
}
