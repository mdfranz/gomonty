package monty

import (
	"bytes"
	"context"
	"errors"
	"testing"
)

// The fakes below stand in for a backend that is not the native library. They
// are scripted per test and count how often handles are closed, which is where
// ownership mistakes show up.

type fakeRepl struct {
	onFeed func(code string) opResult
	feeds  []string
	closed int
	raw    []byte
}

func (r *fakeRepl) Close()                { r.closed++ }
func (r *fakeRepl) dump() ([]byte, error) { return r.raw, nil }
func (r *fakeRepl) feedStart(code string, _ wireFeedOptions) opResult {
	r.feeds = append(r.feeds, code)
	return r.onFeed(code)
}

type fakeProgress struct {
	repl         *fakeRepl
	onResumeCall func(wireCallResult) opResult
	gotCall      []wireCallResult
	closed       int
}

func (p *fakeProgress) Close()                { p.closed++ }
func (p *fakeProgress) dump() ([]byte, error) { return []byte("PROGRESS"), nil }
func (p *fakeProgress) resumeCall(r wireCallResult) opResult {
	p.gotCall = append(p.gotCall, r)
	return p.onResumeCall(r)
}
func (p *fakeProgress) resumeLookup(wireLookupResult) opResult {
	return opResult{err: errors.New("unexpected lookup")}
}
func (p *fakeProgress) resumeFutures(wireFutureResults) opResult {
	return opResult{err: errors.New("unexpected futures")}
}
func (p *fakeProgress) takeRepl() (replHandle, error) { return p.repl, nil }

type fakeBackend struct {
	repl  *fakeRepl
	loads []opResult // returned by loadProgress, in order
}

func (b *fakeBackend) newRunner(string, wireCompileOptions) (runnerHandle, error) {
	return nil, errors.New("fake backend has no runners")
}
func (b *fakeBackend) loadRunner([]byte) (runnerHandle, error) {
	return nil, errors.New("fake backend has no runners")
}
func (b *fakeBackend) newRepl(wireReplOptions) (replHandle, error) { return b.repl, nil }
func (b *fakeBackend) loadProgress([]byte) opResult {
	if len(b.loads) == 0 {
		return opResult{err: errors.New("no scripted load")}
	}
	next := b.loads[0]
	b.loads = b.loads[1:]
	return next
}

func completeWith(n int64) *wireProgressPayload {
	return &wireProgressPayload{
		Variant:    wireProgressComplete,
		ScriptName: "fake.py",
		IsRepl:     true,
		Output:     &wireValue{Kind: wireValueInt, Int: n},
	}
}

func callAdd() *wireProgressPayload {
	return &wireProgressPayload{
		Variant:      wireProgressFunctionCall,
		ScriptName:   "fake.py",
		IsRepl:       true,
		FunctionName: "add",
		Args:         []wireValue{{Kind: wireValueInt, Int: 40}, {Kind: wireValueInt, Int: 2}},
		CallID:       7,
	}
}

// A script that pauses on a host function is driven to completion through the
// public API without touching the native library.
func TestBackendReplPauseAndResume(t *testing.T) {
	repl := &fakeRepl{}
	pause := &fakeProgress{repl: repl}
	done := &fakeProgress{repl: repl}
	pause.onResumeCall = func(wireCallResult) opResult {
		return opResult{prints: "done\n", progress: done, desc: completeWith(42)}
	}
	repl.onFeed = func(code string) opResult {
		if code == "add(40, 2)" {
			return opResult{prints: "hello\n", progress: pause, desc: callAdd()}
		}
		return opResult{progress: done, desc: completeWith(7)}
	}

	r, err := newReplWith(&fakeBackend{repl: repl}, ReplOptions{ScriptName: "fake.py"})
	if err != nil {
		t.Fatal(err)
	}

	var printed bytes.Buffer
	var gotArgs []Value
	value, err := r.FeedRun(context.Background(), "add(40, 2)", FeedOptions{
		Functions: map[string]ExternalFunction{
			"add": func(_ context.Context, call Call) (Result, error) {
				gotArgs = call.Args
				return Return(Int(42)), nil
			},
		},
		Print: func(_, text string) { printed.WriteString(text) },
	})
	if err != nil {
		t.Fatalf("FeedRun: %v", err)
	}
	if got := value.Raw(); got != int64(42) {
		t.Fatalf("result = %#v, want 42", got)
	}
	if len(gotArgs) != 2 || gotArgs[0].Raw() != int64(40) || gotArgs[1].Raw() != int64(2) {
		t.Fatalf("host function args = %#v", gotArgs)
	}
	if printed.String() != "hello\ndone\n" {
		t.Fatalf("printed = %q", printed.String())
	}

	// The host's answer was passed to the backend as a return of 42.
	if len(pause.gotCall) != 1 || pause.gotCall[0].Kind != wireCallResultReturn ||
		pause.gotCall[0].Value == nil || pause.gotCall[0].Value.Int != 42 {
		t.Fatalf("backend saw %+v", pause.gotCall)
	}

	// Every progress handle is closed exactly once; the session is not.
	if pause.closed != 1 || done.closed != 1 {
		t.Fatalf("progress closes: pause=%d done=%d, want 1 each", pause.closed, done.closed)
	}
	if repl.closed != 0 {
		t.Fatalf("session closed %d times while still in use", repl.closed)
	}

	// The session came back, so the next snippet runs on it.
	value, err = r.FeedRun(context.Background(), "again", FeedOptions{})
	if err != nil || value.Raw() != int64(7) {
		t.Fatalf("second FeedRun = %v, %v", value.Raw(), err)
	}
	if len(repl.feeds) != 2 {
		t.Fatalf("session fed %v", repl.feeds)
	}
}

func TestBackendFailureHandsSessionBack(t *testing.T) {
	repl := &fakeRepl{}
	boom := errors.New("boom")
	calls := 0
	repl.onFeed = func(string) opResult {
		calls++
		if calls == 1 {
			return opResult{err: boom, repl: repl}
		}
		return opResult{progress: &fakeProgress{repl: repl}, desc: completeWith(1)}
	}

	r, err := newReplWith(&fakeBackend{repl: repl}, ReplOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.FeedRun(context.Background(), "x", FeedOptions{}); !errors.Is(err, boom) {
		t.Fatalf("err = %v, want boom", err)
	}
	if v, err := r.FeedRun(context.Background(), "x", FeedOptions{}); err != nil || v.Raw() != int64(1) {
		t.Fatalf("session unusable after a failed snippet: %v, %v", v.Raw(), err)
	}
}

// Bad inputs fail before anything runs. The session must come back, not stay
// marked in flight.
func TestFeedStartWithBadInputsKeepsSession(t *testing.T) {
	repl := &fakeRepl{}
	repl.onFeed = func(string) opResult {
		return opResult{progress: &fakeProgress{repl: repl}, desc: completeWith(1)}
	}
	r, err := newReplWith(&fakeBackend{repl: repl}, ReplOptions{})
	if err != nil {
		t.Fatal(err)
	}

	// A value of a kind the wire format does not know fails conversion.
	bad := Value{kind: ValueKind("not-a-real-kind")}
	_, err = r.FeedRun(context.Background(), "x", FeedOptions{Inputs: map[string]Value{"bad": bad}})
	if err == nil {
		t.Fatal("an unsupported value was accepted as an input")
	}
	if len(repl.feeds) != 0 {
		t.Fatalf("backend was fed despite invalid inputs: %v", repl.feeds)
	}
	if _, err := r.FeedRun(context.Background(), "x", FeedOptions{}); err != nil {
		t.Fatalf("session lost after invalid inputs: %v", err)
	}
}

func TestBackendReplDumpGetsGomontyHeader(t *testing.T) {
	repl := &fakeRepl{raw: []byte("RAW")}
	r, err := newReplWith(&fakeBackend{repl: repl}, ReplOptions{})
	if err != nil {
		t.Fatal(err)
	}
	data, err := r.Dump()
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.HasPrefix(data, dumpHeader) || !bytes.HasSuffix(data, []byte("RAW")) {
		t.Fatalf("dump = %q", data)
	}
}

// A REPL snapshot is loaded twice: once to resume from, once to recover the
// session that owns it. The recovery copy must be closed.
func TestLoadReplSnapshotWithBackend(t *testing.T) {
	repl := &fakeRepl{}
	primary := &fakeProgress{repl: repl}
	backup := &fakeProgress{repl: repl}
	b := &fakeBackend{loads: []opResult{
		{progress: primary, desc: callAdd()},
		{progress: backup, desc: callAdd()},
	}}

	progress, owner, err := loadReplSnapshotWith(b, addDumpHeader([]byte("snapshot")))
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := progress.(*Snapshot); !ok {
		t.Fatalf("progress is %T, want *Snapshot", progress)
	}
	if owner == nil || owner.state.handle == nil {
		t.Fatal("owner session was not restored")
	}
	if backup.closed != 1 || primary.closed != 0 {
		t.Fatalf("closes: backup=%d primary=%d, want 1 and 0", backup.closed, primary.closed)
	}
}

func TestLoadSnapshotClosesHandlesOnFailure(t *testing.T) {
	leaked := &fakeProgress{}
	b := &fakeBackend{loads: []opResult{{progress: leaked, err: errors.New("bad payload")}}}
	if _, err := loadSnapshotWith(b, addDumpHeader([]byte("snapshot"))); err == nil {
		t.Fatal("expected an error")
	}
	if leaked.closed != 1 {
		t.Fatalf("progress closed %d times, want 1", leaked.closed)
	}

	// A backend that reports success without a handle must not panic.
	b = &fakeBackend{loads: []opResult{{}}}
	if _, err := loadSnapshotWith(b, addDumpHeader([]byte("snapshot"))); err == nil {
		t.Fatal("expected an error for a missing handle")
	}
}
