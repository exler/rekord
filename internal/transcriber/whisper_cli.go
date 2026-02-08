// Package transcriber provides local speech-to-text transcription using whisper.cpp CLI
package transcriber

import (
	"bufio"
	"bytes"
	"encoding/binary"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/exler/rekord/internal/logging"
)

// WhisperCLI wraps the whisper.cpp command-line tool
type WhisperCLI struct {
	modelPath   string
	whisperPath string
}

// NewWhisperCLI creates a new WhisperCLI instance
func NewWhisperCLI(modelPath string) (*WhisperCLI, error) {
	// Find whisper executable
	whisperPath := findWhisperExecutable()
	if whisperPath == "" {
		return nil, fmt.Errorf("whisper.cpp executable not found. Please install whisper.cpp or set WHISPER_PATH")
	}

	return &WhisperCLI{
		modelPath:   modelPath,
		whisperPath: whisperPath,
	}, nil
}

// findWhisperExecutable searches for the whisper executable
func findWhisperExecutable() string {
	// Check environment variable first
	if path := os.Getenv("WHISPER_PATH"); path != "" {
		if _, err := os.Stat(path); err == nil {
			return path
		}
	}

	// Common names for whisper.cpp executable
	names := []string{"whisper", "whisper-cli", "whisper-cpp"}

	// Check in PATH
	for _, name := range names {
		if path, err := exec.LookPath(name); err == nil {
			return path
		}
	}

	// Check common installation locations
	locations := []string{
		"/usr/local/bin",
		"/usr/bin",
		filepath.Join(os.Getenv("HOME"), ".local/bin"),
		filepath.Join(os.Getenv("HOME"), "whisper.cpp"),
		"./whisper.cpp",
	}

	for _, loc := range locations {
		for _, name := range names {
			path := filepath.Join(loc, name)
			if _, err := os.Stat(path); err == nil {
				return path
			}
		}
	}

	return ""
}

// TranscribeCLI transcribes audio using whisper.cpp CLI and returns segments
func (w *WhisperCLI) TranscribeCLI(samples []float32) ([]Segment, error) {
	// Create temporary WAV file
	tmpFile, err := os.CreateTemp("", "rekord-*.wav")
	if err != nil {
		return nil, fmt.Errorf("failed to create temp file: %w", err)
	}
	tmpPath := tmpFile.Name()
	defer os.Remove(tmpPath)

	// Write WAV file
	if err := writeWAV(tmpFile, samples, 16000); err != nil {
		tmpFile.Close()
		return nil, fmt.Errorf("failed to write WAV file: %w", err)
	}
	tmpFile.Close()

	logging.Debug("Running whisper on %s (%d samples)", tmpPath, len(samples))

	// Run whisper.cpp with output to stdout only (no progress)
	cmd := exec.Command(w.whisperPath,
		"-m", w.modelPath,
		"-f", tmpPath,
		"-l", "en",
		"--no-prints", // Suppress all prints except transcript
		"--print-progress", "false",
	)

	// Capture stdout for transcript, redirect stderr to log file
	var stdout bytes.Buffer
	cmd.Stdout = &stdout

	// Redirect stderr to log file
	if logFile := logging.GetLogFile(); logFile != nil {
		cmd.Stderr = logFile
	} else {
		cmd.Stderr = io.Discard
	}

	err = cmd.Run()
	if err != nil {
		logging.Error("Whisper failed: %v", err)
		return nil, fmt.Errorf("whisper failed: %w", err)
	}

	// Parse output - only the transcript text
	output := stdout.String()
	logging.Debug("Whisper output: %s", output)

	segments := parseWhisperOutput(output)
	logging.Info("Transcribed %d segments", len(segments))

	return segments, nil
}

// writeWAV writes audio samples to a WAV file
func writeWAV(f *os.File, samples []float32, sampleRate int) error {
	// Convert float32 to int16
	int16Samples := make([]int16, len(samples))
	for i, s := range samples {
		// Clamp and convert
		if s > 1.0 {
			s = 1.0
		} else if s < -1.0 {
			s = -1.0
		}
		int16Samples[i] = int16(s * 32767)
	}

	// Write WAV header
	var buf bytes.Buffer

	// RIFF header
	buf.WriteString("RIFF")
	dataSize := len(int16Samples) * 2
	binary.Write(&buf, binary.LittleEndian, uint32(36+dataSize)) // File size - 8
	buf.WriteString("WAVE")

	// fmt chunk
	buf.WriteString("fmt ")
	binary.Write(&buf, binary.LittleEndian, uint32(16))           // Chunk size
	binary.Write(&buf, binary.LittleEndian, uint16(1))            // Audio format (PCM)
	binary.Write(&buf, binary.LittleEndian, uint16(1))            // Num channels
	binary.Write(&buf, binary.LittleEndian, uint32(sampleRate))   // Sample rate
	binary.Write(&buf, binary.LittleEndian, uint32(sampleRate*2)) // Byte rate
	binary.Write(&buf, binary.LittleEndian, uint16(2))            // Block align
	binary.Write(&buf, binary.LittleEndian, uint16(16))           // Bits per sample

	// data chunk
	buf.WriteString("data")
	binary.Write(&buf, binary.LittleEndian, uint32(dataSize))

	// Write header
	if _, err := f.Write(buf.Bytes()); err != nil {
		return err
	}

	// Write samples
	return binary.Write(f, binary.LittleEndian, int16Samples)
}

// parseWhisperOutput parses whisper.cpp output into segments
func parseWhisperOutput(output string) []Segment {
	var segments []Segment

	// Pattern for timestamps: [00:00:00.000 --> 00:00:05.000]
	timestampPattern := regexp.MustCompile(`\[(\d+:\d+:\d+\.\d+) --> (\d+:\d+:\d+\.\d+)\]\s*(.*)`)

	// Patterns to filter out whisper.cpp log/info lines
	skipPatterns := []*regexp.Regexp{
		regexp.MustCompile(`^whisper_`),            // whisper internal logs
		regexp.MustCompile(`^main:`),               // main function logs
		regexp.MustCompile(`^\s*$`),                // empty lines
		regexp.MustCompile(`^system_info:`),        // system info
		regexp.MustCompile(`^sampling:`),           // sampling info
		regexp.MustCompile(`^beam_search:`),        // beam search info
		regexp.MustCompile(`^output_`),             // output file info
		regexp.MustCompile(`^log_mel_spectrogram`), // mel spectrogram
		regexp.MustCompile(`^encode:`),             // encode info
		regexp.MustCompile(`^decode:`),             // decode info
		regexp.MustCompile(`^run:`),                // run info
		regexp.MustCompile(`^model:`),              // model info
		regexp.MustCompile(`^\[BLANK_AUDIO\]`),     // blank audio marker
		regexp.MustCompile(`^processing audio`),    // processing info
		regexp.MustCompile(`^loading model`),       // loading info
		regexp.MustCompile(`^energy:`),             // energy info
		regexp.MustCompile(`^speed:`),              // speed info
		regexp.MustCompile(`^total`),               // total info
		regexp.MustCompile(`^file:`),               // file info
		regexp.MustCompile(`^n_`),                  // n_ parameters
		regexp.MustCompile(`^ctx:`),                // context info
		regexp.MustCompile(`^params:`),             // params info
		regexp.MustCompile(`^initializing coreml`), // coreml info
		regexp.MustCompile(`^coreml_`),             // coreml logs
		regexp.MustCompile(`^cuda`),                // cuda logs
		regexp.MustCompile(`^using \d+ threads`),   // thread info
		regexp.MustCompile(`^fallback`),            // fallback info
		regexp.MustCompile(`^seeking`),             // seeking info
	}

	scanner := bufio.NewScanner(strings.NewReader(output))
	for scanner.Scan() {
		line := scanner.Text()
		trimmedLine := strings.TrimSpace(line)

		if trimmedLine == "" {
			continue
		}

		// Check if line should be skipped
		skip := false
		for _, pattern := range skipPatterns {
			if pattern.MatchString(strings.ToLower(trimmedLine)) {
				skip = true
				break
			}
		}
		if skip {
			continue
		}

		matches := timestampPattern.FindStringSubmatch(line)
		if len(matches) == 4 {
			startTime := parseTimestamp(matches[1])
			endTime := parseTimestamp(matches[2])
			text := strings.TrimSpace(matches[3])

			if text != "" && text != "[BLANK_AUDIO]" {
				segments = append(segments, Segment{
					Text:      text,
					StartTime: startTime,
					EndTime:   endTime,
					Timestamp: time.Now(),
				})
			}
		} else {
			// Plain text without timestamps - only include if it looks like actual transcript
			// Must not start with special characters or look like a log line
			if !strings.HasPrefix(trimmedLine, "[") &&
				!strings.HasPrefix(trimmedLine, "#") &&
				!strings.HasPrefix(trimmedLine, "=") &&
				!strings.Contains(trimmedLine, ":") &&
				len(trimmedLine) > 1 {
				segments = append(segments, Segment{
					Text:      trimmedLine,
					Timestamp: time.Now(),
				})
			}
		}
	}

	return segments
}

// parseTimestamp parses a timestamp string into a Duration
func parseTimestamp(ts string) time.Duration {
	parts := strings.Split(ts, ":")
	if len(parts) != 3 {
		return 0
	}

	hours, _ := strconv.Atoi(parts[0])
	minutes, _ := strconv.Atoi(parts[1])

	secParts := strings.Split(parts[2], ".")
	seconds, _ := strconv.Atoi(secParts[0])
	milliseconds := 0
	if len(secParts) > 1 {
		milliseconds, _ = strconv.Atoi(secParts[1])
	}

	return time.Duration(hours)*time.Hour +
		time.Duration(minutes)*time.Minute +
		time.Duration(seconds)*time.Second +
		time.Duration(milliseconds)*time.Millisecond
}

// Close is a no-op for CLI wrapper
func (w *WhisperCLI) Close() error {
	return nil
}
