# Overview
Rekord is a Go TUI app for real-time meeting transcription. It captures system audio (and optionally microphone input), runs local speech-to-text via `whisper.cpp`, and shows live transcripts with timestamps and audio-level visualization.

## Architecture
- `cmd/rekord/main.go` wires the app: parses flags, selects audio devices, initializes logging, sets up the UI, and orchestrates capture + transcription loops.
- Audio capture is handled by `internal/audio`, which shells out to PulseAudio/PipeWire (`parec`) on Linux and to a compiled Swift helper using ScreenCaptureKit on macOS (13+). Both emit raw float32-LE samples to the app callback.
- Transcription is handled by `internal/transcriber`, which buffers samples and invokes the whisper CLI to produce segments.
- The TUI is in `internal/ui` using Bubble Tea, receiving messages for new segments, audio levels, and errors.
- Logs are managed via `internal/logging`.
- GitHub Actions release workflow builds a linux amd64 binary.

## Project Structure
- `cmd/rekord/`: Application entrypoint.
- `internal/audio/`: Audio device discovery and capture (PulseAudio/PipeWire).
- `internal/transcriber/`: Whisper CLI wrapper, segmentation, model handling.
- `internal/ui/`: Bubble Tea TUI views and messages.
- `internal/logging/`: File logging setup and helpers.

## Dev Commands
- Build: `go build -o rekord ./cmd/rekord`
- Run: `go run cmd/rekord/main.go`
- Test: `go test ./...`
- Static analysis: `staticcheck ./...` and `go vet ./...`

## Guidelines

* Update the AGENTS.md whenever it feels appropriate.
* Do not create new documentation files unless explicitly asked to.
