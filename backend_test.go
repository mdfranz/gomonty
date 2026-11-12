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

func (f *fakeBackend) NewRunner(string, CompileOptions) (RunnerHandle, error) {
	return &fakeRunner{b: f}, nil
}
func (f *fakeBackend) LoadRunner([]byte) (RunnerHandle, error) { return &fakeRunner{b: f}, nil }
func (f *fakeBackend) NewRepl(ReplOptions) (ReplHandle, error) { return &fakeRepl{b: f}, nil }
func (f *fakeBackend) LoadProgress([]byte) Step {
	return Step{Err: errors.New("fake backend cannot load dumps")}
}

func (r *fakeRunner) Close()                        { r.b.closes.Add(1) }
func (r *fakeRunner) Dump() ([]byte, error)         { return []byte("runner"), nil }
func (r *fakeRunner) TypeCheck(prefix string) error { return nil }
func (r *fakeRunner) Start(_ context.Context, opts StartOptions) Step {
	return pausedAtDouble(r.b, opts.Inputs["x"], false)
}
func (r *fakeRepl) Close()                { r.b.closes.Add(1) }
func (r *fakeRepl) Dump() ([]byte, error) { return []byte("repl"), nil }
func (r *fakeRepl) FeedStart(_ context.Context, _ string, opts FeedStartOptions) Step {
	return pausedAtDouble(r.b, opts.Inputs["x"], true)
}

func pausedAtDouble(b *fakeBackend, x Value, isRepl bool) Step {
	return Step{
		Progress: &fakeProgress{b: b},
		Prints:   "paused\n",
		Desc: ProgressDesc{
			Kind:         ProgressFunctionCall,
			ScriptName:   "fake.py",
			IsRepl:       isRepl,
			FunctionName: "double",
			Args:         []Value{x},
			CallID:       1,
		},
	}
}

func (p *fakeProgress) Close()                { p.b.closes.Add(1) }
func (p *fakeProgress) Dump() ([]byte, error) { return []byte("progress"), nil }
func (p *fakeProgress) TakeRepl() (ReplHandle, error) {
	return &fakeRepl{b: p.b}, nil
}

func (p *fakeProgress) ResumeCall(_ context.Context, result Result) Step {
	value, ok := result.Value().Raw().(int64)
	if !ok {
		// Like a backend that cannot encode the answer: nothing was sent.
		return Step{Err: errors.New("fake backend only takes ints"), Retained: true}
	}
	return Step{
		Progress: &fakeProgress{b: p.b, complete: true},
		Desc:     ProgressDesc{Kind: ProgressComplete, ScriptName: "fake.py", Output: Int(value * 2)},
	}
}

func (p *fakeProgress) ResumeLookup(context.Context, Value, bool) Step {
	return Step{Err: errors.New("unused")}
}
func (p *fakeProgress) ResumeFutures(context.Context, map[uint32]Result) Step {
	return Step{Err: errors.New("unused")}
}

func doubleFunction(_ context.Context, call Call) (Result, error) {
	return Return(call.Args[0]), nil
}

func TestRunnerRunsOverAnyBackend(t *testing.T) {
	fake := &fakeBackend{}
	runner, err := New("ignored", CompileOptions{Backend: fake})
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
	runner, err := New("ignored", CompileOptions{Backend: fake})
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
	repl, err := NewRepl(ReplOptions{ScriptName: "fake.py", Backend: &fakeBackend{}})
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
