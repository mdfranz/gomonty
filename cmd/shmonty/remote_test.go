package main

import (
	"bytes"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

func TestParseRemoteFlag(t *testing.T) {
	cases := []struct {
		in       []string
		endpoint string
		rest     []string
		wantErr  bool
	}{
		{nil, "", nil, false},
		{[]string{"a.py"}, "", []string{"a.py"}, false},
		{[]string{"--remote", "h:443"}, "h:443", nil, false},
		{[]string{"--remote=h:443"}, "h:443", nil, false},
		{[]string{"a.py", "--remote", "h:443"}, "h:443", []string{"a.py"}, false},
		{[]string{"--remote"}, "", nil, true},
	}
	for _, c := range cases {
		endpoint, rest, err := parseRemoteFlag(c.in)
		if (err != nil) != c.wantErr || endpoint != c.endpoint || !reflect.DeepEqual(rest, c.rest) {
			t.Errorf("parseRemoteFlag(%q) = %q, %q, %v", c.in, endpoint, rest, err)
		}
	}
}

func TestUseRemoteRefusesTokenWithoutTLS(t *testing.T) {
	t.Setenv("MONTY_REMOTE_INSECURE", "1")
	t.Setenv("MONTY_REMOTE_TOKEN", "secret")
	if err := useRemote("localhost:1"); err == nil {
		t.Fatal("useRemote sent a token without TLS")
	}
}

func TestWorkerAttrsSayWhereItRuns(t *testing.T) {
	old := remoteEndpoint
	t.Cleanup(func() { remoteEndpoint = old })

	remoteEndpoint = ""
	if got := workerAttrs(); !reflect.DeepEqual(got, []any{"worker", "local"}) {
		t.Errorf("local attrs = %v", got)
	}
	remoteEndpoint = "h:443"
	if got := workerAttrs(); !reflect.DeepEqual(got, []any{"worker", "remote", "worker_endpoint", "h:443"}) {
		t.Errorf("remote attrs = %v", got)
	}
}

// useRealWorker points newRepl at the worker named by MONTY_REMOTE_ENDPOINT
// for the rest of the test, and skips the test when none is configured.
func useRealWorker(t *testing.T) {
	t.Helper()
	endpoint := os.Getenv("MONTY_REMOTE_ENDPOINT")
	if endpoint == "" {
		t.Skip("MONTY_REMOTE_ENDPOINT is not set")
	}
	oldBackend, oldEndpoint, oldTransport := replBackend, remoteEndpoint, remoteTransport
	if err := useRemote(endpoint); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		remoteTransport.Close()
		replBackend, remoteEndpoint, remoteTransport = oldBackend, oldEndpoint, oldTransport
	})
}

// The same checks the local sandbox passes must pass over a remote worker, so a
// regression in value or OS-call support shows up here.
func TestSandboxChecksOverARemoteWorker(t *testing.T) {
	useRealWorker(t)
	repl, err := newRepl()
	if err != nil {
		t.Fatal(err)
	}
	m := newModel(repl, nil, &logBuffer{})
	if e := m.runCode("checks", sandboxChecks); e.isErr {
		t.Fatal(e.result)
	}
	if e := m.runCode("read", `assert Path("/sandbox/test.txt").read_text() == "saved"`); e.isErr {
		t.Fatal(e.result)
	}
}

func TestStatusLineSaysWhereTheWorkerRuns(t *testing.T) {
	old := remoteEndpoint
	t.Cleanup(func() { remoteEndpoint = old })
	m := newModel(nil, nil, &logBuffer{})

	remoteEndpoint = ""
	if got := m.renderStatusLine(); strings.Contains(got, "remote:") {
		t.Fatalf("a local session says it is remote: %q", got)
	}
	remoteEndpoint = "worker.example:443"
	if got := m.renderStatusLine(); !strings.Contains(got, "remote:worker.example:443") {
		t.Fatalf("status line %q does not name the worker", got)
	}
}

// A script given with --remote runs on the worker through a REPL session, with
// the same host functions, filesystem and output as a local run.
func TestScriptRunsOverARemoteWorker(t *testing.T) {
	useRealWorker(t)
	path := filepath.Join(t.TempDir(), "remote_script.py")
	script := `from pathlib import Path
print("from the worker")
total = host_calc("add", 2, 3)
await host_sleep(0.01, "slept")
Path("/sandbox/readme.txt").read_text().startswith("Welcome") and total
`
	if err := os.WriteFile(path, []byte(script), 0o600); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	if err := execScript(path, &stdout, &stderr); err != nil {
		t.Fatalf("execScript: %v\nstderr: %s", err, stderr.String())
	}
	if !strings.Contains(stdout.String(), "from the worker") || !strings.Contains(stdout.String(), "5") {
		t.Fatalf("stdout = %q, want the print and the final value 5", stdout.String())
	}
	if !strings.Contains(stderr.String(), "worker=remote") {
		t.Fatalf("telemetry did not say the script ran remotely: %q", stderr.String())
	}
}

func TestFailedRemoteScriptShowsATraceback(t *testing.T) {
	useRealWorker(t)
	path := filepath.Join(t.TempDir(), "boom.py")
	if err := os.WriteFile(path, []byte("def f():\n    raise ValueError('boom')\nf()\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	if err := execScript(path, &stdout, &stderr); err == nil {
		t.Fatal("a failing script succeeded")
	}
	for _, want := range []string{"Traceback (most recent call last)", "in f", "ValueError: boom"} {
		if !strings.Contains(stderr.String(), want) {
			t.Errorf("stderr lacks %q:\n%s", want, stderr.String())
		}
	}
}

// Cancelling a turn that is waiting on a remote worker ends it promptly, and
// the interface gets a working session back.
func TestCtrlCCancelsATurnOnARemoteWorker(t *testing.T) {
	useRealWorker(t)
	m := newTestModel(t)
	m, cmd := submit(m, "await host_sleep(20)")
	done := make(chan tea.Msg, 1)
	go func() { done <- cmd() }()
	time.Sleep(500 * time.Millisecond)
	m, _ = press(m, tea.KeyCtrlC)

	select {
	case msg := <-done:
		next, _ := m.Update(msg)
		m = next.(model)
	case <-time.After(10 * time.Second):
		t.Fatal("the remote turn was not cancelled")
	}
	if e := lastEntry(t, m); e.isErr || !strings.Contains(e.result, "cancelled") {
		t.Fatalf("entry = %+v", e)
	}
	if e := m.runCode("after", "1 + 2"); e.isErr || e.result != "3" {
		t.Fatalf("the new remote session: %+v", e)
	}
}

func TestResetAndRewindOverARemoteWorker(t *testing.T) {
	useRealWorker(t)
	m := newTestModel(t)
	for _, code := range []string{"x = 1", "y = 2"} {
		if e := m.runCode(code, code); e.isErr {
			t.Fatal(e.result)
		}
	}
	m.history = []string{"x = 1", "y = 2"}
	m = m.rewind()
	if e := m.runCode("x", "x"); e.isErr || e.result != "1" {
		t.Fatalf("x after a remote rewind: %+v", e)
	}
	if e := m.runCode("y", "y"); !e.isErr {
		t.Fatal("y survived a remote rewind that removed it")
	}

	m, _ = m.sessionCommand("/reset")
	if e := lastEntry(t, m); e.isErr {
		t.Fatalf("/reset: %+v", e)
	}
	if e := m.runCode("x", "x"); !e.isErr {
		t.Fatal("x survived /reset on the worker")
	}
}

// A worker may refuse to store a snapshot; that is reported, not fatal.
func TestDumpAndLoadOverARemoteWorker(t *testing.T) {
	useRealWorker(t)
	m := newTestModel(t)
	if e := m.runCode("x", "x = 41"); e.isErr {
		t.Fatal(e.result)
	}
	path := filepath.Join(t.TempDir(), "remote.dump")
	m, _ = m.sessionCommand("/dump " + path)
	e := lastEntry(t, m)
	if e.isErr {
		t.Skipf("the worker refused the dump: %s", e.result)
	}
	m, _ = m.sessionCommand("/load " + path)
	if e := lastEntry(t, m); e.isErr {
		t.Fatalf("/load: %+v", e)
	}
	if e := m.runCode("x", "x + 1"); e.isErr || e.result != "42" {
		t.Fatalf("after /load: %+v", e)
	}
}

// The same scripts, with the same pinned results, run on a remote worker, so a
// difference between the local library and a worker shows up as a failure here.
func TestPythonScriptsOverARemoteWorker(t *testing.T) {
	useRealWorker(t)
	checkPythonScripts(t)
}

// With /typecheck on, a snippet with a type error is rejected before it runs;
// with it off the same snippet runs.
func TestTypeCheckOnARemoteWorker(t *testing.T) {
	useRealWorker(t)
	m := newTestModel(t)

	if e := m.runCode("untyped", "x: int = 'text'"); e.isErr {
		t.Fatalf("type checking is off by default, but a snippet failed: %+v", e)
	}
	m, _ = m.sessionCommand("/typecheck on")
	if e := lastEntry(t, m); e.isErr {
		t.Fatalf("/typecheck on: %+v", e)
	}
	e := m.runCode("bad", "y: int = 'text'")
	if !e.isErr || !strings.Contains(e.result, "type check failed") {
		t.Fatalf("with checking on, a type error ran: %+v", e)
	}
	if e := m.runCode("good", "z: int = 4\nz + 1"); e.isErr || e.result != "5" {
		t.Fatalf("a well-typed snippet: %+v", e)
	}
	m, _ = m.sessionCommand("/typecheck off")
	if e := m.runCode("again", "w: int = 'text'"); e.isErr {
		t.Fatalf("after /typecheck off the snippet failed: %+v", e)
	}
}
