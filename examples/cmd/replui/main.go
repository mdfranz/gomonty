// Command replui is a minimal bubbletea REPL for interactively exercising
// gomonty — a lighter way to test scripts and telemetry than wiring
// through a full host application. State persists across inputs like a
// real Python REPL (via monty.Repl.FeedRun), one external function
// (host_time) is registered so callback spans have something to exercise,
// and every FeedRun call is wired with monty.SlogHandler logging to a file
// (not stdout, since the TUI owns the terminal) — tail -f it in another
// pane to watch telemetry live.
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
	quitting  bool
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
			if code == "" {
				return m, nil
			}
			m.entries = append(m.entries, m.run(code))
			if len(m.entries) > maxHistoryEntries {
				m.entries = m.entries[len(m.entries)-maxHistoryEntries:]
			}
			return m, nil
		}
	}
	var cmd tea.Cmd
	m.textInput, cmd = m.textInput.Update(msg)
	return m, cmd
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
	b.WriteString("gomonty repl  (Ctrl+C to quit)\n")
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
	logPath := "replui-telemetry.log"
	logFile, err := os.Create(logPath)
	if err != nil {
		log.Fatal(err)
	}
	defer logFile.Close()
	logger := slog.New(slog.NewTextHandler(logFile, &slog.HandlerOptions{Level: slog.LevelDebug}))

	repl, err := monty.NewRepl(monty.ReplOptions{ScriptName: "replui.py"})
	if err != nil {
		log.Fatal(err)
	}

	if _, err := tea.NewProgram(newModel(repl, logger, logPath)).Run(); err != nil {
		log.Fatal(err)
	}
}
