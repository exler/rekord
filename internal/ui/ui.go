// Package ui provides the TUI interface for rekord
package ui

import (
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/help"
	"github.com/charmbracelet/bubbles/key"
	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/exler/rekord/internal/transcriber"
)

// Styles
var (
	titleStyle = lipgloss.NewStyle().
			Bold(true).
			Foreground(lipgloss.Color("#FF6B6B")).
			Background(lipgloss.Color("#1A1A2E")).
			Padding(0, 1)

	statusStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("#4ECDC4")).
			Padding(0, 1)

	recordingStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("#FF6B6B")).
			Bold(true).
			Blink(true)

	stoppedStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("#95A5A6"))

	transcriptStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("#ECF0F1")).
			Padding(1, 2)

	timestampStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("#7F8C8D")).
			Width(12)

	helpStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("#7F8C8D")).
			Padding(1, 0)

	borderStyle = lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(lipgloss.Color("#4ECDC4")).
			Padding(0, 1)

	audioLevelStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("#2ECC71"))
)

// Bar width for audio level meter
const barWidth = 20

// KeyMap defines keyboard shortcuts
type KeyMap struct {
	Start key.Binding
	Stop  key.Binding
	Save  key.Binding
	Clear key.Binding
	Quit  key.Binding
	Up    key.Binding
	Down  key.Binding
	Help  key.Binding
}

// DefaultKeyMap returns the default key bindings
func DefaultKeyMap() KeyMap {
	return KeyMap{
		Start: key.NewBinding(
			key.WithKeys("s", "enter"),
			key.WithHelp("s/enter", "start recording"),
		),
		Stop: key.NewBinding(
			key.WithKeys("s", "enter"),
			key.WithHelp("s/enter", "stop recording"),
		),
		Save: key.NewBinding(
			key.WithKeys("ctrl+s"),
			key.WithHelp("ctrl+s", "save transcript"),
		),
		Clear: key.NewBinding(
			key.WithKeys("c"),
			key.WithHelp("c", "clear transcript"),
		),
		Quit: key.NewBinding(
			key.WithKeys("q", "ctrl+c"),
			key.WithHelp("q", "quit"),
		),
		Up: key.NewBinding(
			key.WithKeys("up", "k"),
			key.WithHelp("↑/k", "scroll up"),
		),
		Down: key.NewBinding(
			key.WithKeys("down", "j"),
			key.WithHelp("↓/j", "scroll down"),
		),
		Help: key.NewBinding(
			key.WithKeys("?"),
			key.WithHelp("?", "toggle help"),
		),
	}
}

// ShortHelp returns keybindings for the short help view
func (k KeyMap) ShortHelp() []key.Binding {
	return []key.Binding{k.Start, k.Save, k.Clear, k.Quit, k.Help}
}

// FullHelp returns keybindings for the full help view
func (k KeyMap) FullHelp() [][]key.Binding {
	return [][]key.Binding{
		{k.Start, k.Stop},
		{k.Save, k.Clear},
		{k.Up, k.Down},
		{k.Quit, k.Help},
	}
}

// Model represents the application state
type Model struct {
	// State
	isRecording bool
	segments    []transcriber.Segment
	audioLevel  float32
	startTime   time.Time
	error       string
	modelLoaded bool
	modelPath   string
	deviceName  string

	// Components
	viewport viewport.Model
	spinner  spinner.Model
	help     help.Model
	keys     KeyMap

	// Dimensions
	width  int
	height int

	// Callbacks
	onStart func() error
	onStop  func() error
	onSave  func(string) error
}

// NewSegmentMsg is sent when a new segment is transcribed
type NewSegmentMsg struct {
	Segment transcriber.Segment
}

// AudioLevelMsg is sent with audio level updates
type AudioLevelMsg struct {
	Level float32
}

// ErrorMsg is sent when an error occurs
type ErrorMsg struct {
	Error error
}

// ModelLoadedMsg is sent when the model is loaded
type ModelLoadedMsg struct{}

// New creates a new UI model
func New(modelPath, deviceName string) Model {
	s := spinner.New()
	s.Spinner = spinner.Dot
	s.Style = lipgloss.NewStyle().Foreground(lipgloss.Color("#FF6B6B"))

	h := help.New()
	h.ShowAll = false

	vp := viewport.New(80, 20)
	vp.Style = transcriptStyle

	return Model{
		spinner:    s,
		help:       h,
		keys:       DefaultKeyMap(),
		viewport:   vp,
		segments:   make([]transcriber.Segment, 0),
		modelPath:  modelPath,
		deviceName: deviceName,
	}
}

// SetCallbacks sets the recording callbacks
func (m *Model) SetCallbacks(onStart, onStop func() error, onSave func(string) error) {
	m.onStart = onStart
	m.onStop = onStop
	m.onSave = onSave
}

// Init initializes the model
func (m Model) Init() tea.Cmd {
	return tea.Batch(m.spinner.Tick, tea.EnterAltScreen)
}

// Update handles messages
func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	var cmds []tea.Cmd

	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		m.viewport.Width = msg.Width - 4
		m.viewport.Height = msg.Height - 10
		m.help.Width = msg.Width

	case tea.KeyMsg:
		switch {
		case key.Matches(msg, m.keys.Quit):
			if m.isRecording && m.onStop != nil {
				m.onStop()
			}
			return m, tea.Quit

		case key.Matches(msg, m.keys.Start) && !m.isRecording:
			m.isRecording = true
			m.startTime = time.Now()
			m.error = ""
			if m.onStart != nil {
				if err := m.onStart(); err != nil {
					m.error = err.Error()
					m.isRecording = false
				}
			}
			return m, m.spinner.Tick

		case key.Matches(msg, m.keys.Stop) && m.isRecording:
			m.isRecording = false
			if m.onStop != nil {
				if err := m.onStop(); err != nil {
					m.error = err.Error()
				}
			}
			return m, nil

		case key.Matches(msg, m.keys.Save):
			if m.onSave != nil {
				filename := fmt.Sprintf("transcript_%s.txt", time.Now().Format("2006-01-02_15-04-05"))
				if err := m.onSave(filename); err != nil {
					m.error = err.Error()
				}
			}
			return m, nil

		case key.Matches(msg, m.keys.Clear):
			m.segments = m.segments[:0]
			m.viewport.SetContent("")
			return m, nil

		case key.Matches(msg, m.keys.Help):
			m.help.ShowAll = !m.help.ShowAll
			return m, nil
		}

	case NewSegmentMsg:
		m.segments = append(m.segments, msg.Segment)
		m.viewport.SetContent(m.renderTranscript())
		m.viewport.GotoBottom()
		return m, nil

	case AudioLevelMsg:
		m.audioLevel = msg.Level
		return m, nil

	case ErrorMsg:
		m.error = msg.Error.Error()
		return m, nil

	case ModelLoadedMsg:
		m.modelLoaded = true
		return m, nil

	case spinner.TickMsg:
		if m.isRecording {
			var cmd tea.Cmd
			m.spinner, cmd = m.spinner.Update(msg)
			cmds = append(cmds, cmd)
		}
	}

	// Handle viewport scrolling
	var cmd tea.Cmd
	m.viewport, cmd = m.viewport.Update(msg)
	cmds = append(cmds, cmd)

	return m, tea.Batch(cmds...)
}

// View renders the UI
func (m Model) View() string {
	if m.width == 0 {
		return "Loading..."
	}

	var b strings.Builder

	// Title
	title := titleStyle.Render(" REKORD - Meeting Transcriber ")
	b.WriteString(title)
	b.WriteString("\n\n")

	// Status bar
	var status string
	if m.isRecording {
		duration := time.Since(m.startTime).Round(time.Second)
		status = fmt.Sprintf("%s Recording... %s | Audio: %s",
			m.spinner.View(),
			duration.String(),
			m.renderAudioLevel(),
		)
		status = recordingStyle.Render("● REC ") + statusStyle.Render(status)
	} else {
		status = stoppedStyle.Render("○ STOPPED - Press 's' to start recording")
	}
	b.WriteString(statusStyle.Render(status))
	b.WriteString("\n")

	// Device info
	deviceInfo := fmt.Sprintf("Device: %s | Model: %s", m.deviceName, m.modelPath)
	b.WriteString(lipgloss.NewStyle().Foreground(lipgloss.Color("#7F8C8D")).Render(deviceInfo))
	b.WriteString("\n\n")

	// Error display
	if m.error != "" {
		errorStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("#E74C3C")).Bold(true)
		b.WriteString(errorStyle.Render("Error: " + m.error))
		b.WriteString("\n\n")
	}

	// Transcript viewport
	b.WriteString(borderStyle.Render(m.viewport.View()))
	b.WriteString("\n\n")

	// Help
	b.WriteString(helpStyle.Render(m.help.View(m.keys)))

	return b.String()
}

// renderTranscript renders all transcript segments
func (m Model) renderTranscript() string {
	if len(m.segments) == 0 {
		return lipgloss.NewStyle().
			Foreground(lipgloss.Color("#7F8C8D")).
			Italic(true).
			Render("No transcription yet. Start recording to begin...")
	}

	var b strings.Builder
	for _, seg := range m.segments {
		timestamp := timestampStyle.Render(seg.Timestamp.Format("15:04:05"))
		text := seg.Text
		fmt.Fprintf(&b, "%s %s\n", timestamp, text)
	}
	return b.String()
}

// renderAudioLevel renders an audio level meter
func (m Model) renderAudioLevel() string {
	level := int(m.audioLevel * barWidth)
	level = min(max(level, 0), barWidth) // Clamp level to [0, barWidth]
	bar := strings.Repeat("█", level) + strings.Repeat("░", barWidth-level)
	return audioLevelStyle.Render(bar)
}

// AddSegment adds a new transcript segment (for external use)
func (m *Model) AddSegment(seg transcriber.Segment) {
	m.segments = append(m.segments, seg)
	m.viewport.SetContent(m.renderTranscript())
	m.viewport.GotoBottom()
}
