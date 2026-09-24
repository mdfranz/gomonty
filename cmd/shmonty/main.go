// Command shmonty is a minimal bubbletea REPL for interactively exercising
// gomonty — a lighter way to test scripts and telemetry than wiring
// through a full host application. State persists across inputs like a
// real Python REPL (via monty.Repl.FeedRun), one external function
// (host_time) is registered so callback spans have something to exercise,
// and every FeedRun call is wired with monty.SlogHandler logging to a file
// (not stdout, since the TUI owns the terminal) — tail -f it in another
// pane to watch telemetry live. Up/Down recall previous inputs, like a
// normal shell history.
package main

import (
	"context"
	"fmt"
	"log"
	"log/slog"
	"os"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"

	monty "github.com/ewhauser/gomonty"
)

const maxHistoryEntries = 15

type entry struct {
	code   string
	output string
	result string
	isErr  bool
}

type model struct {
	textInput textinput.Model
	repl      *monty.Repl
	logger    *slog.Logger
	logPath   string
	entries   []entry

	// history/historyIdx/pendingInput implement Up/Down recall.
	// historyIdx == len(history) means "not currently navigating" (a fresh
	// line); pendingInput is what was being typed before the first Up
	// press, restored when Down navigates back past the end of history.
	history      []string
	historyIdx   int
	pendingInput string

	quitting bool
}

func newModel(repl *monty.Repl, logger *slog.Logger, logPath string) model {
	ti := textinput.New()
	ti.Placeholder = "40 + 2"
	ti.Focus()
	ti.CharLimit = 2000
	ti.Width = 70
	return model{textInput: ti, repl: repl, logger: logger, logPath: logPath}
}

func (m model) Init() tea.Cmd {
	return textinput.Blink
}

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch msg.Type {
		case tea.KeyCtrlC, tea.KeyEsc:
			m.quitting = true
			return m, tea.Quit
		case tea.KeyEnter:
			code := strings.TrimSpace(m.textInput.Value())
			m.textInput.SetValue("")
			m.historyIdx = len(m.history)
			m.pendingInput = ""
			if code == "" {
				return m, nil
			}
			m.entries = append(m.entries, m.run(code))
			if len(m.entries) > maxHistoryEntries {
				m.entries = m.entries[len(m.entries)-maxHistoryEntries:]
			}
			m.history = append(m.history, code)
			m.historyIdx = len(m.history)
			return m, nil
		case tea.KeyUp:
			return m.navigateHistory(-1), nil
		case tea.KeyDown:
			return m.navigateHistory(1), nil
		}
	}
	var cmd tea.Cmd
	m.textInput, cmd = m.textInput.Update(msg)
	return m, cmd
}

// navigateHistory moves historyIdx by delta (-1 for Up, +1 for Down),
// clamped to [0, len(history)], and updates the text input to match.
// Stepping to len(history) restores whatever was being typed before Up was
// first pressed.
func (m model) navigateHistory(delta int) model {
	if len(m.history) == 0 {
		return m
	}
	if m.historyIdx == len(m.history) && delta < 0 {
		m.pendingInput = m.textInput.Value()
	}
	next := m.historyIdx + delta
	if next < 0 {
		next = 0
	}
	if next > len(m.history) {
		next = len(m.history)
	}
	m.historyIdx = next
	if m.historyIdx == len(m.history) {
		m.textInput.SetValue(m.pendingInput)
	} else {
		m.textInput.SetValue(m.history[m.historyIdx])
	}
	m.textInput.CursorEnd()
	return m
}

func (m model) run(code string) entry {
	var stdout strings.Builder
	value, err := m.repl.FeedRun(context.Background(), code, monty.FeedOptions{
		Print:     monty.WriterPrintCallback(&stdout),
		Telemetry: monty.SlogHandler{Logger: m.logger},
		Functions: map[string]monty.ExternalFunction{
			"host_time": func(context.Context, monty.Call) (monty.Result, error) {
				return monty.Return(monty.String(time.Now().Format(time.RFC3339))), nil
			},
		},
	})
	if err != nil {
		return entry{code: code, output: stdout.String(), result: err.Error(), isErr: true}
	}
	return entry{code: code, output: stdout.String(), result: fmt.Sprintf("%v", value.Raw())}
}

func (m model) View() string {
	var b strings.Builder
	b.WriteString("shmonty  (Ctrl+C to quit, Up/Down for history)\n")
	fmt.Fprintf(&b, "telemetry log: %s\n\n", m.logPath)

	for _, e := range m.entries {
		fmt.Fprintf(&b, ">>> %s\n", e.code)
		if e.output != "" {
			b.WriteString(e.output)
			if !strings.HasSuffix(e.output, "\n") {
				b.WriteString("\n")
			}
		}
		if e.isErr {
			fmt.Fprintf(&b, "error: %s\n", e.result)
		} else {
			fmt.Fprintf(&b, "%s\n", e.result)
		}
		b.WriteString("\n")
	}

	b.WriteString(m.textInput.View())
	b.WriteString("\n")
	return b.String()
}

func main() {
	logPath := "shmonty-telemetry.log"
	logFile, err := os.Create(logPath)
	if err != nil {
		log.Fatal(err)
	}
	defer logFile.Close()
	logger := slog.New(slog.NewTextHandler(logFile, &slog.HandlerOptions{Level: slog.LevelDebug}))

	repl, err := monty.NewRepl(monty.ReplOptions{ScriptName: "shmonty.py"})
	if err != nil {
		log.Fatal(err)
	}

	if _, err := tea.NewProgram(newModel(repl, logger, logPath)).Run(); err != nil {
		log.Fatal(err)
	}
}
