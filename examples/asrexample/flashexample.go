package main

import (
	"flag"
	"fmt"
	"io/ioutil"
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
	// EngineType EngineType
	EngineType = "16k_zh"
)

func main() {
	var c = flag.Int("c", 1, "concurrency")
	var l = flag.Bool("l", false, "loop or not")
	var f = flag.String("f", "test.pcm", "audio file")
	flag.Parse()

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
		process(id, file)
	}
}

func processOnce(id int, wg *sync.WaitGroup, file string) {
	defer wg.Done()
	process(id, file)
}

func process(id int, file string) {
	audio, err := os.Open(file)
	defer audio.Close()
	if err != nil {
		fmt.Printf("open file error: %v\n", err)
		return
	}
	credential := common.NewCredential(SecretID, SecretKey)
	recognizer := asr.NewFlashRecognizer(AppID, credential)
	data, err := ioutil.ReadAll(audio)
	if err != nil {
		fmt.Printf("%s|failed read data, error: %v\n", time.Now().Format("2006-01-02 15:04:05"), err)
		return
	}

	req := new(asr.FlashRecognitionRequest)
	req.EngineType = EngineType
	req.VoiceFormat = "pcm"
	req.SpeakerDiarization = 0
	req.FilterDirty = 0
	req.FilterModal = 0
	req.FilterPunc = 0
	req.ConvertNumMode = 1
	req.FirstChannelOnly = 1
	req.WordInfo = 0

	resp, err := recognizer.DoRecognize(req, data)
	if err != nil {
		fmt.Printf("%s|failed do recognize, error: %v\n", time.Now().Format("2006-01-02 15:04:05"), err)
		return
	}
	fmt.Printf("request_id: %s\n", resp.RequestId)

	for _, channelResult := range resp.FlashResult {
		fmt.Printf("channel_id: %d, result: %s\n", channelResult.ChannelId, channelResult.Text)
	}
}
