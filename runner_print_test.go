//go:build (darwin && arm64) || (linux && amd64) || (linux && arm64) || (windows && amd64)

package monty

import (
	"context"
	"reflect"
	"strings"
	"testing"
)

func TestBytesCrossFFIBoundary(t *testing.T) {
	t.Run("result", func(t *testing.T) {
		runner, err := New(`b"hello"`, CompileOptions{ScriptName: "bytes.py"})
		if err != nil {
			t.Fatalf("New: %v", err)
		}
		t.Cleanup(func() { closeTestRunner(runner) })

		value, err := runner.Run(context.Background(), RunOptions{})
		if err != nil {
			t.Fatalf("Run: %v", err)
		}
		if got := value.Raw(); !reflect.DeepEqual(got, []byte("hello")) {
			t.Fatalf("result = %#v, want []byte(%q)", got, "hello")
		}
	})

	t.Run("input", func(t *testing.T) {
		runner, err := New(`len(data)`, CompileOptions{ScriptName: "bytes.py", Inputs: []string{"data"}})
		if err != nil {
			t.Fatalf("New: %v", err)
		}
		t.Cleanup(func() { closeTestRunner(runner) })

		value, err := runner.Run(context.Background(), RunOptions{Inputs: map[string]Value{"data": Bytes([]byte("hello"))}})
		if err != nil {
			t.Fatalf("Run: %v", err)
		}
		if got := value.Raw(); got != int64(5) {
			t.Fatalf("result = %#v, want 5", got)
		}
	})

	t.Run("callback argument", func(t *testing.T) {
		runner, err := New(`consume(b"hello")`, CompileOptions{ScriptName: "bytes.py"})
		if err != nil {
			t.Fatalf("New: %v", err)
		}
		t.Cleanup(func() { closeTestRunner(runner) })

		value, err := runner.Run(context.Background(), RunOptions{Functions: map[string]ExternalFunction{
			"consume": func(_ context.Context, call Call) (Result, error) {
				if len(call.Args) != 1 || !reflect.DeepEqual(call.Args[0].Raw(), []byte("hello")) {
					t.Errorf("callback args = %#v, want one []byte(%q)", call.Args, "hello")
				}
				return Return(Bytes([]byte("world"))), nil
			},
		}})
		if err != nil {
			t.Fatalf("Run: %v", err)
		}
		if got := value.Raw(); !reflect.DeepEqual(got, []byte("world")) {
			t.Fatalf("callback result = %#v, want []byte(%q)", got, "world")
		}
	})
}

func TestRunnerRunCapturesInitialPrint(t *testing.T) {
	runner, err := New("print('hello')", CompileOptions{ScriptName: "probe.py"})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() {
		closeTestRunner(runner)
	})

	var out strings.Builder
	value, err := runner.Run(context.Background(), RunOptions{
		Print: WriterPrintCallback(&out),
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	if got := out.String(); got != "hello\n" {
		t.Fatalf("unexpected print output: got %q want %q", got, "hello\n")
	}
	if value.Kind() != valueKindNone {
		t.Fatalf("unexpected result kind: got %v want None", value.Kind())
	}
}

func TestRunnerRunCapturesInitialPrintBeforeExternalDispatch(t *testing.T) {
	runner, err := New("print('before')\next()", CompileOptions{ScriptName: "probe.py"})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() {
		closeTestRunner(runner)
	})

	var out strings.Builder
	calls := 0
	value, err := runner.Run(context.Background(), RunOptions{
		Print: WriterPrintCallback(&out),
		Functions: map[string]ExternalFunction{
			"ext": func(context.Context, Call) (Result, error) {
				calls++
				return Return(Int(7)), nil
			},
		},
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	if got := out.String(); got != "before\n" {
		t.Fatalf("unexpected print output: got %q want %q", got, "before\n")
	}
	if calls != 1 {
		t.Fatalf("unexpected external call count: got %d want %d", calls, 1)
	}
	got, ok := value.Raw().(int64)
	if !ok || got != 7 {
		t.Fatalf("unexpected result: got %#v want %d", value.Raw(), 7)
	}
}

func TestRunnerRunCapturesPrintAfterResume(t *testing.T) {
	runner, err := New("ext()\nprint('after')", CompileOptions{ScriptName: "probe.py"})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() {
		closeTestRunner(runner)
	})

	var out strings.Builder
	calls := 0
	value, err := runner.Run(context.Background(), RunOptions{
		Print: WriterPrintCallback(&out),
		Functions: map[string]ExternalFunction{
			"ext": func(context.Context, Call) (Result, error) {
				calls++
				return Return(None()), nil
			},
		},
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	if got := out.String(); got != "after\n" {
		t.Fatalf("unexpected print output: got %q want %q", got, "after\n")
	}
	if calls != 1 {
		t.Fatalf("unexpected external call count: got %d want %d", calls, 1)
	}
	if value.Kind() != valueKindNone {
		t.Fatalf("unexpected result kind: got %v want None", value.Kind())
	}
}

func TestReplFeedRunCapturesInitialPrint(t *testing.T) {
	repl, err := NewRepl(ReplOptions{ScriptName: "probe.py"})
	if err != nil {
		t.Fatalf("NewRepl: %v", err)
	}
	t.Cleanup(func() {
		closeTestRepl(repl)
	})

	var out strings.Builder
	value, err := repl.FeedRun(context.Background(), "print('hello')", FeedOptions{
		Print: WriterPrintCallback(&out),
	})
	if err != nil {
		t.Fatalf("FeedRun: %v", err)
	}

	if got := out.String(); got != "hello\n" {
		t.Fatalf("unexpected print output: got %q want %q", got, "hello\n")
	}
	if value.Kind() != valueKindNone {
		t.Fatalf("unexpected result kind: got %v want None", value.Kind())
	}
}
