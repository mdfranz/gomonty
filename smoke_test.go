package monty

import (
	"bytes"
	"context"
	"testing"
)

// TestPlatformSmoke exercises the core native FFI behaviors (eval, binary bytes
// round-trip across the wire, host function callback, default time.sleep, and
// a persistent REPL session) so platform-specific jobs (Windows, musl, release
// artifact smoke checks) can validate the shared library quickly at runtime.
func TestPlatformSmoke(t *testing.T) {
	t.Run("eval bytes round-trip and host callback", func(t *testing.T) {
		input := []byte{0x00, 0x01, 0xfe, 0xff, 'g', 'o'}
		runner, err := New(
			`(echo_tag(payload), payload + b"-ok")`,
			CompileOptions{
				ScriptName: "smoke_eval.py",
				Inputs:     []string{"payload"},
			},
		)
		if err != nil {
			t.Fatalf("New: %v", err)
		}
		defer closeTestRunner(runner)

		called := false
		res, err := runner.Run(context.Background(), RunOptions{
			Inputs: map[string]Value{"payload": Bytes(input)},
			Functions: map[string]ExternalFunction{
				"echo_tag": func(_ context.Context, call Call) (Result, error) {
					called = true
					if len(call.Args) != 1 {
						t.Fatalf("echo_tag args = %d, want 1", len(call.Args))
					}
					got, ok := call.Args[0].Raw().([]byte)
					if !ok || !bytes.Equal(got, input) {
						t.Fatalf("echo_tag arg = %v (%T), want %v", call.Args[0].Raw(), call.Args[0].Raw(), input)
					}
					return Return(Int(int64(len(got)))), nil
				},
			},
		})
		if err != nil {
			t.Fatalf("Run: %v", err)
		}
		if !called {
			t.Fatal("host function echo_tag was not called")
		}

		items, ok := res.Raw().(Tuple)
		if !ok || len(items) != 2 {
			t.Fatalf("result = %v (%T), want 2-element Tuple", res.Raw(), res.Raw())
		}
		if n, ok := items[0].Raw().(int64); !ok || n != int64(len(input)) {
			t.Fatalf("tuple[0] = %v (%T), want int64(%d)", items[0].Raw(), items[0].Raw(), len(input))
		}
		wantBytes := []byte{0x00, 0x01, 0xfe, 0xff, 'g', 'o', '-', 'o', 'k'}
		if gotBytes, ok := items[1].Raw().([]byte); !ok || !bytes.Equal(gotBytes, wantBytes) {
			t.Fatalf("tuple[1] = %v (%T), want %v", items[1].Raw(), items[1].Raw(), wantBytes)
		}
	})

	t.Run("default time.sleep", func(t *testing.T) {
		runner, err := New("import time\ntime.sleep(0.01)\n'slept'", CompileOptions{ScriptName: "smoke_sleep.py"})
		if err != nil {
			t.Fatalf("New: %v", err)
		}
		defer closeTestRunner(runner)

		got, err := runner.Run(context.Background(), RunOptions{})
		if err != nil {
			t.Fatalf("Run: %v", err)
		}
		if s, ok := got.Raw().(string); !ok || s != "slept" {
			t.Fatalf("got = %v (%T), want \"slept\"", got.Raw(), got.Raw())
		}
	})

	t.Run("repl session", func(t *testing.T) {
		repl, err := NewRepl(ReplOptions{ScriptName: "smoke_repl.py"})
		if err != nil {
			t.Fatalf("NewRepl: %v", err)
		}
		defer closeTestRepl(repl)

		if _, err := repl.FeedRun(context.Background(), `x = b"\x00\xff"`, FeedOptions{}); err != nil {
			t.Fatalf("FeedRun setup: %v", err)
		}
		got, err := repl.FeedRun(context.Background(), "len(x)", FeedOptions{})
		if err != nil {
			t.Fatalf("FeedRun len: %v", err)
		}
		if v, ok := got.Raw().(int64); !ok || v != 2 {
			t.Fatalf("got = %v (%T), want int64(2)", got.Raw(), got.Raw())
		}
	})
}
