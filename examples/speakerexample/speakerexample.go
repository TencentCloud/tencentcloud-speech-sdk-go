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
	// EngineModelType EngineModelType
	EngineModelType = ""
	// SliceSize SliceSize
	SliceSize = 6400
)

// MySpeakerRecognitionListener implementation of SpeakerRecognitionListener
type MySpeakerRecognitionListener struct {
	ID int
}

// OnRecognitionStart implementation of SpeakerRecognitionListener
func (listener *MySpeakerRecognitionListener) OnRecognitionStart(response *asr.SpeakerRecognitionResponse) {
	fmt.Printf("%s|%s|OnRecognitionStart speaker_context_id=%s\n",
		time.Now().Format("2006-01-02 15:04:05"), response.VoiceID, response.SpeakerContextId)
}

// OnRecognitionSentences implementation of SpeakerRecognitionListener
func (listener *MySpeakerRecognitionListener) OnRecognitionSentences(response *asr.SpeakerRecognitionResponse) {
	if response.Sentences == nil {
		return
	}
	for i, s := range response.Sentences.SentenceList {
		fmt.Printf("%s|%s|OnRecognitionSentences [%d] sentence_id=%d speaker=%d type=%d text=%s\n",
			time.Now().Format("2006-01-02 15:04:05"), response.VoiceID,
			i,s.SentenceId, s.SpeakerId, s.SentenceType, s.Sentence)
	}
}

// OnSentenceEnd implementation of SpeakerRecognitionListener
func (listener *MySpeakerRecognitionListener) OnSentenceEnd(response *asr.SpeakerRecognitionResponse) {
	fmt.Printf("%s|%s|OnSentenceEnd code=%d message=%s\n",
		time.Now().Format("2006-01-02 15:04:05"), response.VoiceID, response.Code, response.Message)
}

// OnFail implementation of SpeakerRecognitionListener
func (listener *MySpeakerRecognitionListener) OnFail(response *asr.SpeakerRecognitionResponse, err error) {
	fmt.Printf("%s|%s|OnFail code=%d message=%s error=%v\n",
		time.Now().Format("2006-01-02 15:04:05"), response.VoiceID, response.Code, response.Message, err)
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

	listener := &MySpeakerRecognitionListener{
		ID: id,
	}
	credential := common.NewCredential(SecretID, SecretKey)
	recognizer := asr.NewSpeakerRecognizer(AppID, credential, EngineModelType, listener)
	recognizer.ProxyURL = proxyURL
	recognizer.VoiceFormat = asr.SpeakerAudioFormatPCM
	recognizer.NeedVad = 1
	recognizer.VadSilenceTime = 1000
	recognizer.SpeakerDiarization = 1
	recognizer.EnableSpeakerContext = 1
	// 首次不设 SpeakerContextId，Start() 后从 OnRecognitionStart 回调拿到
	// 后续可设置：recognizer.SpeakerContextId = "之前拿到的id"

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
		err = recognizer.Write(data[:n])
		if err != nil {
			break
		}
		//模拟真实场景，200ms产生200ms数据
		time.Sleep(200 * time.Millisecond)
	}
	recognizer.Stop()
	return nil
}
