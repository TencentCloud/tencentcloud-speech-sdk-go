package main

import (
	"flag"
	"fmt"
	"github.com/tencentcloud/tencentcloud-speech-sdk-go/asr"
	"github.com/tencentcloud/tencentcloud-speech-sdk-go/common"
	"os"
	"sync"
	"time"
)

var (
	//TODO 补充信息
	// AppID AppID
	//AppID = ""
	//// SecretID SecretID
	//SecretID = ""
	//// SecretKey SecretKey
	//SecretKey = ""

	// AppID AppID
	AppID = "1256237915"
	// SecretID SecretID
	SecretID = "AKIDjTG5peZN6Xo1CC2nPo4DG9pfP3eO4c3M"
	// SecretKey SecretKey
	SecretKey = "ITenocRAmDUb3V0Vs7JOMDu6Zs0JXaPz"

	// SliceSize SliceSize
	SliceSize = 3200
)

// MyVNRecognitionListener implementation of SpeechRecognitionListener
type MyVNRecognitionListener struct {
	ID int
}

// OnVNRecognitionStart implementation of SpeechRecognitionListener
func (listener *MyVNRecognitionListener) OnVNRecognitionStart(response *asr.VNRecognitionResponse) {
	fmt.Printf("%s|%s|OnRecognitionStart\n", time.Now().Format("2006-01-02 15:04:05"), response.VoiceID)
}

// OnVNRecognitionComplete implementation of SpeechRecognitionListener
func (listener *MyVNRecognitionListener) OnVNRecognitionComplete(response *asr.VNRecognitionResponse) {
	fmt.Printf("%s|%s|OnRecognitionComplete｜result:%d\n", time.Now().Format("2006-01-02 15:04:05"), response.VoiceID, response.Result)
}

// OnVNFail implementation of SpeechRecognitionListener
func (listener *MyVNRecognitionListener) OnVNFail(response *asr.VNRecognitionResponse, err error) {
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

	listener := &MyVNRecognitionListener{
		ID: id,
	}
	credential := common.NewCredential(SecretID, SecretKey)
	recognizer := asr.NewVNRecognizer(AppID, credential, listener)
	recognizer.ProxyURL = proxyURL
	recognizer.VoiceFormat = asr.AudioFormatPCM
	recognizer.WaitTime = 30000
	//握手阶段
	err = recognizer.Start()
	if err != nil {
		fmt.Printf("%s|recognizer start failed, error: %v\n", time.Now().Format("2006-01-02 15:04:05"), err)
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
		err, end := recognizer.Write(data)
		if err != nil || end {
			break
		}
		//模拟真实场景，200ms产生200ms数据
		time.Sleep(200 * time.Millisecond)
	}
	recognizer.Stop()
	return nil
}
