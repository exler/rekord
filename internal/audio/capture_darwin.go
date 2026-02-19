//go:build darwin

// macOS audio capture:
//
//   System audio  — Apple ScreenCaptureKit (macOS 13+, no third-party driver
//                   required). The calling process / its parent terminal must
//                   have Screen Recording permission in
//                   System Settings > Privacy & Security > Screen Recording.
//
//   Microphone    — ffmpeg + AVFoundation.
//                   Install ffmpeg: brew install ffmpeg
//
// On first use rekord compiles the bundled Swift helper with swiftc (part of
// Xcode Command Line Tools) and caches the binary at
// ~/.cache/rekord/rekord-screencapture.

package audio

import (
	"bufio"
	_ "embed"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
)

//go:embed screencapture_helper.swift
var screenCaptureHelperSrc []byte

// screenCaptureKitDevice is the sentinel device name for ScreenCaptureKit
// system-audio capture throughout the codebase.
const screenCaptureKitDevice = "screencapturekit"

// avfAudioDeviceRe matches lines like: [AVFoundation indev @ ...] [2] MacBook Pro Microphone
var avfAudioDeviceRe = regexp.MustCompile(`\[(\d+)\]\s+(.+)$`)

// listAVFoundationAudioDevices returns real AVFoundation input devices (microphones).
func listAVFoundationAudioDevices() ([]MonitorSource, error) {
	cmd := exec.Command("ffmpeg",
		"-f", "avfoundation",
		"-list_devices", "true",
		"-i", "",
	)
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return nil, fmt.Errorf("failed to create stderr pipe: %w", err)
	}
	_ = cmd.Start()

	inAudioSection := false
	var sources []MonitorSource

	scanner := bufio.NewScanner(stderr)
	for scanner.Scan() {
		line := scanner.Text()
		if strings.Contains(line, "AVFoundation audio devices") {
			inAudioSection = true
			continue
		}
		if strings.Contains(line, "AVFoundation video devices") {
			inAudioSection = false
			continue
		}
		if !inAudioSection {
			continue
		}
		m := avfAudioDeviceRe.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		index := m[1]
		name := strings.TrimSpace(m[2])
		lower := strings.ToLower(name)
		// Skip virtual loopback drivers – ScreenCaptureKit handles system audio.
		if strings.Contains(lower, "blackhole") ||
			strings.Contains(lower, "soundflower") ||
			strings.Contains(lower, "loopback") {
			continue
		}
		sources = append(sources, MonitorSource{
			Name:        index,
			Description: name,
			IsMonitor:   false,
			IsInput:     true,
		})
	}
	_ = cmd.Wait()
	return sources, nil
}

// ListMonitorSources returns all available audio sources on macOS.
// System audio is represented as a single virtual ScreenCaptureKit source;
// real microphones come from AVFoundation via ffmpeg.
func ListMonitorSources() ([]MonitorSource, error) {
	sources := []MonitorSource{
		{
			Name:        screenCaptureKitDevice,
			Description: "System Audio (ScreenCaptureKit, macOS 13+)",
			IsMonitor:   true,
			IsInput:     false,
		},
	}
	mics, err := listAVFoundationAudioDevices()
	if err == nil {
		sources = append(sources, mics...)
	}
	return sources, nil
}

// GetDefaultMonitorSource returns the ScreenCaptureKit sentinel for system
// audio capture (no external driver required on macOS 13+).
func GetDefaultMonitorSource() (string, error) {
	return screenCaptureKitDevice, nil
}

// GetDefaultInputSource returns the AVFoundation index of the first real
// microphone input device.
func GetDefaultInputSource() (string, error) {
	sources, err := listAVFoundationAudioDevices()
	if err != nil {
		return "", err
	}
	for _, s := range sources {
		lower := strings.ToLower(s.Description)
		if strings.Contains(lower, "microphone") || strings.Contains(lower, "mic") {
			return s.Name, nil
		}
	}
	for _, s := range sources {
		if s.IsInput {
			return s.Name, nil
		}
	}
	return "", errors.New("no input audio device found (is ffmpeg installed? brew install ffmpeg)")
}

// screenCaptureHelperBin returns the path to the compiled Swift helper binary,
// compiling it from the embedded source if necessary.
func screenCaptureHelperBin() (string, error) {
	cacheDir, err := os.UserCacheDir()
	if err != nil {
		return "", fmt.Errorf("failed to get user cache dir: %w", err)
	}
	binDir := filepath.Join(cacheDir, "rekord")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		return "", fmt.Errorf("failed to create cache dir: %w", err)
	}
	binPath := filepath.Join(binDir, "rekord-screencapture")

	// Return cached binary if it already exists.
	if _, err := os.Stat(binPath); err == nil {
		return binPath, nil
	}

	// Write embedded Swift source to a temp file and compile it.
	srcFile, err := os.CreateTemp("", "rekord-screencapture-*.swift")
	if err != nil {
		return "", fmt.Errorf("failed to create temp source file: %w", err)
	}
	defer os.Remove(srcFile.Name())

	if _, err := srcFile.Write(screenCaptureHelperSrc); err != nil {
		srcFile.Close()
		return "", fmt.Errorf("failed to write Swift source: %w", err)
	}
	srcFile.Close()

	// swiftc is bundled with Xcode Command Line Tools.
	compileCmd := exec.Command("swiftc", "-O", "-o", binPath, srcFile.Name())
	if out, err := compileCmd.CombinedOutput(); err != nil {
		return "", fmt.Errorf("failed to compile screencapture helper (ensure Xcode CLT is installed: xcode-select --install): %w\n%s", err, out)
	}
	return binPath, nil
}

// startSource starts capturing audio from a single device.
// For the screenCaptureKitDevice sentinel it spawns the compiled Swift helper;
// for all other names it spawns ffmpeg with an AVFoundation device index.
func (c *MultiCapture) startSource(source *Source) error {
	source.stopCh = make(chan struct{})
	ctx, cancel := mustContext()
	source.cancel = cancel

	if source.deviceName == screenCaptureKitDevice {
		binPath, err := screenCaptureHelperBin()
		if err != nil {
			cancel()
			return fmt.Errorf("screencapturekit helper unavailable: %w", err)
		}
		source.cmd = exec.CommandContext(ctx, binPath)
	} else {
		// ffmpeg AVFoundation capture for microphone inputs.
		// Device name is the AVFoundation audio index, e.g. "0".
		avfInput := fmt.Sprintf("none:%s", source.deviceName)
		source.cmd = exec.CommandContext(ctx, "ffmpeg",
			"-f", "avfoundation",
			"-i", avfInput,
			"-ar", "16000",
			"-ac", "1",
			"-f", "f32le",
			"pipe:1",
		)
	}

	stdout, err := source.cmd.StdoutPipe()
	if err != nil {
		cancel()
		return fmt.Errorf("failed to create stdout pipe: %w", err)
	}
	if err := source.cmd.Start(); err != nil {
		cancel()
		if source.deviceName == screenCaptureKitDevice {
			return fmt.Errorf("failed to start screencapture helper: %w", err)
		}
		return fmt.Errorf("failed to start ffmpeg: %w", err)
	}

	source.wg.Add(1)
	go c.readAudioLoop(source, stdout)
	return nil
}
