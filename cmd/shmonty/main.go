// Command shmonty is a minimal bubbletea REPL for interactively exercising
// gomonty — a lighter way to test scripts and telemetry than wiring
// through a full host application. It runs in the terminal's alternate
// screen buffer (like vim/less): the screen clears on start and the
// original scrollback is restored on exit, so nothing it prints lingers
// afterward. State persists across inputs like a real Python REPL (via
// monty.Repl.FeedRun), one external function (host_time) is registered so
// callback spans have something to exercise, and typing /debug toggles
// monty.SlogHandler logging for subsequent FeedRun calls, shown in a
// dedicated pane along the top of the screen (roughly a quarter of the
// terminal height). /execute <path> reads a .py file (absolute or relative)
// and feeds its contents to the REPL as one call, exactly as if it had been
// typed in — variables and imports it defines carry over into later input.
// Tab completes the path argument to /execute, shell-style: an unambiguous
// match completes in full, a partial match extends to the longest common
// prefix, and a second Tab at that prefix lists the candidates. Up/Down
// recall previous inputs, like a normal shell history. Ctrl+R rewinds
// (bpython's term for it): pops the last input back into the prompt for
// editing and replays everything before it against a fresh interpreter, so
// a mistyped line can be fixed without restarting the whole session.
//
// Run as `shmonty <path>` instead of bare, it skips the REPL entirely: runs
// the file once — like `python script.py`, not /execute — printing print()
// output followed by the script's final value, then exits (non-zero on a
// read or execution error).
//
// If OTEL_EXPORTER_OTLP_ENDPOINT is set, telemetry exports via
// otelmonty.Handler to any standard OTLP backend (Logfire included) for
// every FeedRun/Run call instead of monty.SlogHandler — /debug becomes
// unavailable in that mode, since there's no local slog output to show.
// See telemetry_otel.go.
package main

import (
	"context"
	"fmt"
	"log"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"

	monty "github.com/ewhauser/gomonty"
)

const (
	maxHistoryEntries = 15
	// maxStoredLogLines bounds logLines' memory growth over a long session;
	// only the most recent logPaneHeight() of these are ever shown at once.
	maxStoredLogLines = 500
	minLogPaneHeight  = 3
	// defaultLogPaneHeight is used until the first tea.WindowSizeMsg arrives.
	defaultLogPaneHeight = 8
)

type entry struct {
	code   string
	output string
	result string
	isErr  bool
}

// logBuffer collects slog output as an io.Writer target so it can be pulled
// into the model as plain lines once run() returns, rather than writing
// straight to the terminal — nothing outside View() is allowed to touch the
// screen while bubbletea owns it.
type logBuffer struct {
	mu    sync.Mutex
	lines []string
}

func (b *logBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	for _, line := range strings.Split(strings.TrimRight(string(p), "\n"), "\n") {
		if line != "" {
			b.lines = append(b.lines, line)
		}
	}
	return len(p), nil
}

func (b *logBuffer) drain() []string {
	b.mu.Lock()
	defer b.mu.Unlock()
	lines := b.lines
	b.lines = nil
	return lines
}

type model struct {
	textInput textinput.Model
	repl      *monty.Repl
	logger    *slog.Logger
	logs      *logBuffer
	entries   []entry

	// history/historyIdx/pendingInput implement Up/Down recall.
	// historyIdx == len(history) means "not currently navigating" (a fresh
	// line); pendingInput is what was being typed before the first Up
	// press, restored when Down navigates back past the end of history.
	history      []string
	historyIdx   int
	pendingInput string

	// debugEnabled gates whether FeedRun gets a Telemetry handler at all,
	// and whether the log pane is shown. Off by default — toggled with
	// /debug. Meaningless once otelHandler is set: see otelHandler's doc.
	debugEnabled bool
	// otelHandler, when non-nil, replaces SlogHandler for every FeedRun
	// call unconditionally, ignoring debugEnabled entirely — set once at
	// startup from initOTelTelemetry (telemetry_otel.go) when
	// OTEL_EXPORTER_OTLP_ENDPOINT is configured. An OTel-exporting session
	// has nothing local to show, so there's no local pane for /debug to
	// toggle; /debug reports that instead of flipping debugEnabled.
	otelHandler monty.TelemetryHandler
	// logLines holds captured telemetry output, most recent last; only the
	// tail of it (sized by logPaneHeight) is ever rendered at once.
	logLines []string

	// width/height track the terminal size (via tea.WindowSizeMsg), used to
	// size the log pane to roughly a quarter of the screen.
	width, height int

	quitting bool
}

func newModel(repl *monty.Repl, logger *slog.Logger, logs *logBuffer) model {
	ti := textinput.New()
	ti.Placeholder = "40 + 2"
	ti.Focus()
	ti.CharLimit = 2000
	ti.Width = 70
	return model{textInput: ti, repl: repl, logger: logger, logs: logs}
}

// debugStatusEntry reports the new /debug state as a pseudo-entry so it
// shows up in the transcript like any other REPL interaction.
func (m model) debugStatusEntry() entry {
	state := "off"
	if m.debugEnabled {
		state = "on"
	}
	return entry{code: "/debug", result: fmt.Sprintf("telemetry logging %s", state)}
}

func (m model) Init() tea.Cmd {
	return textinput.Blink
}

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		return m, nil
	case tea.KeyMsg:
		switch msg.Type {
		case tea.KeyCtrlC, tea.KeyEsc:
			m.quitting = true
			return m, tea.Quit
		case tea.KeyCtrlD:
			// Matches real shell/Python REPL behavior: Ctrl-D only exits on
			// an empty line, so it can't accidentally nuke mid-typed input.
			if m.textInput.Value() == "" {
				m.quitting = true
				return m, tea.Quit
			}
			return m, nil
		case tea.KeyEnter:
			code := strings.TrimSpace(m.textInput.Value())
			m.textInput.SetValue("")
			m.historyIdx = len(m.history)
			m.pendingInput = ""
			if code == "" {
				return m, nil
			}
			if code == "/debug" {
				// Not Python code and not added to history — a slash
				// command toggling this session's own state.
				if m.otelHandler != nil {
					m.entries = append(m.entries, entry{
						code:   "/debug",
						result: "telemetry is exporting via OpenTelemetry (OTEL_EXPORTER_OTLP_ENDPOINT set); local /debug pane is unavailable in this mode",
					})
					return m, nil
				}
				m.debugEnabled = !m.debugEnabled
				m.entries = append(m.entries, m.debugStatusEntry())
				return m, nil
			}
			m.entries = append(m.entries, m.runInput(code))
			if len(m.entries) > maxHistoryEntries {
				m.entries = m.entries[len(m.entries)-maxHistoryEntries:]
			}
			m.logLines = append(m.logLines, m.logs.drain()...)
			if len(m.logLines) > maxStoredLogLines {
				m.logLines = m.logLines[len(m.logLines)-maxStoredLogLines:]
			}
			m.history = append(m.history, code)
			m.historyIdx = len(m.history)
			return m, nil
		case tea.KeyUp:
			return m.navigateHistory(-1), nil
		case tea.KeyDown:
			return m.navigateHistory(1), nil
		case tea.KeyTab:
			return m.completePath(), nil
		case tea.KeyCtrlR:
			return m.rewind(), nil
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

const (
	executeCmd    = "/execute"
	executePrefix = executeCmd + " "
)

// completePath implements shell-style Tab completion for /execute's path
// argument — the only place shmonty takes a filesystem path. Outside that
// context it's a no-op, so Tab never inserts a stray tab character into
// Python code. A single match completes in full (with a trailing "/" for a
// directory, so the next Tab can descend into it); multiple matches extend
// to their longest common prefix; and pressing Tab again once already at
// that prefix lists every candidate, like bash's double-Tab.
func (m model) completePath() model {
	value := m.textInput.Value()
	if !strings.HasPrefix(value, executePrefix) {
		return m
	}
	frag := value[len(executePrefix):]

	dir, base := filepath.Split(frag)
	searchDir := dir
	if searchDir == "" {
		searchDir = "."
	}
	entries, err := os.ReadDir(searchDir)
	if err != nil {
		return m
	}

	var matches []string
	for _, e := range entries {
		name := e.Name()
		if strings.HasPrefix(name, ".") && !strings.HasPrefix(base, ".") {
			continue // hidden entries only show once the fragment itself asks for them
		}
		if !strings.HasPrefix(name, base) {
			continue
		}
		if e.IsDir() {
			name += "/"
		}
		matches = append(matches, name)
	}
	if len(matches) == 0 {
		return m
	}
	sort.Strings(matches)

	common := commonPrefix(matches)
	if common != base {
		m.textInput.SetValue(executePrefix + dir + common)
		m.textInput.CursorEnd()
		return m
	}
	if len(matches) == 1 {
		return m // base already equals the one match in full
	}

	// Already at the longest common prefix with more than one candidate —
	// show them instead of completing further, like a shell's double-Tab.
	m.entries = append(m.entries, entry{code: value, result: strings.Join(matches, "  ")})
	if len(m.entries) > maxHistoryEntries {
		m.entries = m.entries[len(m.entries)-maxHistoryEntries:]
	}
	return m
}

// commonPrefix returns the longest string prefix shared by every element of
// strs ("" for an empty slice).
func commonPrefix(strs []string) string {
	if len(strs) == 0 {
		return ""
	}
	prefix := strs[0]
	for _, s := range strs[1:] {
		for !strings.HasPrefix(s, prefix) {
			prefix = prefix[:len(prefix)-1]
			if prefix == "" {
				return ""
			}
		}
	}
	return prefix
}

// logPaneHeight sizes the log pane to roughly a quarter of the terminal,
// falling back to a fixed default until the first WindowSizeMsg arrives.
func (m model) logPaneHeight() int {
	if m.height <= 0 {
		return defaultLogPaneHeight
	}
	h := m.height / 4
	if h < minLogPaneHeight {
		h = minLogPaneHeight
	}
	return h
}

// renderLogPane renders a fixed-height pane showing the most recent
// telemetry lines, padded with blank lines so the pane's height (and thus
// everything below it) stays stable as lines accumulate. Lines longer than
// the terminal width are wrapped, not cut off — the row budget (height) is
// what gets rationed across entries, newest first, not individual lines'
// content.
func (m model) renderLogPane() string {
	width := m.width
	if width <= 0 {
		width = 70
	}
	height := m.logPaneHeight()

	// Walk from newest to oldest, wrapping each entry and spending the row
	// budget on it; an entry that would blow the remaining budget gets
	// wrapped up to what's left with a trailing "…" marker rather than
	// silently dropping the rest, and older entries are skipped once the
	// budget is exhausted.
	var rows []string
	for i := len(m.logLines) - 1; i >= 0 && len(rows) < height; i-- {
		wrapped := wrapLine(m.logLines[i], width)
		remaining := height - len(rows)
		if len(wrapped) > remaining {
			wrapped = wrapped[:remaining]
			wrapped[remaining-1] = markTruncated(wrapped[remaining-1], width)
		}
		rows = append(wrapped, rows...)
	}

	var b strings.Builder
	b.WriteString(rule("telemetry", width))
	b.WriteString("\n")
	for _, line := range rows {
		b.WriteString(line)
		b.WriteString("\n")
	}
	for i := len(rows); i < height; i++ {
		b.WriteString("\n")
	}
	b.WriteString(strings.Repeat("─", width))
	b.WriteString("\n")
	return b.String()
}

// wrapLine breaks s into width-wide chunks, so a long log line spans
// multiple rows instead of losing its tail.
func wrapLine(s string, width int) []string {
	if width <= 0 || len(s) <= width {
		return []string{s}
	}
	var out []string
	for len(s) > width {
		out = append(out, s[:width])
		s = s[width:]
	}
	return append(out, s)
}

// markTruncated appends a "…" to line to signal that the pane's row budget
// cut it off here, as opposed to the line just legitimately ending.
func markTruncated(line string, width int) string {
	const marker = " …"
	if width <= len(marker) {
		return line
	}
	if len(line) > width-len(marker) {
		line = line[:width-len(marker)]
	}
	return line + marker
}

// rule renders a horizontal divider with a centered-left label, e.g.
// "── telemetry ────────────────". Falls back to a bare label if width is
// too small to fit any filler.
func rule(label string, width int) string {
	prefix := "── " + label + " "
	if width <= len(prefix) {
		return prefix
	}
	return prefix + strings.Repeat("─", width-len(prefix))
}

// runInput dispatches one line of raw REPL input exactly as KeyEnter and
// rewind's replay both need: /execute <path> reads and runs a file,
// anything else runs as Python. It does not handle /debug — that toggles
// session state directly and is deliberately excluded from history, so it
// never appears in the input rewind replays.
func (m model) runInput(code string) entry {
	if code == executeCmd || strings.HasPrefix(code, executePrefix) {
		path := strings.TrimSpace(strings.TrimPrefix(code, executeCmd))
		if path == "" {
			return entry{code: code, result: "usage: /execute <path>", isErr: true}
		}
		return m.executeFile(code, path)
	}
	return m.run(code)
}

// rewind implements bpython's Rewind (Ctrl-R): pops the last input back
// into the prompt for editing, and replays everything before it against a
// fresh Repl so accumulated state (variables, imports) actually reflects a
// session with that line removed, not just a transcript that looks like it.
func (m model) rewind() model {
	if len(m.history) == 0 {
		return m
	}
	last := m.history[len(m.history)-1]
	m.history = m.history[:len(m.history)-1]
	m.historyIdx = len(m.history)
	m.pendingInput = ""

	repl, err := monty.NewRepl(monty.ReplOptions{ScriptName: "shmonty.py"})
	if err != nil {
		// Extremely unlikely — NewRepl only fails on FFI init issues, and
		// startup already proved it works. Leave the session untouched
		// rather than lose it.
		m.history = append(m.history, last)
		return m
	}
	m.repl = repl

	m.entries = nil
	for _, code := range m.history {
		m.entries = append(m.entries, m.runInput(code))
	}
	if len(m.entries) > maxHistoryEntries {
		m.entries = m.entries[len(m.entries)-maxHistoryEntries:]
	}
	m.logLines = append(m.logLines, m.logs.drain()...)
	if len(m.logLines) > maxStoredLogLines {
		m.logLines = m.logLines[len(m.logLines)-maxStoredLogLines:]
	}

	m.textInput.SetValue(last)
	m.textInput.CursorEnd()
	return m
}

func (m model) run(code string) entry {
	return m.runCode(code, code)
}

// executeFile implements "/execute <path>": reads path (absolute or
// relative to the working directory shmonty was started in) and feeds its
// contents to the REPL as one FeedRun call, same as typing them in — state
// (variables, imports) carries over exactly like any other input. display
// is what's echoed in the transcript (">>> /execute path"), so a large
// script doesn't get dumped inline in place of the command that ran it.
func (m model) executeFile(display, path string) entry {
	data, err := os.ReadFile(path)
	if err != nil {
		return entry{code: display, result: err.Error(), isErr: true}
	}
	return m.runCode(display, string(data))
}

// hostFunctions returns the external functions registered for every run,
// interactive or not, so callback spans (and scripts that just want a
// clock) have something to exercise.
func hostFunctions() map[string]monty.ExternalFunction {
	return map[string]monty.ExternalFunction{
		"host_time": func(context.Context, monty.Call) (monty.Result, error) {
			return monty.Return(monty.String(time.Now().Format(time.RFC3339))), nil
		},
	}
}

// runCode feeds code to the REPL and builds the resulting transcript entry,
// labeled with display rather than code itself so callers like
// executeFile can show a short command in place of a whole file's contents.
func (m model) runCode(display, code string) entry {
	var stdout strings.Builder
	opts := monty.FeedOptions{
		Print:     monty.WriterPrintCallback(&stdout),
		Functions: hostFunctions(),
	}
	switch {
	case m.otelHandler != nil:
		opts.Telemetry = m.otelHandler
		// RecordArguments/RecordOutputs stay at their false default here,
		// unlike the /debug branch below: this leaves the process for an
		// external OTel backend, and call payloads can carry credentials
		// (gomonty-olly.md §4) — nothing opts a live export target into
		// seeing them.
	case m.debugEnabled:
		// DurationUnit: DurationNanoseconds — shmonty's own operations are
		// sub-millisecond, so the library's ms default would round every
		// duration down to 0.
		opts.Telemetry = monty.SlogHandler{Logger: m.logger, DurationUnit: monty.DurationNanoseconds}
		// RecordArguments/RecordOutputs default to false so a production
		// host doesn't leak call payloads that might carry credentials —
		// shmonty is a local debug REPL with nothing to protect, so seeing
		// full arguments/results is exactly the point of turning /debug on.
		opts.TelemetryOptions = monty.TelemetryOptions{
			RecordArguments: true,
			RecordOutputs:   true,
		}
	}
	value, err := m.repl.FeedRun(context.Background(), code, opts)
	if err != nil {
		return entry{code: display, output: stdout.String(), result: err.Error(), isErr: true}
	}
	return entry{code: display, output: stdout.String(), result: value.String()}
}

func (m model) View() string {
	var b strings.Builder
	header := "shmonty  (Ctrl+C/Ctrl+D to quit, Up/Down for history, Ctrl+R rewind, /debug telemetry, /execute <path> [Tab completes])"
	if m.otelHandler != nil {
		header += "  [otel export active]"
	}
	b.WriteString(header)
	b.WriteString("\n")
	if m.debugEnabled {
		b.WriteString(m.renderLogPane())
	}
	b.WriteString("\n")

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
	if len(os.Args) > 1 {
		runScript(os.Args[1])
		return
	}

	ctx := context.Background()
	otelHandler, otelShutdown := initOTelTelemetry(ctx)
	defer otelShutdown(ctx)

	logs := &logBuffer{}
	logger := slog.New(slog.NewTextHandler(logs, &slog.HandlerOptions{Level: slog.LevelDebug}))

	repl, err := monty.NewRepl(monty.ReplOptions{ScriptName: "shmonty.py"})
	if err != nil {
		log.Fatal(err)
	}

	m := newModel(repl, logger, logs)
	m.otelHandler = otelHandler

	if _, err := tea.NewProgram(m, tea.WithAltScreen()).Run(); err != nil {
		log.Fatal(err)
	}
}

// runScript implements `shmonty <path>`: runs path once, non-interactively
// — unlike the REPL's /execute, which feeds a file into an already-running
// session — printing print() output as it happens, then the script's final
// value, then exits. Telemetry is always on here (there's no REPL to toggle
// /debug in) and its log lines print last, after the script's own output,
// so they read as a trailing diagnostic block rather than interleaving with
// it. A read or execution error prints to stderr and exits non-zero,
// matching Python's own unhandled-exception convention.
func runScript(path string) {
	data, err := os.ReadFile(path)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}

	ctx := context.Background()
	otelHandler, otelShutdown := initOTelTelemetry(ctx)
	defer otelShutdown(ctx)

	logs := &logBuffer{}
	logger := slog.New(slog.NewTextHandler(logs, &slog.HandlerOptions{Level: slog.LevelDebug}))

	runner, err := monty.New(string(data), monty.CompileOptions{ScriptName: filepath.Base(path)})
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}

	opts := monty.RunOptions{
		Print:     monty.WriterPrintCallback(os.Stdout),
		Functions: hostFunctions(),
	}
	if otelHandler != nil {
		// See runCode's otelHandler branch: RecordArguments/RecordOutputs
		// stay off, unlike the local-only SlogHandler case below.
		opts.Telemetry = otelHandler
	} else {
		opts.Telemetry = monty.SlogHandler{Logger: logger, DurationUnit: monty.DurationNanoseconds}
		opts.TelemetryOptions = monty.TelemetryOptions{
			RecordArguments: true,
			RecordOutputs:   true,
		}
	}

	value, runErr := runner.Run(ctx, opts)

	if runErr != nil {
		fmt.Fprintln(os.Stderr, "error:", runErr)
		printDebugLogs(logs)
		os.Exit(1)
	}
	fmt.Println(value.String())
	printDebugLogs(logs)
}

// printDebugLogs writes captured telemetry lines to stderr as a labeled
// trailing block, or nothing at all if none were captured.
func printDebugLogs(logs *logBuffer) {
	lines := logs.drain()
	if len(lines) == 0 {
		return
	}
	fmt.Fprintln(os.Stderr, rule("telemetry", 80))
	for _, line := range lines {
		fmt.Fprintln(os.Stderr, line)
	}
}
