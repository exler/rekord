package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/exler/rekord/internal/audio"
	"github.com/exler/rekord/internal/logging"
	"github.com/exler/rekord/internal/transcriber"
	"github.com/exler/rekord/internal/ui"
)

var (
	modelPath  string
	deviceName string
	micDevice  string
	noMic      bool
	outputDir  string
	logDir     string
)

func init() {
	defaultModel := filepath.Join(transcriber.GetModelsDir(), "ggml-base.en.bin")
	defaultLogDir := filepath.Join(os.TempDir(), "rekord", "logs")

	flag.StringVar(&modelPath, "model", defaultModel, "Path to the whisper model file")
	flag.StringVar(&deviceName, "device", "", "System audio device name (leave empty for default monitor)")
	flag.StringVar(&micDevice, "mic", "", "Microphone device name (leave empty for default input)")
	flag.BoolVar(&noMic, "no-mic", false, "Disable microphone capture (system audio only)")
	flag.StringVar(&outputDir, "output", ".", "Output directory for transcripts")
	flag.StringVar(&logDir, "logdir", defaultLogDir, "Directory for log files")
}

// App holds the application state
type App struct {
	capture     *audio.Capture
	transcriber *transcriber.Transcriber
	whisper     *transcriber.WhisperCLI
	program     *tea.Program
	model       ui.Model

	audioBuffer []float32
	bufferMu    sync.Mutex
	segments    []transcriber.Segment

	// Control channels for transcription loop
	stopTranscription chan struct{}
	transcriptionDone chan struct{}
}

func main() {
	flag.Parse()

	// Initialize logging first
	if err := logging.Init(logDir); err != nil {
		fmt.Fprintf(os.Stderr, "Warning: Failed to initialize logging: %v\n", err)
	}
	defer logging.Close()

	logging.Info("Rekord starting up")
	logging.Info("Model: %s", modelPath)
	logging.Info("Log directory: %s", logDir)

	// Get default monitor if no device specified
	if deviceName == "" {
		monitor, err := audio.GetDefaultMonitorSource()
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error getting default audio monitor: %v\n", err)
			fmt.Fprintf(os.Stderr, "Please specify a device with -device flag\n")
			fmt.Fprintf(os.Stderr, "\nAvailable sources:\n")
			sources, _ := audio.ListMonitorSources()
			for _, s := range sources {
				if s.IsMonitor {
					fmt.Fprintf(os.Stderr, "  %s (monitor)\n", s.Name)
				} else if s.IsInput {
					fmt.Fprintf(os.Stderr, "  %s (input)\n", s.Name)
				} else {
					fmt.Fprintf(os.Stderr, "  %s\n", s.Name)
				}
			}
			logging.Error("No default audio monitor found")
			os.Exit(1)
		}
		deviceName = monitor
	}
	logging.Info("System audio device: %s", deviceName)

	// Get default microphone if not disabled and not specified
	if !noMic && micDevice == "" {
		mic, err := audio.GetDefaultInputSource()
		if err != nil {
			logging.Warn("Could not find default microphone: %v", err)
			fmt.Fprintf(os.Stderr, "Warning: Could not find default microphone: %v\n", err)
			fmt.Fprintf(os.Stderr, "Continuing with system audio only. Use -mic to specify a microphone.\n")
		} else {
			micDevice = mic
		}
	}

	if noMic {
		logging.Info("Microphone capture disabled")
	} else if micDevice != "" {
		logging.Info("Microphone device: %s", micDevice)
	}

	// Check model exists
	if !transcriber.ModelExists(modelPath) {
		fmt.Fprint(os.Stderr, "Model not found. Please download a Whisper model as per the README instructions.")
		logging.Error("Model not found: %s", modelPath)
		os.Exit(1)
	}

	// Create whisper CLI wrapper
	whisper, err := transcriber.NewWhisperCLI(modelPath)
	if err != nil {
		fmt.Fprint(os.Stderr, "Error initializing whisper.cpp. Please ensure whisper-cli is in your PATH.")
		logging.Error("Whisper initialization failed: %v", err)
		os.Exit(1)
	}
	logging.Info("Whisper CLI initialized")

	// Create application
	app := &App{
		whisper:     whisper,
		audioBuffer: make([]float32, 0, audio.SampleRate*60), // 1 minute buffer
		segments:    make([]transcriber.Segment, 0),
	}

	// Create transcriber
	app.transcriber, err = transcriber.New(transcriber.Config{
		ModelPath:  modelPath,
		SampleRate: audio.SampleRate,
		OnSegment: func(seg transcriber.Segment) {
			if app.program != nil {
				app.program.Send(ui.NewSegmentMsg{Segment: seg})
			}
		},
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error creating transcriber: %v\n", err)
		logging.Error("Transcriber creation failed: %v", err)
		os.Exit(1)
	}

	// Build device info string for UI
	deviceInfo := deviceName
	if micDevice != "" {
		deviceInfo = fmt.Sprintf("System: %s | Mic: %s", shortenDeviceName(deviceName), shortenDeviceName(micDevice))
	}

	// Create UI model
	app.model = ui.New(filepath.Base(modelPath), deviceInfo)
	app.model.SetCallbacks(app.startRecording, app.stopRecording, app.saveTranscript)

	// Create and run program
	app.program = tea.NewProgram(app.model, tea.WithAltScreen())

	logging.Info("Starting TUI")
	if _, err := app.program.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "Error running program: %v\n", err)
		logging.Error("Program error: %v", err)
		os.Exit(1)
	}

	// Cleanup
	logging.Info("Shutting down")
	if app.capture != nil {
		app.capture.Close()
	}
	app.whisper.Close()
}

// shortenDeviceName shortens a device name for display
func shortenDeviceName(name string) string {
	// Remove common prefixes for cleaner display
	name = strings.TrimPrefix(name, "alsa_output.")
	name = strings.TrimPrefix(name, "alsa_input.")
	name = strings.TrimSuffix(name, ".monitor")

	// Truncate if too long
	if len(name) > 30 {
		name = name[:27] + "..."
	}
	return name
}

// startRecording starts audio capture
func (a *App) startRecording() error {
	logging.Info("Starting recording")

	// Build list of devices to capture
	devices := []string{deviceName}
	if micDevice != "" && !noMic {
		devices = append(devices, micDevice)
	}

	var err error
	a.capture, err = audio.NewMultiCapture(devices, a.onAudioData)
	if err != nil {
		logging.Error("Failed to create audio capture: %v", err)
		return fmt.Errorf("failed to create audio capture: %w", err)
	}

	if err := a.capture.Start(); err != nil {
		logging.Error("Failed to start audio capture: %v", err)
		return fmt.Errorf("failed to start audio capture: %w", err)
	}

	// Clear buffers
	a.bufferMu.Lock()
	a.audioBuffer = a.audioBuffer[:0]
	a.bufferMu.Unlock()

	// Create control channels
	a.stopTranscription = make(chan struct{})
	a.transcriptionDone = make(chan struct{})

	// Start transcription goroutine
	go a.transcriptionLoop()

	logging.Info("Recording started successfully with %d device(s)", len(devices))
	return nil
}

// stopRecording stops audio capture
func (a *App) stopRecording() error {
	logging.Info("Stopping recording")

	// Signal transcription loop to stop
	if a.stopTranscription != nil {
		close(a.stopTranscription)
	}

	// Stop audio capture
	if a.capture != nil {
		if err := a.capture.Stop(); err != nil {
			logging.Error("Failed to stop audio capture: %v", err)
			return fmt.Errorf("failed to stop audio capture: %w", err)
		}
	}

	// Wait for transcription loop to finish (with timeout)
	if a.transcriptionDone != nil {
		select {
		case <-a.transcriptionDone:
			logging.Debug("Transcription loop finished")
		case <-time.After(2 * time.Second):
			logging.Warn("Transcription loop did not finish in time")
		}
	}

	// Process remaining audio in background to not block UI
	go func() {
		a.processRemainingAudio()
		logging.Info("Recording stopped, total segments: %d", len(a.segments))
	}()

	return nil
}

// onAudioData handles incoming audio data
func (a *App) onAudioData(samples []float32) {
	a.bufferMu.Lock()
	a.audioBuffer = append(a.audioBuffer, samples...)
	a.bufferMu.Unlock()

	// Calculate audio level for visualization
	var sum float32
	for _, s := range samples {
		if s < 0 {
			sum -= s
		} else {
			sum += s
		}
	}
	level := sum / float32(len(samples))

	if a.program != nil {
		a.program.Send(ui.AudioLevelMsg{Level: level * 10}) // Scale for visibility
	}
}

// transcriptionLoop periodically transcribes accumulated audio
func (a *App) transcriptionLoop() {
	defer close(a.transcriptionDone)

	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-a.stopTranscription:
			logging.Debug("Transcription loop received stop signal")
			return
		case <-ticker.C:
			a.processAudioBuffer()
		}
	}
}

// processAudioBuffer transcribes the current audio buffer
func (a *App) processAudioBuffer() {
	a.bufferMu.Lock()
	if len(a.audioBuffer) < audio.SampleRate*3 { // Need at least 3 seconds
		a.bufferMu.Unlock()
		return
	}

	// Copy buffer
	audioData := make([]float32, len(a.audioBuffer))
	copy(audioData, a.audioBuffer)

	// Keep last 2 seconds for context
	overlapSamples := audio.SampleRate * 2
	if len(a.audioBuffer) > overlapSamples {
		a.audioBuffer = a.audioBuffer[len(a.audioBuffer)-overlapSamples:]
	} else {
		a.audioBuffer = a.audioBuffer[:0]
	}
	a.bufferMu.Unlock()

	logging.Debug("Processing audio buffer: %d samples", len(audioData))

	// Transcribe
	segments, err := a.whisper.TranscribeCLI(audioData)
	if err != nil {
		logging.Error("Transcription failed: %v", err)
		if a.program != nil {
			a.program.Send(ui.ErrorMsg{Error: err})
		}
		return
	}

	// Send segments to UI
	for _, seg := range segments {
		a.segments = append(a.segments, seg)
		logging.Debug("New segment: %s", seg.Text)
		if a.program != nil {
			a.program.Send(ui.NewSegmentMsg{Segment: seg})
		}
	}
}

// processRemainingAudio transcribes any remaining audio in the buffer
func (a *App) processRemainingAudio() {
	a.bufferMu.Lock()
	if len(a.audioBuffer) < audio.SampleRate { // Need at least 1 second
		a.bufferMu.Unlock()
		return
	}

	audioData := make([]float32, len(a.audioBuffer))
	copy(audioData, a.audioBuffer)
	a.audioBuffer = a.audioBuffer[:0]
	a.bufferMu.Unlock()

	segments, err := a.whisper.TranscribeCLI(audioData)
	if err != nil {
		if a.program != nil {
			a.program.Send(ui.ErrorMsg{Error: err})
		}
		return
	}

	for _, seg := range segments {
		a.segments = append(a.segments, seg)
		if a.program != nil {
			a.program.Send(ui.NewSegmentMsg{Segment: seg})
		}
	}
}

// saveTranscript saves the transcript to a file
func (a *App) saveTranscript(filename string) error {
	path := filepath.Join(outputDir, filename)

	f, err := os.Create(path)
	if err != nil {
		return fmt.Errorf("failed to create file: %w", err)
	}
	defer f.Close()

	// Write header
	fmt.Fprintf(f, "Rekord Meeting Transcript\n")
	fmt.Fprintf(f, "Generated: %s\n", time.Now().Format(time.RFC1123))
	fmt.Fprintf(f, "Device: %s\n", deviceName)
	fmt.Fprintf(f, "Model: %s\n", modelPath)
	fmt.Fprintf(f, "----------------------------------------\n\n")

	// Write segments
	for _, seg := range a.segments {
		timestamp := seg.Timestamp.Format("15:04:05")
		fmt.Fprintf(f, "[%s] %s\n", timestamp, seg.Text)
	}

	return nil
}
