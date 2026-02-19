//go:build linux

package audio

import (
	"errors"
	"fmt"
	"os/exec"
	"strings"
)

// ListMonitorSources returns available monitor sources for capturing system audio.
// On Linux this queries PulseAudio/PipeWire via pactl.
func ListMonitorSources() ([]MonitorSource, error) {
	cmd := exec.Command("pactl", "list", "sources", "short")
	output, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("failed to list PulseAudio sources: %w", err)
	}

	var sources []MonitorSource
	lines := strings.Split(string(output), "\n")
	for _, line := range lines {
		if line == "" {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) >= 2 {
			name := fields[1]
			isMonitor := strings.Contains(name, ".monitor")
			isInput := !isMonitor && (strings.Contains(name, "input") ||
				strings.Contains(name, "mic") ||
				strings.Contains(name, "Mic") ||
				strings.Contains(name, "capture"))
			sources = append(sources, MonitorSource{
				Name:        name,
				Description: name,
				IsMonitor:   isMonitor,
				IsInput:     isInput || !isMonitor,
			})
		}
	}

	return sources, nil
}

// GetDefaultMonitorSource returns the default output monitor source name.
func GetDefaultMonitorSource() (string, error) {
	cmd := exec.Command("pactl", "get-default-sink")
	output, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("failed to get default sink: %w", err)
	}

	sink := strings.TrimSpace(string(output))
	if sink == "" {
		return "", errors.New("no default sink found")
	}

	return sink + ".monitor", nil
}

// GetDefaultInputSource returns the default input (microphone) source name.
func GetDefaultInputSource() (string, error) {
	cmd := exec.Command("pactl", "get-default-source")
	output, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("failed to get default source: %w", err)
	}

	source := strings.TrimSpace(string(output))
	if source == "" {
		return "", errors.New("no default source found")
	}

	// Don't return a monitor as the input source
	if strings.Contains(source, ".monitor") {
		sources, err := ListMonitorSources()
		if err != nil {
			return "", err
		}
		for _, s := range sources {
			if s.IsInput && !s.IsMonitor {
				return s.Name, nil
			}
		}
		return "", errors.New("no input source found")
	}

	return source, nil
}

// startSource starts capturing from a single PulseAudio/PipeWire source using parec.
func (c *MultiCapture) startSource(source *Source) error {
	source.stopCh = make(chan struct{})

	ctx, cancel := mustContext()
	source.cancel = cancel

	source.cmd = exec.CommandContext(ctx, "parec",
		"--format=float32le",
		"--rate=16000",
		"--channels=1",
		"-d", source.deviceName,
	)

	stdout, err := source.cmd.StdoutPipe()
	if err != nil {
		cancel()
		return fmt.Errorf("failed to create stdout pipe: %w", err)
	}

	if err := source.cmd.Start(); err != nil {
		cancel()
		return fmt.Errorf("failed to start parec: %w", err)
	}

	source.wg.Add(1)
	go c.readAudioLoop(source, stdout)

	return nil
}
