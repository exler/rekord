// Package audio provides system audio capture functionality using PulseAudio/PipeWire
package audio

import (
	"context"
	"errors"
	"fmt"
	"math"
	"os/exec"
	"strings"
	"sync"
)

const (
	SampleRate   = 16000 // Whisper expects 16kHz
	Channels     = 1     // Mono audio
	FrameSize    = 480   // 30ms frames at 16kHz
	BufferFrames = 10    // Buffer multiple frames
)

// Source represents a single audio source (monitor or microphone)
type Source struct {
	cmd        *exec.Cmd
	cancel     context.CancelFunc
	deviceName string
	stopCh     chan struct{}
	wg         sync.WaitGroup
}

// MultiCapture handles audio capture from multiple sources (system + microphone)
type MultiCapture struct {
	sources   []*Source
	mu        sync.Mutex
	isRunning bool
	onAudio   func([]float32)
}

// Capture handles audio capture from system audio (single source, kept for compatibility)
type Capture = MultiCapture

// MonitorSource represents a PulseAudio/PipeWire monitor source
type MonitorSource struct {
	Name        string
	Description string
	IsMonitor   bool
	IsInput     bool
}

// ListMonitorSources returns available monitor sources for capturing system audio
func ListMonitorSources() ([]MonitorSource, error) {
	// Use pactl to list sources and find monitors
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
			// Input sources typically contain "input" or don't have ".monitor"
			isInput := !isMonitor && (strings.Contains(name, "input") ||
				strings.Contains(name, "mic") ||
				strings.Contains(name, "Mic") ||
				strings.Contains(name, "capture"))
			sources = append(sources, MonitorSource{
				Name:        name,
				Description: name,
				IsMonitor:   isMonitor,
				IsInput:     isInput || !isMonitor, // Non-monitors are typically inputs
			})
		}
	}

	return sources, nil
}

// GetDefaultMonitorSource returns the default output monitor source
func GetDefaultMonitorSource() (string, error) {
	// Get default sink and append .monitor
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

// GetDefaultInputSource returns the default input (microphone) source
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

	// Don't return if it's a monitor (we want actual input)
	if strings.Contains(source, ".monitor") {
		// Try to find an actual input source
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

// NewCapture creates a new audio capture instance with a single device
func NewCapture(deviceName string, onAudio func([]float32)) (*MultiCapture, error) {
	return NewMultiCapture([]string{deviceName}, onAudio)
}

// NewMultiCapture creates a new audio capture instance with multiple devices
func NewMultiCapture(deviceNames []string, onAudio func([]float32)) (*MultiCapture, error) {
	if len(deviceNames) == 0 {
		return nil, errors.New("at least one device name is required")
	}

	sources := make([]*Source, len(deviceNames))
	for i, name := range deviceNames {
		sources[i] = &Source{
			deviceName: name,
			stopCh:     make(chan struct{}),
		}
	}

	c := &MultiCapture{
		sources: sources,
		onAudio: onAudio,
	}

	return c, nil
}

// Start begins capturing audio from all sources
func (c *MultiCapture) Start() error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.isRunning {
		return errors.New("capture already running")
	}

	// Start each source
	for _, source := range c.sources {
		if err := c.startSource(source); err != nil {
			// Stop any sources that were started
			c.stopAllSources()
			return fmt.Errorf("failed to start source %s: %w", source.deviceName, err)
		}
	}

	c.isRunning = true
	return nil
}

// startSource starts a single audio source
func (c *MultiCapture) startSource(source *Source) error {
	// Create a new stop channel
	source.stopCh = make(chan struct{})

	// Use parec for PulseAudio/PipeWire capture
	ctx, cancel := context.WithCancel(context.Background())
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

	// Start reading audio in a goroutine
	source.wg.Add(1)
	go func() {
		defer source.wg.Done()

		buffer := make([]byte, FrameSize*4) // 4 bytes per float32
		samples := make([]float32, FrameSize)

		for {
			select {
			case <-source.stopCh:
				return
			default:
				n, err := stdout.Read(buffer)
				if err != nil {
					return
				}

				// Convert bytes to float32
				numSamples := n / 4
				for i := 0; i < numSamples; i++ {
					samples[i] = bytesToFloat32(buffer[i*4 : (i+1)*4])
				}

				if c.onAudio != nil {
					c.onAudio(samples[:numSamples])
				}
			}
		}
	}()

	return nil
}

func bytesToFloat32(b []byte) float32 {
	bits := uint32(b[0]) | uint32(b[1])<<8 | uint32(b[2])<<16 | uint32(b[3])<<24
	return math.Float32frombits(bits)
}

// stopAllSources stops all audio sources
func (c *MultiCapture) stopAllSources() {
	for _, source := range c.sources {
		c.stopSource(source)
	}
}

// stopSource stops a single audio source
func (c *MultiCapture) stopSource(source *Source) {
	// Signal stop
	select {
	case <-source.stopCh:
		// Already closed
	default:
		close(source.stopCh)
	}

	// Cancel the context to kill parec
	if source.cancel != nil {
		source.cancel()
	}

	// Wait for the goroutine to finish
	source.wg.Wait()

	// Wait for command to exit
	if source.cmd != nil && source.cmd.Process != nil {
		source.cmd.Wait()
	}
}

// Stop stops audio capture from all sources
func (c *MultiCapture) Stop() error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if !c.isRunning {
		return nil
	}

	c.isRunning = false
	c.stopAllSources()

	return nil
}

// Close releases all resources
func (c *MultiCapture) Close() error {
	return c.Stop()
}

// IsRunning returns whether capture is active
func (c *MultiCapture) IsRunning() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.isRunning
}

// GetDeviceNames returns the names of all devices being captured
func (c *MultiCapture) GetDeviceNames() []string {
	names := make([]string, len(c.sources))
	for i, s := range c.sources {
		names[i] = s.deviceName
	}
	return names
}
