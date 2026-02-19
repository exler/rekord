// Package audio provides system audio capture functionality.
// On Linux it uses PulseAudio/PipeWire (parec/pactl).
// On macOS it uses ffmpeg with AVFoundation; system audio capture requires
// a virtual loopback driver such as BlackHole (https://github.com/ExistentialAudio/BlackHole).
package audio

import (
	"context"
	"errors"
	"fmt"
	"math"
	"os/exec"
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

// MonitorSource represents an audio source available for capture
type MonitorSource struct {
	Name        string
	Description string
	IsMonitor   bool
	IsInput     bool
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

func bytesToFloat32(b []byte) float32 {
	bits := uint32(b[0]) | uint32(b[1])<<8 | uint32(b[2])<<16 | uint32(b[3])<<24
	return math.Float32frombits(bits)
}

// readAudioLoop reads raw float32-LE samples from a ReadCloser and calls onAudio.
// It is shared by all platform implementations.
func (c *MultiCapture) readAudioLoop(source *Source, stdout interface{ Read([]byte) (int, error) }) {
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

	// Cancel the context to kill the capture process
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

// mustContext is a helper used by platform startSource implementations.
func mustContext() (context.Context, context.CancelFunc) {
	return context.WithCancel(context.Background())
}
