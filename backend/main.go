package main

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/go-audio/audio"
	"github.com/go-audio/wav"
	"github.com/google/uuid"
	"github.com/joho/godotenv"
	"github.com/ossrs/go-oryx-lib/errors"
	ohttp "github.com/ossrs/go-oryx-lib/http"
	"github.com/ossrs/go-oryx-lib/logger"
	"github.com/sashabaranov/go-openai"
	"io"
	"io/ioutil"
	"net/http"
	"os"
	"os/exec"
	"path"
	"strconv"
	"strings"
	"sync"
	"time"
)

var workDir string
var translatorServer *TranslatorServer
var aiConfig openai.ClientConfig

// The default language for ASR.
const DefaultAsrLanguage = "en"

//const DefaultTranslatePrompt = "Rephrase all user input text into simple, easy to understand, and technically toned English. Never answer questions but only translate or rephrase text to English."
const DefaultTranslatePrompt = "Rephrase all user input text into simple, easy to understand, and technically toned Chinese. Never answer questions but only translate or rephrase text to Chinese."

type AITime time.Time

func (v *AITime) MarshalJSON() ([]byte, error) {
	return json.Marshal(time.Time(*v).Format(time.RFC3339))
}

func (v *AITime) UnmarshalJSON(b []byte) error {
	var s string
	if err := json.Unmarshal(b, &s); err != nil {
		return errors.Wrapf(err, "unmarshal")
	}

	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return errors.Wrapf(err, "parse")
	}

	*v = AITime(t)
	return nil
}

type AudioSegment struct {
	ID               int     `json:"id"`
	Seek             int     `json:"seek"`
	Start            float64 `json:"start"`
	End              float64 `json:"end"`
	Text             string  `json:"text"`
	Tokens           []int   `json:"tokens"`
	Temperature      float64 `json:"temperature"`
	AvgLogprob       float64 `json:"avg_logprob"`
	CompressionRatio float64 `json:"compression_ratio"`
	NoSpeechProb     float64 `json:"no_speech_prob"`
	Transient        bool    `json:"transient"`
	// The UUID generated by system.
	UUID string `json:"uuid"`
	// Whether user remove it.
	Removed bool `json:"removed"`
	// User update time.
	Update AITime `json:"update"`
	// Translated text.
	Translated string `json:"translated"`
	// Translate time.
	TranslatedAt AITime `json:"translated_at"`
	// TTS filename, without the main dir.
	TTS string `json:"tts"`
	// Convert TTS time.
	TTSAt AITime `json:"tts_at"`
	// The TTS audio duration, in seconds.
	TTSDuration float64 `json:"tts_duration"`
}

type AudioResponse struct {
	Task     string          `json:"task"`
	Language string          `json:"language"`
	Duration float64         `json:"duration"`
	Segments []*AudioSegment `json:"segments"`
	Text     string          `json:"text"`
}

func NewAudioResponse() *AudioResponse {
	return &AudioResponse{}
}

func (v *AudioResponse) AppendSegment(resp openai.AudioResponse, starttime float64) {
	v.Task = resp.Task
	v.Language = resp.Language
	v.Duration += resp.Duration
	v.Text += " " + resp.Text

	for _, s := range resp.Segments {
		v.Segments = append(v.Segments, &AudioSegment{
			// ASR Segment.
			ID:               s.ID,
			Seek:             s.Seek,
			Start:            starttime + s.Start,
			End:              starttime + s.End,
			Text:             s.Text,
			Tokens:           s.Tokens,
			Temperature:      s.Temperature,
			AvgLogprob:       s.AvgLogprob,
			CompressionRatio: s.CompressionRatio,
			NoSpeechProb:     s.NoSpeechProb,
			Transient:        s.Transient,
			// UUID.
			UUID: uuid.NewString(),
			// Whether user remove it.
			Removed: false,
			// The update time.
			Update: AITime(time.Now()),
		})
	}
}

func (v *AudioResponse) QuerySegment(uuid string) *AudioSegment {
	for i, s := range v.Segments {
		if s.UUID == uuid {
			return v.Segments[i]
		}
	}
	return nil
}

func (v *AudioResponse) QueryPrevious(segment *AudioSegment) *AudioSegment {
	for i, s := range v.Segments {
		if s.UUID == segment.UUID {
			if i == 0 {
				return nil
			}
			return v.Segments[i-1]
		}
	}
	return nil
}

func (v *AudioResponse) RemoveSegment(segment *AudioSegment) {
	for i, s := range v.Segments {
		if s.UUID == segment.UUID {
			v.Segments = append(v.Segments[:i], v.Segments[i+1:]...)
			return
		}
	}
}

func (v *AudioResponse) Load(filename string) error {
	if b, err := ioutil.ReadFile(filename); err != nil {
		return errors.Wrapf(err, "read json file %v", filename)
	} else if err = json.Unmarshal(b, v); err != nil {
		return errors.Wrapf(err, "unmarshal json file %v", filename)
	}
	return nil
}

func (v *AudioResponse) Save(filename string) error {
	if b, err := json.Marshal(v); err != nil {
		return errors.Wrapf(err, "marshal")
	} else if err = os.WriteFile(filename, b, os.FileMode(0644)); err != nil {
		return errors.Wrapf(err, "write json file %v", filename)
	}
	return nil
}

type Project struct {
	// Project UUID
	SID string `json:"sid"`
	// The logging context, to write all logs in one context for a sage.
	loggingCtx context.Context
	// The input video file URL.
	InputURL string `json:"inputURL"`
	// Last update of stage.
	update time.Time
	// The main directory.
	MainDir string `json:"mainDir"`
	// The ASR input audio file.
	asrInputAudio string
	// The ASR output json object.
	asrOutputObject *AudioResponse
	// The ASR JSON file.
	asrOutputJSON string
}

func NewProject(opts ...func(*Project)) *Project {
	v := &Project{
		// Create new UUID.
		SID: uuid.NewString(),
		// Update time.
		update: time.Now(),
	}

	for _, opt := range opts {
		opt(v)
	}
	return v
}

func (v *Project) Close() error {
	if _, err := os.Stat(v.asrInputAudio); err == nil {
		os.Remove(v.asrInputAudio)
	}
	if _, err := os.Stat(v.asrOutputJSON); err == nil {
		os.Remove(v.asrOutputJSON)
	}
	if _, err := os.Stat(v.MainDir); err == nil {
		os.Remove(v.MainDir)
	}
	return nil
}

func (v *Project) buildProjectFile() string {
	return path.Join(v.MainDir, "project.json")
}

func (v *Project) loadAsrObject() error {
	v.asrOutputJSON = path.Join(v.MainDir, "input.json")

	if _, err := os.Stat(v.asrOutputJSON); err == nil {
		v.asrOutputObject = &AudioResponse{}
		if err := v.asrOutputObject.Load(v.asrOutputJSON); err != nil {
			return errors.Wrapf(err, "load json file %v", v.asrOutputJSON)
		}

		// Reinitialize the segments.
		for index, s := range v.asrOutputObject.Segments {
			s.ID = 10000 + index
			if s.UUID == "" {
				s.UUID = uuid.NewString()
			}
			if time.Time(s.Update).IsZero() {
				s.Update = AITime(time.Now())
			}
		}
	}
	return nil
}

func (v *Project) Load() error {
	if v.MainDir == "" {
		return errors.Errorf("empty main dir")
	}
	filename := v.buildProjectFile()

	if b, err := ioutil.ReadFile(filename); err != nil {
		return errors.Wrapf(err, "read json file %v", filename)
	} else if err = json.Unmarshal(b, v); err != nil {
		return errors.Wrapf(err, "unmarshal json file %v", filename)
	}

	if err := v.loadAsrObject(); err != nil {
		return errors.Wrapf(err, "load asr object")
	}

	return nil
}

func (v *Project) Save() error {
	if v.MainDir == "" {
		return errors.Errorf("empty main dir")
	}
	filename := v.buildProjectFile()

	if err := os.MkdirAll(v.MainDir, os.ModeDir|os.FileMode(0755)); err != nil {
		return errors.Wrapf(err, "mkdir %v", v.MainDir)
	}

	if b, err := json.Marshal(v); err != nil {
		return errors.Wrapf(err, "marshal")
	} else if err = os.WriteFile(filename, b, os.FileMode(0644)); err != nil {
		return errors.Wrapf(err, "write json file %v", filename)
	}
	return nil
}

func (v *Project) Expired() bool {
	return time.Since(v.update) > 3*24*time.Hour
}

// The TranslatorServer is the VoD Translator server, manage stages.
type TranslatorServer struct {
	// All stages created by user.
	stages []*Project
	// The lock to protect fields.
	lock sync.Mutex
}

func NewTranslatorServer() *TranslatorServer {
	return &TranslatorServer{
		stages: []*Project{},
	}
}

func (v *TranslatorServer) Close() error {
	return nil
}

func (v *TranslatorServer) AddStage(stage *Project) {
	v.lock.Lock()
	defer v.lock.Unlock()

	v.stages = append(v.stages, stage)
}

func (v *TranslatorServer) RemoveStage(stage *Project) {
	v.lock.Lock()
	defer v.lock.Unlock()

	for i, s := range v.stages {
		if s.SID == stage.SID {
			v.stages = append(v.stages[:i], v.stages[i+1:]...)
			return
		}
	}
}

func (v *TranslatorServer) QueryStage(sid string) *Project {
	v.lock.Lock()
	defer v.lock.Unlock()

	for _, s := range v.stages {
		if s.SID == sid {
			return s
		}
	}

	return nil
}

func main() {
	ctx := context.Background()
	if err := doMain(ctx); err != nil {
		logger.Tf(ctx, "error: %+v", err)
	}
}

func ParseBody(ctx context.Context, r io.ReadCloser, v interface{}) error {
	b, err := ioutil.ReadAll(r)
	if err != nil {
		return errors.Wrapf(err, "read body")
	}
	defer r.Close()

	if len(b) == 0 {
		return nil
	}

	if err := json.Unmarshal(b, v); err != nil {
		return errors.Wrapf(err, "json unmarshal %v", string(b))
	}

	return nil
}

func doCreateStage(ctx context.Context, sid string) *Project {
	ctx = logger.WithContext(ctx)
	project := NewProject(func(project *Project) {
		project.loggingCtx = ctx
		project.SID = sid
		project.MainDir = path.Join(workDir, "projects", fmt.Sprintf("project-%v", sid))
	})

	// If stage exists, load it.
	filename := project.buildProjectFile()
	if _, err := os.Stat(filename); err == nil {
		if err := project.Load(); err != nil {
			return nil
		}
	} else {
		if err := project.Save(); err != nil {
			return nil
		}
	}

	translatorServer.AddStage(project)
	logger.Tf(ctx, "Create project sid=%v", project.SID)

	go func() {
		defer project.Close()

		for ctx.Err() == nil {
			select {
			case <-ctx.Done():
			case <-time.After(3 * time.Second):
				if project.Expired() {
					logger.Tf(ctx, "Project: Remove %v for expired, update=%v",
						project.SID, project.update.Format(time.RFC3339))
					translatorServer.RemoveStage(project)
					return
				}
			}
		}
	}()
	return project
}

func handleStageCreate(ctx context.Context, w http.ResponseWriter, r *http.Request) error {
	project := doCreateStage(ctx, uuid.NewString())
	ctx = project.loggingCtx

	ohttp.WriteData(ctx, w, r, &struct {
		SID string `json:"sid"`
	}{
		SID: project.SID,
	})
	return nil
}

func handleStageLoad(ctx context.Context, w http.ResponseWriter, r *http.Request) error {
	var sid string
	if err := ParseBody(ctx, r.Body, &struct {
		SID *string `json:"sid"`
	}{
		SID: &sid,
	}); err != nil {
		return errors.Wrapf(err, "parse body")
	}

	project := translatorServer.QueryStage(sid)
	if project == nil {
		project = doCreateStage(ctx, sid)
	}

	ctx = project.loggingCtx

	ohttp.WriteData(ctx, w, r, &struct {
		// The UUID of stage.
		SID string `json:"sid"`
		// The input video file URL.
		InputURL string `json:"url"`
	}{
		SID: project.SID, InputURL: project.InputURL,
	})
	return nil
}

func handleStageAsr(ctx context.Context, w http.ResponseWriter, r *http.Request) error {
	var sid, inputURL string
	if err := ParseBody(ctx, r.Body, &struct {
		SID      *string `json:"sid"`
		InputURL *string `json:"url"`
	}{
		SID: &sid, InputURL: &inputURL,
	}); err != nil {
		return errors.Wrapf(err, "parse body")
	}

	project := translatorServer.QueryStage(sid)
	if project == nil {
		project = doCreateStage(ctx, sid)
	}

	ctx = project.loggingCtx
	project.asrInputAudio = path.Join(project.MainDir, "input.m4a")
	logger.Tf(ctx, "Handle project sid=%v, main=%v, url=%v, output=%v",
		project.SID, project.MainDir, inputURL, project.asrInputAudio)

	// Convert input to audio only file.
	if _, err := os.Stat(project.asrInputAudio); err != nil {
		project.InputURL = inputURL
		if err := project.Save(); err != nil {
			return errors.Wrapf(err, "save project")
		}

		inputFile := inputURL
		if strings.HasPrefix(inputFile, "/api/vod-translator/resources/") {
			inputFile = path.Join("static", inputFile[len("/api/vod-translator/resources/"):])
		}
		if _, err := os.Stat(inputFile); err != nil {
			return errors.Wrapf(err, "no file %v", inputFile)
		}

		if true {
			if err := exec.CommandContext(ctx, "ffmpeg",
				"-i", inputFile,
				"-vn", "-c:a", "aac", "-ac", "1", "-ar", "16000", "-ab", "50k",
				project.asrInputAudio,
			).Run(); err != nil {
				return errors.Errorf("Error converting the file")
			}
			logger.Tf(ctx, "Convert to ogg %v ok", project.asrInputAudio)
		}
	}

	// Load ASR from JSON file.
	project.asrOutputJSON = path.Join(project.MainDir, "input.json")
	if _, err := os.Stat(project.asrOutputJSON); err == nil {
		if err := project.loadAsrObject(); err != nil {
			return errors.Wrapf(err, "load asr object")
		}
		logger.Tf(ctx, "Load ASR object from %v ok", project.asrOutputJSON)
	} else {
		// Load the duration of input file.
		duration, bitrate, err := detectInput(ctx, project)
		if err != nil {
			return errors.Wrapf(err, "detect input")
		}

		// Reset the ASR output object.
		project.asrOutputObject = NewAudioResponse()

		// Split the audio to segments, because each ASR is limited to 25MB by OpenAI,
		// see https://platform.openai.com/docs/guides/speech-to-text
		limitDuration := int(25*1024*1024*8/float64(bitrate)) / 10
		for starttime := float64(0); starttime < duration; starttime += float64(limitDuration) {
			if err := func() error {
				tmpAsrInputAudio := path.Join(project.MainDir, fmt.Sprintf("input-%v.m4a", starttime))
				defer os.Remove(tmpAsrInputAudio)

				if err := exec.CommandContext(ctx, "ffmpeg",
					"-i", project.asrInputAudio,
					"-ss", fmt.Sprintf("%v", starttime), "-t", fmt.Sprintf("%v", limitDuration),
					"-c", "copy", "-y", tmpAsrInputAudio,
				).Run(); err != nil {
					return errors.Errorf("Error converting the file %v", tmpAsrInputAudio)
				}
				logger.Tf(ctx, "Convert to segment %v ok, starttime=%v", tmpAsrInputAudio, starttime)

				// Do ASR, convert to text.
				client := openai.NewClientWithConfig(aiConfig)
				resp, err := client.CreateTranscription(
					ctx,
					openai.AudioRequest{
						Model:    openai.Whisper1,
						FilePath: tmpAsrInputAudio,
						Format:   openai.AudioResponseFormatVerboseJSON,
						Language: os.Getenv("VODT_ASR_LANGUAGE"),
					},
				)
				if err != nil {
					return errors.Wrapf(err, "transcription")
				}
				logger.Tf(ctx, "ASR ok, project=%v, resp is <%v>B, segments=%v",
					project.SID, len(resp.Text), len(project.asrOutputObject.Segments))

				// Append the segment to ASR output object.
				project.asrOutputObject.AppendSegment(resp, starttime)
				if err := project.asrOutputObject.Save(project.asrOutputJSON); err != nil {
					return errors.Wrapf(err, "save")
				}
				logger.Tf(ctx, "Save ASR output to %v ok", project.asrOutputJSON)

				return nil
			}(); err != nil {
				return errors.Wrapf(err, "split starttime=%v, duration=%v", starttime, limitDuration)
			}
		}
	}

	ohttp.WriteData(ctx, w, r, &struct {
		SID string         `json:"sid"`
		ASR *AudioResponse `json:"asr"`
	}{
		SID: project.SID, ASR: project.asrOutputObject,
	})
	return nil
}

func handleStageAsrUpdate(ctx context.Context, w http.ResponseWriter, r *http.Request) error {
	var sid string
	var segment AudioSegment
	if err := ParseBody(ctx, r.Body, &struct {
		SID     *string       `json:"sid"`
		Segment *AudioSegment `json:"segment"`
	}{
		SID: &sid, Segment: &segment,
	}); err != nil {
		return errors.Wrapf(err, "parse body")
	}

	stage := translatorServer.QueryStage(sid)
	if stage == nil {
		stage = doCreateStage(ctx, sid)
	}
	ctx = stage.loggingCtx

	target := stage.asrOutputObject.QuerySegment(segment.UUID)
	if target == nil {
		return errors.Errorf("no segment %v", segment.UUID)
	}

	// Update target.
	if target.Translated != segment.Translated {
		target.TranslatedAt = AITime(time.Now())
	}
	target.Removed = segment.Removed
	target.Update = AITime(time.Now())
	target.Text = segment.Text
	target.Translated = segment.Translated

	if err := stage.asrOutputObject.Save(stage.asrOutputJSON); err != nil {
		return errors.Wrapf(err, "save")
	}
	logger.Tf(ctx, "Save ASR output to %v ok", stage.asrOutputJSON)

	ohttp.WriteData(ctx, w, r, nil)
	return nil
}

func handleStageTranslate(ctx context.Context, w http.ResponseWriter, r *http.Request) error {
	var sid string
	var segment AudioSegment
	if err := ParseBody(ctx, r.Body, &struct {
		SID     *string       `json:"sid"`
		Segment *AudioSegment `json:"segment"`
	}{
		SID: &sid, Segment: &segment,
	}); err != nil {
		return errors.Wrapf(err, "parse body")
	}

	stage := translatorServer.QueryStage(sid)
	if stage == nil {
		stage = doCreateStage(ctx, sid)
	}
	ctx = stage.loggingCtx

	target := stage.asrOutputObject.QuerySegment(segment.UUID)
	if target == nil {
		return errors.Errorf("no segment %v", segment.UUID)
	}

	shouldTranslate := func(target *AudioSegment) bool {
		if target.Removed || target.Text == "" {
			return false
		}
		return target.Translated == "" || time.Time(target.Update).After(time.Time(target.TranslatedAt))
	}
	if shouldTranslate(target) {
		messages := []openai.ChatCompletionMessage{
			{Role: openai.ChatMessageRoleSystem, Content: DefaultTranslatePrompt},
		}
		previous := stage.asrOutputObject.QueryPrevious(target)
		if previous != nil && previous.Translated != "" && previous.Text != "" {
			messages = append(messages, openai.ChatCompletionMessage{
				Role: openai.ChatMessageRoleUser, Content: previous.Text,
			})
			messages = append(messages, openai.ChatCompletionMessage{
				Role: openai.ChatMessageRoleAssistant, Content: previous.Translated,
			})
		}
		messages = append(messages, openai.ChatCompletionMessage{
			Role: openai.ChatMessageRoleUser, Content: target.Text,
		})

		client := openai.NewClientWithConfig(aiConfig)
		resp, err := client.CreateChatCompletion(ctx, openai.ChatCompletionRequest{
			Model:    openai.GPT3Dot5Turbo1106,
			Messages: messages,
		})
		if err != nil {
			return errors.Wrapf(err, "translate")
		}

		target.Translated = resp.Choices[0].Message.Content
		target.TranslatedAt = AITime(time.Now())
		logger.Tf(ctx, "Translate ok, messages=%v, resp is <%v>B", len(messages), len(target.Translated))

		if err := stage.asrOutputObject.Save(stage.asrOutputJSON); err != nil {
			return errors.Wrapf(err, "save")
		}
		logger.Tf(ctx, "Save ASR output to %v ok", stage.asrOutputJSON)
	} else {
		logger.Tf(ctx, "Ignore translation for %v", target)
	}

	ohttp.WriteData(ctx, w, r, &struct {
		Segment *AudioSegment `json:"segment"`
	}{
		Segment: target,
	})
	return nil
}

func handleStageShorter(ctx context.Context, w http.ResponseWriter, r *http.Request) error {
	var sid string
	var segment AudioSegment
	if err := ParseBody(ctx, r.Body, &struct {
		SID     *string       `json:"sid"`
		Segment *AudioSegment `json:"segment"`
	}{
		SID: &sid, Segment: &segment,
	}); err != nil {
		return errors.Wrapf(err, "parse body")
	}

	stage := translatorServer.QueryStage(sid)
	if stage == nil {
		stage = doCreateStage(ctx, sid)
	}
	ctx = stage.loggingCtx

	target := stage.asrOutputObject.QuerySegment(segment.UUID)
	if target == nil {
		return errors.Errorf("no segment %v", segment.UUID)
	}

	if true {
		messages := []openai.ChatCompletionMessage{
			{Role: openai.ChatMessageRoleSystem, Content: "Make the text shorter. Please maintain the original meaning."},
		}
		if previous := stage.asrOutputObject.QueryPrevious(target); previous != nil && previous.Translated != "" {
			messages = append(messages, []openai.ChatCompletionMessage{
				{Role: openai.ChatMessageRoleUser, Content: previous.Translated},
				{Role: openai.ChatMessageRoleAssistant, Content: previous.Translated},
			}...)
		}
		messages = append(messages, []openai.ChatCompletionMessage{
			{Role: openai.ChatMessageRoleUser, Content: target.Translated},
		}...)

		client := openai.NewClientWithConfig(aiConfig)
		resp, err := client.CreateChatCompletion(ctx, openai.ChatCompletionRequest{
			Model:    openai.GPT4TurboPreview,
			Messages: messages,
		})
		if err != nil {
			return errors.Wrapf(err, "translate")
		}

		target.Translated = resp.Choices[0].Message.Content
		target.TranslatedAt = AITime(time.Now())
		logger.Tf(ctx, "Translate ok, messages=%v, resp is <%v>B", len(messages), len(target.Translated))

		if err := stage.asrOutputObject.Save(stage.asrOutputJSON); err != nil {
			return errors.Wrapf(err, "save")
		}
		logger.Tf(ctx, "Save ASR output to %v ok", stage.asrOutputJSON)
	} else {
		logger.Tf(ctx, "Ignore translation for %v", target)
	}

	ohttp.WriteData(ctx, w, r, &struct {
		Segment *AudioSegment `json:"segment"`
	}{
		Segment: target,
	})
	return nil
}

func doTTS(ctx context.Context, stage *Project, target *AudioSegment) error {
	client := openai.NewClientWithConfig(aiConfig)
	resp, err := client.CreateSpeech(ctx, openai.CreateSpeechRequest{
		Model:          openai.TTSModel1,
		Input:          target.Translated,
		Voice:          openai.VoiceNova,
		ResponseFormat: openai.SpeechResponseFormatAac,
	})
	if err != nil {
		return errors.Wrapf(err, "create speech")
	}
	defer resp.Close()

	ttsFilename := fmt.Sprintf("tts-%v.aac", target.UUID)
	ttsFile := path.Join(stage.MainDir, ttsFilename)
	out, err := os.Create(ttsFile)
	if err != nil {
		return errors.Errorf("Unable to create the file %v for writing", ttsFile)
	}
	defer out.Close()

	if _, err = io.Copy(out, resp); err != nil {
		return errors.Errorf("Error writing the file")
	}

	target.TTS = ttsFilename
	target.TTSAt = AITime(time.Now())
	logger.Tf(ctx, "TTS ok")
	return nil
}

func detectInput(ctx context.Context, stage *Project) (duration float64, bitrate int, err error) {
	args := []string{
		"-show_error", "-show_private_data", "-v", "quiet", "-find_stream_info", "-print_format", "json",
		"-show_format",
	}
	args = append(args, "-i", stage.asrInputAudio)

	stdout, err := exec.CommandContext(ctx, "ffprobe", args...).Output()
	if err != nil {
		err = errors.Wrapf(err, "probe %v", stage.asrInputAudio)
		return
	}

	type VLiveFileFormat struct {
		Starttime string `json:"start_time"`
		Duration  string `json:"duration"`
		Bitrate   string `json:"bit_rate"`
		Streams   int32  `json:"nb_streams"`
		Score     int32  `json:"probe_score"`
		HasVideo  bool   `json:"has_video"`
		HasAudio  bool   `json:"has_audio"`
	}

	format := struct {
		Format VLiveFileFormat `json:"format"`
	}{}
	if err = json.Unmarshal([]byte(stdout), &format); err != nil {
		err = errors.Wrapf(err, "parse format %v", stdout)
		return
	}

	var fv float64
	if fv, err = strconv.ParseFloat(format.Format.Duration, 64); err != nil {
		err = errors.Wrapf(err, "parse duration %v", format.Format.Duration)
		return
	} else {
		duration = fv
	}

	var iv int64
	if iv, err = strconv.ParseInt(format.Format.Bitrate, 10, 64); err != nil {
		err = errors.Wrapf(err, "parse bitrate %v", format.Format.Bitrate)
		return
	} else {
		bitrate = int(iv)
	}

	logger.Tf(ctx, "ASR input duration=%v, bitrate=%v", duration, bitrate)
	return
}

func detectTTS(ctx context.Context, stage *Project, target *AudioSegment) error {
	args := []string{
		"-show_error", "-show_private_data", "-v", "quiet", "-find_stream_info", "-print_format", "json",
		"-show_format",
	}
	args = append(args, "-i", path.Join(stage.MainDir, target.TTS))

	stdout, err := exec.CommandContext(ctx, "ffprobe", args...).Output()
	if err != nil {
		return errors.Wrapf(err, "probe %v", target.TTS)
	}

	type VLiveFileFormat struct {
		Starttime string `json:"start_time"`
		Duration  string `json:"duration"`
		Bitrate   string `json:"bit_rate"`
		Streams   int32  `json:"nb_streams"`
		Score     int32  `json:"probe_score"`
		HasVideo  bool   `json:"has_video"`
		HasAudio  bool   `json:"has_audio"`
	}

	format := struct {
		Format VLiveFileFormat `json:"format"`
	}{}
	if err = json.Unmarshal([]byte(stdout), &format); err != nil {
		return errors.Wrapf(err, "parse format %v", stdout)
	}

	if fv, err := strconv.ParseFloat(format.Format.Duration, 64); err != nil {
		return errors.Wrapf(err, "parse duration %v", format.Format.Duration)
	} else {
		target.TTSDuration = fv
	}
	logger.Tf(ctx, "TTS duration %v", target.TTSDuration)
	return nil
}

func handleStageTTS(ctx context.Context, w http.ResponseWriter, r *http.Request) error {
	var sid string
	var segment AudioSegment
	if err := ParseBody(ctx, r.Body, &struct {
		SID     *string       `json:"sid"`
		Segment *AudioSegment `json:"segment"`
	}{
		SID: &sid, Segment: &segment,
	}); err != nil {
		return errors.Wrapf(err, "parse body")
	}

	stage := translatorServer.QueryStage(sid)
	if stage == nil {
		return errors.Errorf("no stage %v", sid)
	}
	ctx = stage.loggingCtx

	target := stage.asrOutputObject.QuerySegment(segment.UUID)
	if target == nil {
		return errors.Errorf("no segment %v", segment.UUID)
	}

	shouldTTS := func(target *AudioSegment) bool {
		if target.Removed || target.Text == "" || target.Translated == "" {
			return false
		}
		return target.TTS == "" || target.TTSDuration <= 0 || time.Time(target.TranslatedAt).After(time.Time(target.TTSAt))
	}
	if shouldTTS(target) {
		if err := doTTS(ctx, stage, target); err != nil {
			return errors.Wrapf(err, "tts")
		}
	} else {
		logger.Tf(ctx, "Ignore TTS for %v", target)
	}

	if err := detectTTS(ctx, stage, target); err != nil {
		return errors.Wrapf(err, "detect")
	}

	if err := stage.asrOutputObject.Save(stage.asrOutputJSON); err != nil {
		return errors.Wrapf(err, "save")
	}
	logger.Tf(ctx, "Save ASR output to %v ok", stage.asrOutputJSON)

	ohttp.WriteData(ctx, w, r, &struct {
		Segment *AudioSegment `json:"segment"`
	}{
		Segment: target,
	})
	return nil
}

func handleStagePreview(ctx context.Context, w http.ResponseWriter, r *http.Request) error {
	ss := strings.Split(r.URL.Path[len("/api/vod-translator/preview/"):], "/")
	sid, uuid, filename := ss[0], ss[1], ss[2]

	stage := translatorServer.QueryStage(sid)
	if stage == nil {
		return errors.Errorf("no stage %v", sid)
	}
	ctx = stage.loggingCtx

	target := stage.asrOutputObject.QuerySegment(uuid)
	if target == nil {
		return errors.Errorf("no segment %v", uuid)
	}
	logger.Tf(ctx, "Serve TTS %v %v", target, filename)

	w.Header().Set("Content-Type", "audio/aac")

	ttsFileServer := http.FileServer(http.Dir(path.Join(stage.MainDir)))
	r.URL.Path = fmt.Sprintf("/%v", target.TTS)
	ttsFileServer.ServeHTTP(w, r)
	return nil
}

func handleStageMerge(ctx context.Context, w http.ResponseWriter, r *http.Request) error {
	var sid string
	var segment, nextSegment AudioSegment
	if err := ParseBody(ctx, r.Body, &struct {
		SID     *string       `json:"sid"`
		Segment *AudioSegment `json:"segment"`
		Next    *AudioSegment `json:"next"`
	}{
		SID: &sid, Segment: &segment, Next: &nextSegment,
	}); err != nil {
		return errors.Wrapf(err, "parse body")
	}

	stage := translatorServer.QueryStage(sid)
	if stage == nil {
		return errors.Errorf("no stage %v", sid)
	}
	ctx = stage.loggingCtx

	target := stage.asrOutputObject.QuerySegment(segment.UUID)
	if target == nil {
		return errors.Errorf("no segment %v", segment.UUID)
	}

	next := stage.asrOutputObject.QuerySegment(nextSegment.UUID)
	if next == nil {
		return errors.Errorf("no segment %v", nextSegment.UUID)
	}
	if next.Removed {
		return errors.Errorf("invalid next %v", nextSegment)
	}

	previous := stage.asrOutputObject.QueryPrevious(next)
	if previous != target {
		return errors.Errorf("invalid %v next %v", segment, nextSegment)
	}

	target.End = next.End
	target.Text += " " + next.Text
	target.Tokens = append(target.Tokens, next.Tokens...)
	target.Translated += " " + next.Translated
	target.TranslatedAt = AITime(time.Now())

	if err := doTTS(ctx, stage, target); err != nil {
		return errors.Wrapf(err, "tts")
	}
	if err := detectTTS(ctx, stage, target); err != nil {
		return errors.Wrapf(err, "detect")
	}

	// Remove the next, after merged to target.
	stage.asrOutputObject.RemoveSegment(next)

	if err := stage.asrOutputObject.Save(stage.asrOutputJSON); err != nil {
		return errors.Wrapf(err, "save")
	}
	logger.Tf(ctx, "Save ASR output to %v ok", stage.asrOutputJSON)

	ohttp.WriteData(ctx, w, r, &struct {
		Segment *AudioSegment `json:"segment"`
	}{
		Segment: target,
	})
	return nil
}

func handleStageExport(ctx context.Context, w http.ResponseWriter, r *http.Request) error {
	var sid string
	if err := ParseBody(ctx, r.Body, &struct {
		SID *string `json:"sid"`
	}{
		SID: &sid,
	}); err != nil {
		return errors.Wrapf(err, "parse body")
	}

	stage := translatorServer.QueryStage(sid)
	if stage == nil {
		return errors.Errorf("no stage %v", sid)
	}
	ctx = stage.loggingCtx

	audioFilename := fmt.Sprintf("audio-%v.wav", stage.SID)
	audioFile := path.Join(stage.MainDir, audioFilename)

	f, err := os.Create(audioFile)
	if err != nil {
		return errors.Wrapf(err, "create %v", audioFile)
	}
	defer f.Close()

	// 100KHZ, each frame is 10ms.
	buf := &audio.IntBuffer{Data: make([]int, 100000*48), Format: &audio.Format{SampleRate: 100000, NumChannels: 1}}
	enc := wav.NewEncoder(f, buf.Format.SampleRate, 16, buf.Format.NumChannels, 1)
	defer enc.Close()

	insertSilent := func(duration float64) error {
		if duration >= 0.01 {
			logger.Tf(ctx, "Write wav ok, silent=%v", duration)
			return enc.Write(&audio.IntBuffer{
				Data:   make([]int, int(100000*duration)),
				Format: &audio.Format{SampleRate: 100000, NumChannels: 1},
			})
		}
		return nil
	}

	var previous *AudioSegment
	for _, segment := range stage.asrOutputObject.Segments {
		var gap float64
		if previous != nil {
			gap = segment.Start - previous.End
		}
		previous = segment
		logger.Tf(ctx, "Handle segment %v, time %v~%v", segment.UUID, segment.Start, segment.End)

		if err := insertSilent(gap); err != nil {
			return errors.Wrapf(err, "insert silent %v", gap)
		}

		if segment.TTS == "" || segment.Removed {
			if err := insertSilent(segment.End - segment.Start); err != nil {
				return errors.Wrapf(err, "insert silent %v", segment.End-segment.Start)
			}
			continue
		}

		var wavDuration float64
		if err := func() error {
			ttsFile := path.Join(stage.MainDir, segment.TTS)
			wavFile := path.Join(stage.MainDir, fmt.Sprintf("tts-%v.wav", segment.UUID))
			logger.Tf(ctx, "Convert tts %v to wav", ttsFile)
			if true {
				if err := exec.CommandContext(ctx, "ffmpeg",
					"-i", ttsFile,
					"-vn", "-c:a", "pcm_s16le", "-ac", "1", "-ar", "100000", "-ab", "300k",
					"-y", wavFile,
				).Run(); err != nil {
					return errors.Errorf("Error converting the file")
				}
			}

			wf, err := os.Open(wavFile)
			if err != nil {
				return errors.Wrapf(err, "open %v", wavFile)
			}
			defer wf.Close()

			dec := wav.NewDecoder(wf)
			bufWav, err := dec.FullPCMBuffer()
			if err != nil {
				return errors.Wrapf(err, "decode %v", wavFile)
			}
			if err = enc.Write(bufWav); err != nil {
				return errors.Wrapf(err, "write %v", wavFile)
			}

			wavDuration = float64(len(bufWav.Data)) / 100000.
			logger.Tf(ctx, "Write wav ok, duration=%v, data=%.3f", segment.TTSDuration, wavDuration)
			return nil
		}(); err != nil {
			return errors.Wrapf(err, "merge")
		}

		if err := insertSilent(segment.End - segment.Start - wavDuration); err != nil {
			return errors.Wrapf(err, "insert silent %v", segment.End-segment.Start-wavDuration)
		}
	}

	enc.Close()
	logger.Tf(ctx, "All segments are converted")

	aacFilename := fmt.Sprintf("audio-%v.mp4", stage.SID)
	aacFile := path.Join(stage.MainDir, aacFilename)
	if true {
		if err := exec.CommandContext(ctx, "ffmpeg",
			"-i", audioFile,
			"-vn", "-c:a", "aac", "-ac", "2", "-ar", "44100", "-ab", "120k",
			"-y", aacFile,
		).Run(); err != nil {
			return errors.Errorf("Error converting the file")
		}
		logger.Tf(ctx, "Convert to aac %v ok", aacFile)
	}
	logger.Tf(ctx, "Download AAC ok")

	http.ServeFile(w, r, aacFile)
	return nil
}

func doMain(ctx context.Context) error {
	if err := doConfig(ctx); err != nil {
		return errors.Wrapf(err, "config")
	}

	// Setup the work dir.
	if pwd, err := os.Getwd(); err != nil {
		return errors.Wrapf(err, "getwd")
	} else {
		workDir = pwd
	}

	// Create the translator server.
	translatorServer = NewTranslatorServer()
	defer translatorServer.Close()

	fs := http.FileServer(http.Dir("./static"))
	http.HandleFunc("/api/vod-translator/resources/", func(w http.ResponseWriter, r *http.Request) {
		r.URL.Path = r.URL.Path[len("/api/vod-translator/resources/"):]
		fs.ServeHTTP(w, r)
	})

	http.HandleFunc("/api/vod-translator/create/", func(w http.ResponseWriter, r *http.Request) {
		if err := handleStageCreate(ctx, w, r); err != nil {
			logger.Tf(ctx, "error: %+v", err)
			http.Error(w, err.Error(), http.StatusInternalServerError)
		}
	})

	http.HandleFunc("/api/vod-translator/load/", func(w http.ResponseWriter, r *http.Request) {
		if err := handleStageLoad(ctx, w, r); err != nil {
			logger.Tf(ctx, "error: %+v", err)
			http.Error(w, err.Error(), http.StatusInternalServerError)
		}
	})

	http.HandleFunc("/api/vod-translator/asr/", func(w http.ResponseWriter, r *http.Request) {
		if err := handleStageAsr(ctx, w, r); err != nil {
			logger.Tf(ctx, "error: %+v", err)
			http.Error(w, err.Error(), http.StatusInternalServerError)
		}
	})

	http.HandleFunc("/api/vod-translator/asr-update/", func(w http.ResponseWriter, r *http.Request) {
		if err := handleStageAsrUpdate(ctx, w, r); err != nil {
			logger.Tf(ctx, "error: %+v", err)
			http.Error(w, err.Error(), http.StatusInternalServerError)
		}
	})

	http.HandleFunc("/api/vod-translator/translate/", func(w http.ResponseWriter, r *http.Request) {
		if err := handleStageTranslate(ctx, w, r); err != nil {
			logger.Tf(ctx, "error: %+v", err)
			http.Error(w, err.Error(), http.StatusInternalServerError)
		}
	})

	http.HandleFunc("/api/vod-translator/shorter/", func(w http.ResponseWriter, r *http.Request) {
		if err := handleStageShorter(ctx, w, r); err != nil {
			logger.Tf(ctx, "error: %+v", err)
			http.Error(w, err.Error(), http.StatusInternalServerError)
		}
	})

	http.HandleFunc("/api/vod-translator/merge/", func(w http.ResponseWriter, r *http.Request) {
		if err := handleStageMerge(ctx, w, r); err != nil {
			logger.Tf(ctx, "error: %+v", err)
			http.Error(w, err.Error(), http.StatusInternalServerError)
		}
	})

	http.HandleFunc("/api/vod-translator/tts/", func(w http.ResponseWriter, r *http.Request) {
		if err := handleStageTTS(ctx, w, r); err != nil {
			logger.Tf(ctx, "error: %+v", err)
			http.Error(w, err.Error(), http.StatusInternalServerError)
		}
	})

	http.HandleFunc("/api/vod-translator/preview/", func(w http.ResponseWriter, r *http.Request) {
		if err := handleStagePreview(ctx, w, r); err != nil {
			logger.Tf(ctx, "error: %+v", err)
			http.Error(w, err.Error(), http.StatusInternalServerError)
		}
	})

	http.HandleFunc("/api/vod-translator/export/", func(w http.ResponseWriter, r *http.Request) {
		if err := handleStageExport(ctx, w, r); err != nil {
			logger.Tf(ctx, "error: %+v", err)
			http.Error(w, err.Error(), http.StatusInternalServerError)
		}
	})

	if err := http.ListenAndServe(":3001", nil); err != nil {
		return errors.Wrap(err, "http serve")
	}
	return nil
}

func doConfig(ctx context.Context) error {
	// setEnvDefault set env key=value if not set.
	setEnvDefault := func(key, value string) {
		if os.Getenv(key) == "" {
			os.Setenv(key, value)
		}
	}

	setEnvDefault("OPENAI_API_KEY", "")
	setEnvDefault("OPENAI_PROXY", "https://api.openai.com/v1")
	setEnvDefault("VODT_ASR_LANGUAGE", DefaultAsrLanguage)
	logger.Tf(ctx, "Environment variables: OPENAI_API_KEY=%vB, OPENAI_PROXY=%v, VODT_ASR_LANGUAGE=%v",
		len(os.Getenv("OPENAI_API_KEY")), os.Getenv("OPENAI_PROXY"), os.Getenv("VODT_ASR_LANGUAGE"))

	// Load env variables from file.
	if _, err := os.Stat("../.env"); err == nil {
		if err := godotenv.Overload("../.env"); err != nil {
			return errors.Wrapf(err, "load env")
		}
	}
	if os.Getenv("OPENAI_API_KEY") == "" {
		return errors.New("OPENAI_API_KEY is required")
	}

	// Initialize OpenAI client config.
	aiConfig = openai.DefaultConfig(os.Getenv("OPENAI_API_KEY"))
	aiConfig.BaseURL = os.Getenv("OPENAI_PROXY")
	logger.Tf(ctx, "OpenAI key(OPENAI_API_KEY): %vB, proxy(OPENAI_PROXY): %v, base url: %v",
		len(os.Getenv("OPENAI_API_KEY")), os.Getenv("OPENAI_PROXY"), aiConfig.BaseURL)

	return nil
}
