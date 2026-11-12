package monty_test

import (
	"context"
	"errors"
	"testing"

	monty "github.com/mdfranz/gomonty"
)

// This backend lives outside package monty, as the remote client will. It
// compiles only if the exported Backend API is enough to implement one.

var errExternalUnsupported = errors.New("external test backend does not support this")

type externalBackend struct{}

func (externalBackend) NewRunner(string, monty.CompileOptions) (monty.RunnerHandle, error) {
	return nil, errExternalUnsupported
}
func (externalBackend) LoadRunner([]byte) (monty.RunnerHandle, error) {
	return nil, errExternalUnsupported
}
func (externalBackend) NewRepl(monty.ReplOptions) (monty.ReplHandle, error) {
	return &externalRepl{}, nil
}
func (externalBackend) LoadProgress([]byte) monty.Step {
	return monty.Step{Err: errExternalUnsupported}
}

// externalRepl pauses every snippet at a call to twice(21) and completes with
// whatever the host answers.
type externalRepl struct{}

func (*externalRepl) Close()                {}
func (*externalRepl) Dump() ([]byte, error) { return nil, errExternalUnsupported }
func (r *externalRepl) FeedStart(string, monty.FeedStartOptions) monty.Step {
	return monty.Step{
		Progress: &externalProgress{repl: r},
		Prints:   "started\n",
		Desc: monty.ProgressDesc{
			Kind:         monty.ProgressFunctionCall,
			IsRepl:       true,
			FunctionName: "twice",
			Args:         []monty.Value{monty.Int(21)},
			CallID:       1,
		},
	}
}

type externalProgress struct{ repl *externalRepl }

func (*externalProgress) Close()                {}
func (*externalProgress) Dump() ([]byte, error) { return nil, errExternalUnsupported }
func (p *externalProgress) TakeRepl() (monty.ReplHandle, error) {
	return p.repl, nil
}
func (p *externalProgress) ResumeCall(result monty.Result) monty.Step {
	answer, ok := result.Value().Raw().(int64)
	if !ok {
		return monty.Step{Err: errors.New("twice must return an int"), Retained: true}
	}
	return monty.Step{
		Progress: &externalProgress{repl: p.repl},
		Desc:     monty.ProgressDesc{Kind: monty.ProgressComplete, IsRepl: true, Output: monty.Int(answer)},
	}
}
func (*externalProgress) ResumeLookup(monty.Value, bool) monty.Step {
	return monty.Step{Err: errExternalUnsupported}
}
func (*externalProgress) ResumeFutures(map[uint32]monty.Result) monty.Step {
	return monty.Step{Err: errExternalUnsupported}
}

func TestExternalPackageCanImplementABackend(t *testing.T) {
	repl, err := monty.NewRepl(monty.ReplOptions{ScriptName: "external.py", Backend: externalBackend{}})
	if err != nil {
		t.Fatal(err)
	}

	var printed string
	run := func() (monty.Value, error) {
		return repl.FeedRun(context.Background(), "ignored", monty.FeedOptions{
			Functions: map[string]monty.ExternalFunction{
				"twice": func(_ context.Context, call monty.Call) (monty.Result, error) {
					n, _ := call.Args[0].Raw().(int64)
					return monty.Return(monty.Int(n * 2)), nil
				},
			},
			Print: func(_, text string) { printed += text },
		})
	}

	got, err := run()
	if err != nil {
		t.Fatalf("FeedRun: %v", err)
	}
	if got.Raw() != int64(42) {
		t.Fatalf("result = %v, want 42", got.Raw())
	}
	if printed != "started\n" {
		t.Fatalf("printed %q, want the backend's output", printed)
	}
	// The session came back, so a second snippet runs on it.
	if got, err = run(); err != nil || got.Raw() != int64(42) {
		t.Fatalf("second FeedRun = %v, %v", got.Raw(), err)
	}

	// An unsupported operation surfaces the backend's own error.
	if _, err := monty.New("1", monty.CompileOptions{Backend: externalBackend{}}); !errors.Is(err, errExternalUnsupported) {
		t.Fatalf("New over a backend without runners: %v", err)
	}
}
