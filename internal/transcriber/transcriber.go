// Package transcriber provides local speech-to-text transcription
package transcriber

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

const (
	// DefaultModelPath is the default location for the whisper model
	DefaultModelPath = "models/ggml-base.en.bin"
)

// Segment represents a transcribed audio segment
type Segment struct {
	Text      string
	StartTime time.Duration
	EndTime   time.Duration
	Timestamp time.Time
}

// Transcriber handles local speech-to-text transcription
type Transcriber struct {
	modelPath    string
	audioBuffer  []float32
	mu           sync.Mutex
	isProcessing bool
	segments     []Segment
	onSegment    func(Segment)
	lastProcess  time.Time
	sampleRate   int
}

// Config holds transcriber configuration
type Config struct {
	ModelPath  string
	SampleRate int
	OnSegment  func(Segment)
}

// New creates a new Transcriber instance
func New(cfg Config) (*Transcriber, error) {
	if cfg.ModelPath == "" {
		cfg.ModelPath = DefaultModelPath
	}

	if cfg.SampleRate == 0 {
		cfg.SampleRate = 16000
	}

	t := &Transcriber{
		modelPath:   cfg.ModelPath,
		audioBuffer: make([]float32, 0, cfg.SampleRate*30), // 30 seconds buffer
		sampleRate:  cfg.SampleRate,
		onSegment:   cfg.OnSegment,
		segments:    make([]Segment, 0),
	}

	return t, nil
}

// LoadModel loads the whisper model from disk
func (t *Transcriber) LoadModel() error {
	// Check if model file exists
	if _, err := os.Stat(t.modelPath); os.IsNotExist(err) {
		return fmt.Errorf("model not found at %s - please download a model first", t.modelPath)
	}

	// Model loading will be handled by the whisper bindings
	// For now, we'll use a stub that will be replaced with actual whisper.cpp integration
	return nil
}

// ProcessAudio adds audio samples to the buffer and triggers transcription
func (t *Transcriber) ProcessAudio(samples []float32) {
	t.mu.Lock()
	defer t.mu.Unlock()

	// Append samples to buffer
	t.audioBuffer = append(t.audioBuffer, samples...)

	// Check if we have enough audio to process (at least 3 seconds)
	minSamples := t.sampleRate * 3
	if len(t.audioBuffer) < minSamples {
		return
	}

	// Check if enough time has passed since last processing (process every 5 seconds)
	if time.Since(t.lastProcess) < 5*time.Second {
		return
	}

	// Trigger async processing
	if !t.isProcessing {
		t.isProcessing = true
		go t.transcribe()
	}
}

// transcribe performs the actual transcription
func (t *Transcriber) transcribe() {
	t.mu.Lock()
	audioData := make([]float32, len(t.audioBuffer))
	copy(audioData, t.audioBuffer)
	// Keep last 2 seconds for context overlap
	overlapSamples := t.sampleRate * 2
	if len(t.audioBuffer) > overlapSamples {
		t.audioBuffer = t.audioBuffer[len(t.audioBuffer)-overlapSamples:]
	}
	t.lastProcess = time.Now()
	t.mu.Unlock()

	// Perform transcription using whisper.cpp
	// This is where the actual whisper transcription happens
	segment := t.runWhisper(audioData)

	if segment.Text != "" {
		t.mu.Lock()
		t.segments = append(t.segments, segment)
		t.mu.Unlock()

		if t.onSegment != nil {
			t.onSegment(segment)
		}
	}

	t.mu.Lock()
	t.isProcessing = false
	t.mu.Unlock()
}

// runWhisper runs the whisper transcription on audio data
func (t *Transcriber) runWhisper(audio []float32) Segment {
	// This will be implemented with actual whisper.cpp bindings
	// For now, return empty segment - the actual implementation
	// will use the whisper.cpp Go bindings

	return Segment{
		Text:      "",
		StartTime: 0,
		EndTime:   time.Duration(len(audio)) * time.Second / time.Duration(t.sampleRate),
		Timestamp: time.Now(),
	}
}

// GetSegments returns all transcribed segments
func (t *Transcriber) GetSegments() []Segment {
	t.mu.Lock()
	defer t.mu.Unlock()

	result := make([]Segment, len(t.segments))
	copy(result, t.segments)
	return result
}

// GetFullTranscript returns the complete transcript as a single string
func (t *Transcriber) GetFullTranscript() string {
	segments := t.GetSegments()
	var result string
	for _, s := range segments {
		if result != "" {
			result += " "
		}
		result += s.Text
	}
	return result
}

// Clear clears the audio buffer and segments
func (t *Transcriber) Clear() {
	t.mu.Lock()
	defer t.mu.Unlock()

	t.audioBuffer = t.audioBuffer[:0]
	t.segments = t.segments[:0]
}

// Close releases resources
func (t *Transcriber) Close() error {
	t.mu.Lock()
	defer t.mu.Unlock()

	t.audioBuffer = nil
	t.segments = nil
	return nil
}

// ModelExists checks if a model file exists at the given path
func ModelExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// GetModelsDir returns the models directory path
func GetModelsDir() string {
	// Check for models in current directory first
	if _, err := os.Stat("models"); err == nil {
		return "models"
	}

	// Check in home directory
	home, err := os.UserHomeDir()
	if err != nil {
		return "models"
	}

	return filepath.Join(home, ".rekord", "models")
}
