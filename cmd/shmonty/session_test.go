package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

func newTestModel(t *testing.T) model {
	t.Helper()
	repl, err := newRepl()
	if err != nil {
		t.Fatal(err)
	}
	return newModel(repl, nil, &logBuffer{})
}

func press(m model, key tea.KeyType) (model, tea.Cmd) {
	next, cmd := m.Update(tea.KeyMsg{Type: key})
	return next.(model), cmd
}

func submit(m model, code string) (model, tea.Cmd) {
	m.textInput.SetValue(code)
	return press(m, tea.KeyEnter)
}

// finish runs the command a submit returned and feeds its result back in.
func finish(t *testing.T, m model, cmd tea.Cmd) model {
	t.Helper()
	if cmd == nil {
		t.Fatal("no command to run")
	}
	next, _ := m.Update(cmd())
	return next.(model)
}

func lastEntry(t *testing.T, m model) entry {
	t.Helper()
	if len(m.entries) == 0 {
		t.Fatal("no transcript entries")
	}
	return m.entries[len(m.entries)-1]
}

// A snippet runs off the UI loop: the model reports it is running, ignores
// typing, and takes the result when it arrives.
func TestSnippetRunsOffTheUILoop(t *testing.T) {
	m := newTestModel(t)
	m, cmd := submit(m, "1 + 1")
	if !m.running || cmd == nil {
		t.Fatalf("running = %v, cmd = %v; want a snippet in flight", m.running, cmd)
	}
	if got := m.renderStatusLine(); !strings.Contains(got, "running") {
		t.Fatalf("status line %q does not say a snippet is running", got)
	}
	// Typing and Enter wait for the prompt.
	m, again := submit(m, "2 + 2")
	if again != nil || len(m.entries) != 0 {
		t.Fatal("input was accepted while a snippet was running")
	}

	m = finish(t, m, cmd)
	if m.running {
		t.Fatal("still running after the result arrived")
	}
	if e := lastEntry(t, m); e.isErr || e.result != "2" {
		t.Fatalf("entry = %+v, want 2", e)
	}
	if len(m.history) != 1 || m.history[0] != "1 + 1" {
		t.Fatalf("history = %v", m.history)
	}
}

// Ctrl+C ends an in-flight turn instead of freezing the interface or quitting.
func TestCtrlCCancelsARunningSnippet(t *testing.T) {
	m := newTestModel(t)
	if e := m.runCode("x", "x = 1"); e.isErr {
		t.Fatal(e.result)
	}
	m, cmd := submit(m, "await host_sleep(20)")
	done := make(chan tea.Msg, 1)
	go func() { done <- cmd() }()

	time.Sleep(100 * time.Millisecond)
	m, quit := press(m, tea.KeyCtrlC)
	if m.quitting || quit != nil {
		t.Fatal("the first Ctrl+C quit instead of cancelling")
	}
	if !m.cancelling || !strings.Contains(m.renderStatusLine(), "cancelling") {
		t.Fatalf("not cancelling: %q", m.renderStatusLine())
	}

	select {
	case msg := <-done:
		next, _ := m.Update(msg)
		m = next.(model)
	case <-time.After(5 * time.Second):
		t.Fatal("the snippet was not cancelled")
	}
	if m.running || m.cancelling {
		t.Fatalf("running = %v, cancelling = %v after the turn ended", m.running, m.cancelling)
	}
	if e := lastEntry(t, m); e.isErr || !strings.Contains(e.result, "cancelled") {
		t.Fatalf("entry = %+v, want a cancellation note", e)
	}
	// The session was restarted and works.
	if e := m.runCode("fresh", "1 + 2"); e.isErr || e.result != "3" {
		t.Fatalf("after cancel: %+v", e)
	}
	if len(m.history) != 0 {
		t.Fatalf("history = %v, want it cleared with the old session", m.history)
	}
}

func TestSecondCtrlCQuitsWhileCancelling(t *testing.T) {
	m := newTestModel(t)
	m, _ = submit(m, "await host_sleep(20)")
	m, _ = press(m, tea.KeyCtrlC)
	m, cmd := press(m, tea.KeyCtrlC)
	if !m.quitting || cmd == nil {
		t.Fatal("a second Ctrl+C did not quit")
	}
}

func TestDumpResetLoad(t *testing.T) {
	m := newTestModel(t)
	if e := m.runCode("x", "x = 41\ndef inc(n):\n    return n + 1"); e.isErr {
		t.Fatal(e.result)
	}
	path := filepath.Join(t.TempDir(), "session.dump")

	m, ok := m.sessionCommand("/dump " + path)
	if !ok {
		t.Fatal("/dump was not a session command")
	}
	if e := lastEntry(t, m); e.isErr || !strings.Contains(e.result, "dumped") {
		t.Fatalf("/dump: %+v", e)
	}
	if info, err := os.Stat(path); err != nil || info.Size() == 0 || info.Mode().Perm() != 0o600 {
		t.Fatalf("dump file: %v, %v", info, err)
	}

	m, _ = m.sessionCommand("/reset")
	if e := lastEntry(t, m); e.isErr || !strings.Contains(e.result, "reset") {
		t.Fatalf("/reset: %+v", e)
	}
	if e := m.runCode("gone", "x"); !e.isErr {
		t.Fatal("x survived /reset")
	}

	m, _ = m.sessionCommand("/load " + path)
	if e := lastEntry(t, m); e.isErr || !strings.Contains(e.result, "loaded") {
		t.Fatalf("/load: %+v", e)
	}
	if e := m.runCode("back", "inc(x)"); e.isErr || e.result != "42" {
		t.Fatalf("after /load: %+v", e)
	}
}

func TestSessionCommandsReportProblems(t *testing.T) {
	m := newTestModel(t)
	for _, code := range []string{"/dump", "/load", "/dump ", "/load /no/such/file"} {
		next, ok := m.sessionCommand(code)
		if !ok {
			t.Fatalf("%q was not handled as a session command", code)
		}
		if e := lastEntry(t, next); !e.isErr {
			t.Errorf("%q: %+v, want an error", code, e)
		}
	}
	garbage := filepath.Join(t.TempDir(), "garbage")
	if err := os.WriteFile(garbage, []byte("not a dump"), 0o600); err != nil {
		t.Fatal(err)
	}
	next, _ := m.sessionCommand("/load " + garbage)
	if e := lastEntry(t, next); !e.isErr {
		t.Fatalf("loading garbage: %+v, want an error", e)
	}
	// A failed load keeps the session.
	if e := next.runCode("still", "1 + 1"); e.isErr || e.result != "2" {
		t.Fatalf("after a failed load: %+v", e)
	}
	if _, ok := m.sessionCommand("1 + 1"); ok {
		t.Fatal("Python code was taken for a session command")
	}
}

// The new commands are not Python, so they stay out of the history that rewind
// replays.
func TestSessionCommandsAreNotHistory(t *testing.T) {
	m := newTestModel(t)
	m.history = []string{"x = 1"}
	m, cmd := submit(m, "/reset")
	if cmd != nil || m.running {
		t.Fatal("/reset ran as a snippet")
	}
	if len(m.history) != 0 {
		t.Fatalf("history = %v, want it cleared by /reset", m.history)
	}
}

func TestTabCompletesPathsOfTheNewCommands(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "snapshot.dump"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, cmd := range []string{"/execute", "/load", "/dump"} {
		m := newTestModel(t)
		m.textInput.SetValue(cmd + " " + filepath.Join(dir, "snap"))
		m = m.completePath()
		if got, want := m.textInput.Value(), cmd+" "+filepath.Join(dir, "snapshot.dump"); got != want {
			t.Errorf("%s: completed to %q, want %q", cmd, got, want)
		}
	}
	m := newTestModel(t)
	m.textInput.SetValue("/reset " + filepath.Join(dir, "snap"))
	if got := m.completePath().textInput.Value(); got != "/reset "+filepath.Join(dir, "snap") {
		t.Errorf("/reset completed a path: %q", got)
	}
}

func TestRewindResetsTheSession(t *testing.T) {
	m := newTestModel(t)
	for _, code := range []string{"x = 1", "y = 2"} {
		if e := m.runCode(code, code); e.isErr {
			t.Fatal(e.result)
		}
	}
	m.history = []string{"x = 1", "y = 2"}
	m = m.rewind()
	if got := m.textInput.Value(); got != "y = 2" {
		t.Fatalf("prompt = %q, want the popped input", got)
	}
	if e := m.runCode("x", "x"); e.isErr || e.result != "1" {
		t.Fatalf("x after rewind: %+v", e)
	}
	if e := m.runCode("y", "y"); !e.isErr {
		t.Fatal("y survived a rewind that removed it")
	}
}

func TestTypeCheckNeedsARemoteWorker(t *testing.T) {
	old := remoteEndpoint
	t.Cleanup(func() { remoteEndpoint = old })
	remoteEndpoint = ""

	m := newTestModel(t)
	next, ok := m.sessionCommand("/typecheck on")
	if !ok {
		t.Fatal("/typecheck was not a session command")
	}
	if e := lastEntry(t, next); !e.isErr || !strings.Contains(e.result, "--remote") || next.typeCheck {
		t.Fatalf("a local session accepted /typecheck: %+v (typeCheck = %v)", e, next.typeCheck)
	}
}

func TestTypeCheckCommandToggles(t *testing.T) {
	old := remoteEndpoint
	t.Cleanup(func() { remoteEndpoint = old })
	remoteEndpoint = "worker.example:443"

	m := newTestModel(t)
	steps := []struct {
		cmd  string
		want bool
		err  bool
	}{
		{"/typecheck", true, false}, // no argument toggles
		{"/typecheck", false, false},
		{"/typecheck on", true, false},
		{"/typecheck on", true, false}, // explicit is idempotent
		{"/typecheck bogus", true, true},
		{"/typecheck off", false, false},
	}
	for _, step := range steps {
		next, _ := m.sessionCommand(step.cmd)
		m = next
		if m.typeCheck != step.want || lastEntry(t, m).isErr != step.err {
			t.Fatalf("%q: typeCheck = %v, entry = %+v; want %v, error %v", step.cmd, m.typeCheck, lastEntry(t, m), step.want, step.err)
		}
	}

	m.typeCheck = true
	if got := m.renderStatusLine(); !strings.Contains(got, "typecheck:on") {
		t.Fatalf("status line %q does not show type checking", got)
	}
}

func TestSessionCommandsKeepTypeCheckAcrossReset(t *testing.T) {
	old := remoteEndpoint
	t.Cleanup(func() { remoteEndpoint = old })
	remoteEndpoint = "worker.example:443"
	m := newTestModel(t)
	m.typeCheck = true
	m, _ = m.sessionCommand("/reset")
	if !m.typeCheck {
		t.Fatal("/reset turned type checking off; it is a setting of the shell, not of the session state")
	}
}
