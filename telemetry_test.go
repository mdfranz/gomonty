package monty_test

import (
	"context"
	"testing"

	monty "github.com/ewhauser/gomonty"
)

// panicTelemetryHandler fails the test if any method is called. Issue #10
// only wires TelemetryHandler through as far as dispatchConfig — no call
// site invokes it yet — so a configured handler must be fully inert.
type panicTelemetryHandler struct{ t *testing.T }

func (h panicTelemetryHandler) StartExecution(ctx context.Context, _ monty.ExecutionInfo) (context.Context, monty.ExecutionSpan) {
	h.t.Fatal("StartExecution must not be called yet (issue #10 is plumbing-only)")
	return ctx, nil
}

func (h panicTelemetryHandler) StartCallback(ctx context.Context, _ monty.CallbackInfo) (context.Context, monty.CallbackSpan) {
	h.t.Fatal("StartCallback must not be called yet (issue #10 is plumbing-only)")
	return ctx, nil
}

func (h panicTelemetryHandler) StartWait(ctx context.Context, _ monty.WaitInfo) (context.Context, monty.WaitSpan) {
	h.t.Fatal("StartWait must not be called yet (issue #10 is plumbing-only)")
	return ctx, nil
}

func (h panicTelemetryHandler) RecordPrint(_ context.Context, _ string) {
	h.t.Fatal("RecordPrint must not be called yet (issue #10 is plumbing-only)")
}

func TestTelemetryHandlerNotYetInvoked(t *testing.T) {
	runner, err := monty.New("40 + 2", monty.CompileOptions{ScriptName: "telemetry_test.py"})
	if err != nil {
		t.Fatalf("compile: %v", err)
	}

	value, err := runner.Run(context.Background(), monty.RunOptions{
		Telemetry: panicTelemetryHandler{t: t},
	})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if got, ok := value.Raw().(int64); !ok || got != 42 {
		t.Fatalf("unexpected result: %#v", value.Raw())
	}
}

func TestFeedOptionsTelemetryField(t *testing.T) {
	repl, err := monty.NewRepl(monty.ReplOptions{ScriptName: "telemetry_test.py"})
	if err != nil {
		t.Fatalf("new repl: %v", err)
	}

	value, err := repl.FeedRun(context.Background(), "40 + 2", monty.FeedOptions{
		Telemetry: panicTelemetryHandler{t: t},
	})
	if err != nil {
		t.Fatalf("feed run: %v", err)
	}
	if got, ok := value.Raw().(int64); !ok || got != 42 {
		t.Fatalf("unexpected result: %#v", value.Raw())
	}
}
