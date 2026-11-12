package monty

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
)

// fakeBackend runs no interpreter. Every execution pauses once at an external
// call named "double" with the input x, and completes with twice what the host
// returns. It proves Runner, Repl, the snapshots and the dispatch loop work
// over any backend, not only the native library.
type fakeBackend struct {
	closes atomic.Int32
}

type fakeRunner struct{ b *fakeBackend }
type fakeRepl struct{ b *fakeBackend }
type fakeProgress struct {
	b        *fakeBackend
	complete bool
}

func (f *fakeBackend) newRunner(string, CompileOptions) (runnerHandle, error) {
	return &fakeRunner{b: f}, nil
}
func (f *fakeBackend) loadRunner([]byte) (runnerHandle, error) { return &fakeRunner{b: f}, nil }
func (f *fakeBackend) newRepl(ReplOptions) (replHandle, error) { return &fakeRepl{b: f}, nil }
func (f *fakeBackend) loadProgress([]byte) step {
	return step{err: errors.New("fake backend cannot load dumps")}
}

func (r *fakeRunner) Close()                        { r.b.closes.Add(1) }
func (r *fakeRunner) Dump() ([]byte, error)         { return []byte("runner"), nil }
func (r *fakeRunner) TypeCheck(prefix string) error { return nil }
func (r *fakeRunner) Start(opts StartOptions) step {
	return pausedAtDouble(r.b, opts.Inputs["x"], false)
}
func (r *fakeRepl) Close()                { r.b.closes.Add(1) }
func (r *fakeRepl) Dump() ([]byte, error) { return []byte("repl"), nil }
func (r *fakeRepl) FeedStart(_ string, opts FeedStartOptions) step {
	return pausedAtDouble(r.b, opts.Inputs["x"], true)
}

func pausedAtDouble(b *fakeBackend, x Value, isRepl bool) step {
	return step{
		progress: &fakeProgress{b: b},
		prints:   "paused\n",
		desc: progressDesc{
			kind:         progressFunctionCall,
			scriptName:   "fake.py",
			isRepl:       isRepl,
			functionName: "double",
			args:         []Value{x},
			callID:       1,
		},
	}
}

func (p *fakeProgress) Close()                { p.b.closes.Add(1) }
func (p *fakeProgress) Dump() ([]byte, error) { return []byte("progress"), nil }
func (p *fakeProgress) TakeRepl() (replHandle, error) {
	return &fakeRepl{b: p.b}, nil
}

func (p *fakeProgress) ResumeCall(result Result) step {
	value, ok := result.Value().Raw().(int64)
	if !ok {
		// Like a backend that cannot encode the answer: nothing was sent.
		return step{err: errors.New("fake backend only takes ints"), retained: true}
	}
	return step{
		progress: &fakeProgress{b: p.b, complete: true},
		desc:     progressDesc{kind: progressComplete, scriptName: "fake.py", output: Int(value * 2)},
	}
}

func (p *fakeProgress) ResumeLookup(Value, bool) step { return step{err: errors.New("unused")} }
func (p *fakeProgress) ResumeFutures(map[uint32]Result) step {
	return step{err: errors.New("unused")}
}

func doubleFunction(_ context.Context, call Call) (Result, error) {
	return Return(call.Args[0]), nil
}

func TestRunnerRunsOverAnyBackend(t *testing.T) {
	fake := &fakeBackend{}
	runner, err := New("ignored", CompileOptions{backend: fake})
	if err != nil {
		t.Fatal(err)
	}

	var printed string
	got, err := runner.Run(context.Background(), RunOptions{
		Inputs:    map[string]Value{"x": Int(21)},
		Functions: map[string]ExternalFunction{"double": doubleFunction},
		Print:     func(_ string, text string) { printed += text },
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if got.Raw() != int64(42) {
		t.Fatalf("result = %v, want 42", got)
	}
	if printed != "paused\n" {
		t.Fatalf("printed %q, want the backend's prints", printed)
	}
	// Both progress handles (the paused call and the completed state) are
	// released; the runner is still owned by the caller.
	if closes := fake.closes.Load(); closes != 2 {
		t.Fatalf("closed %d handles during Run, want 2 progress handles", closes)
	}

	data, err := runner.Dump()
	if err != nil {
		t.Fatal(err)
	}
	if string(data[dumpHeaderLen:]) != "runner" {
		t.Fatalf("Dump payload = %q, want the backend's bytes behind the gomonty header", data[dumpHeaderLen:])
	}
}

func TestSnapshotSurvivesAnAnswerTheBackendCannotEncode(t *testing.T) {
	fake := &fakeBackend{}
	runner, err := New("ignored", CompileOptions{backend: fake})
	if err != nil {
		t.Fatal(err)
	}
	progress, err := runner.Start(context.Background(), StartOptions{Inputs: map[string]Value{"x": Int(5)}})
	if err != nil {
		t.Fatal(err)
	}
	snapshot := progress.(*Snapshot)

	if _, err := snapshot.ResumeReturn(context.Background(), String("not an int")); err == nil {
		t.Fatal("resuming with an unencodable value succeeded")
	}
	// The failed resume did not consume the snapshot, so it can be answered again.
	next, err := snapshot.ResumeReturn(context.Background(), Int(5))
	if err != nil {
		t.Fatalf("resume after encode failure: %v", err)
	}
	if complete, ok := next.(*Complete); !ok || complete.Output.Raw() != int64(10) {
		t.Fatalf("got %#v, want Complete with output 10", next)
	}
}

func TestReplRunsOverAnyBackendAndStaysUsable(t *testing.T) {
	repl, err := NewRepl(ReplOptions{ScriptName: "fake.py", backend: &fakeBackend{}})
	if err != nil {
		t.Fatal(err)
	}
	for i := int64(1); i <= 2; i++ {
		got, err := repl.FeedRun(context.Background(), "ignored", FeedOptions{
			Inputs:    map[string]Value{"x": Int(i)},
			Functions: map[string]ExternalFunction{"double": doubleFunction},
		})
		if err != nil {
			t.Fatalf("FeedRun %d: %v", i, err)
		}
		if got.Raw() != i*2 {
			t.Fatalf("FeedRun %d = %v, want %d", i, got, i*2)
		}
	}
}
