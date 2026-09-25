package otelmonty_test

import (
	"context"
	"errors"
	"testing"

	monty "github.com/ewhauser/gomonty"
	"github.com/ewhauser/gomonty/otelmonty"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
)

// newTestHandler returns an otelmonty.Handler wired to an in-memory span
// recorder, plus the recorder itself for assertions.
func newTestHandler() (otelmonty.Handler, *tracetest.SpanRecorder) {
	recorder := tracetest.NewSpanRecorder()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(recorder))
	return otelmonty.Handler{Tracer: tp.Tracer("test")}, recorder
}

func findSpan(spans []sdktrace.ReadOnlySpan, name string) sdktrace.ReadOnlySpan {
	for _, s := range spans {
		if s.Name() == name {
			return s
		}
	}
	return nil
}

func attrValue(span sdktrace.ReadOnlySpan, key string) (string, bool) {
	for _, a := range span.Attributes() {
		if string(a.Key) == key {
			return a.Value.Emit(), true
		}
	}
	return "", false
}

func TestHandlerSuccessfulRun(t *testing.T) {
	runner, err := monty.New(`host_value()`, monty.CompileOptions{ScriptName: "ok.py"})
	if err != nil {
		t.Fatalf("compile: %v", err)
	}

	handler, recorder := newTestHandler()
	_, err = runner.Run(context.Background(), monty.RunOptions{
		Telemetry: handler,
		Functions: map[string]monty.ExternalFunction{
			"host_value": func(context.Context, monty.Call) (monty.Result, error) {
				return monty.Return(monty.Int(1)), nil
			},
		},
	})
	if err != nil {
		t.Fatalf("run: %v", err)
	}

	spans := recorder.Ended()
	root := findSpan(spans, "monty.run")
	if root == nil {
		t.Fatal("no monty.run span recorded")
	}
	if root.Status().Code.String() == "Error" {
		t.Errorf("successful run got Error status: %+v", root.Status())
	}
	if script, ok := attrValue(root, "monty.script_name"); !ok || script != "ok.py" {
		t.Errorf("monty.script_name = %q, %v, want \"ok.py\", true", script, ok)
	}

	call := findSpan(spans, "monty.call")
	if call == nil {
		t.Fatal("no monty.call span recorded")
	}
	if call.Parent().SpanID() != root.SpanContext().SpanID() {
		t.Errorf("monty.call parent = %v, want root span %v", call.Parent().SpanID(), root.SpanContext().SpanID())
	}
}

func TestHandlerChildSpanAttachesUnderCallback(t *testing.T) {
	runner, err := monty.New(`host_value()`, monty.CompileOptions{ScriptName: "child.py"})
	if err != nil {
		t.Fatalf("compile: %v", err)
	}

	handler, recorder := newTestHandler()
	_, err = runner.Run(context.Background(), monty.RunOptions{
		Telemetry: handler,
		Functions: map[string]monty.ExternalFunction{
			"host_value": func(ctx context.Context, _ monty.Call) (monty.Result, error) {
				_, span := handler.Tracer.Start(ctx, "downstream.http")
				span.End()
				return monty.Return(monty.Int(1)), nil
			},
		},
	})
	if err != nil {
		t.Fatalf("run: %v", err)
	}

	spans := recorder.Ended()
	call := findSpan(spans, "monty.call")
	downstream := findSpan(spans, "downstream.http")
	if call == nil || downstream == nil {
		t.Fatalf("expected monty.call and downstream.http spans, got %d spans", len(spans))
	}
	if downstream.Parent().SpanID() != call.SpanContext().SpanID() {
		t.Errorf("downstream.http parent = %v, want monty.call %v", downstream.Parent().SpanID(), call.SpanContext().SpanID())
	}
}

func TestHandlerRuntimeErrorSetsErrorStatus(t *testing.T) {
	runner, err := monty.New(`raise ValueError("boom")`, monty.CompileOptions{ScriptName: "error.py"})
	if err != nil {
		t.Fatalf("compile: %v", err)
	}

	handler, recorder := newTestHandler()
	_, runErr := runner.Run(context.Background(), monty.RunOptions{Telemetry: handler})
	if runErr == nil {
		t.Fatal("expected a runtime error")
	}

	root := findSpan(recorder.Ended(), "monty.run")
	if root == nil {
		t.Fatal("no monty.run span recorded")
	}
	if root.Status().Code.String() != "Error" {
		t.Errorf("root span status = %v, want Error", root.Status())
	}
	if len(root.Events()) == 0 {
		t.Error("expected span.RecordError to add an exception event")
	}
}

func TestHandlerContextCancellation(t *testing.T) {
	runner, err := monty.New(`host_wait()`, monty.CompileOptions{ScriptName: "cancel.py"})
	if err != nil {
		t.Fatalf("compile: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	handler, recorder := newTestHandler()
	_, runErr := runner.Run(ctx, monty.RunOptions{
		Telemetry: handler,
		Functions: map[string]monty.ExternalFunction{
			"host_wait": func(ctx context.Context, _ monty.Call) (monty.Result, error) {
				cancel()
				<-ctx.Done()
				return monty.Result{}, ctx.Err()
			},
		},
	})
	if !errors.Is(runErr, context.Canceled) {
		t.Fatalf("expected context.Canceled, got %v", runErr)
	}

	root := findSpan(recorder.Ended(), "monty.run")
	if root == nil {
		t.Fatal("no monty.run span recorded")
	}
	if root.Status().Code.String() != "Error" {
		t.Errorf("root span status = %v, want Error", root.Status())
	}
}

func TestHandlerNoPayloadRecordedByDefault(t *testing.T) {
	runner, err := monty.New(`host_echo("secret")`, monty.CompileOptions{ScriptName: "no-payload.py"})
	if err != nil {
		t.Fatalf("compile: %v", err)
	}

	handler, recorder := newTestHandler()
	_, runErr := runner.Run(context.Background(), monty.RunOptions{
		Telemetry: handler,
		// TelemetryOptions left at its zero value: RecordArguments/
		// RecordOutputs both default to false.
		Inputs: map[string]monty.Value{"password": monty.String("hunter2")},
		Functions: map[string]monty.ExternalFunction{
			"host_echo": func(context.Context, monty.Call) (monty.Result, error) {
				return monty.Return(monty.String("also-secret")), nil
			},
		},
	})
	if runErr != nil {
		t.Fatalf("run: %v", runErr)
	}

	for _, span := range recorder.Ended() {
		for _, key := range []string{"monty.inputs", "monty.arguments", "monty.output"} {
			if v, ok := attrValue(span, key); ok {
				t.Errorf("span %s has %s = %q despite RecordArguments/RecordOutputs being unset", span.Name(), key, v)
			}
		}
	}
}

func TestHandlerPayloadOptIn(t *testing.T) {
	runner, err := monty.New(`host_echo("hello")`, monty.CompileOptions{ScriptName: "payload.py"})
	if err != nil {
		t.Fatalf("compile: %v", err)
	}

	handler, recorder := newTestHandler()
	_, runErr := runner.Run(context.Background(), monty.RunOptions{
		Telemetry: handler,
		TelemetryOptions: monty.TelemetryOptions{
			RecordArguments: true,
			RecordOutputs:   true,
		},
		Functions: map[string]monty.ExternalFunction{
			"host_echo": func(_ context.Context, call monty.Call) (monty.Result, error) {
				return monty.Return(call.Args[0]), nil
			},
		},
	})
	if runErr != nil {
		t.Fatalf("run: %v", runErr)
	}

	call := findSpan(recorder.Ended(), "monty.call")
	if call == nil {
		t.Fatal("no monty.call span recorded")
	}
	if _, ok := attrValue(call, "monty.arguments"); !ok {
		t.Error("expected monty.arguments to be set with RecordArguments enabled")
	}
	if _, ok := attrValue(call, "monty.output"); !ok {
		t.Error("expected monty.output to be set with RecordOutputs enabled")
	}
}

func TestHandlerCallbackExceptionSetsErrorStatus(t *testing.T) {
	runner, err := monty.New(`host_raise()`, monty.CompileOptions{ScriptName: "callback-exception.py"})
	if err != nil {
		t.Fatalf("compile: %v", err)
	}

	handler, recorder := newTestHandler()
	arg := "bad input"
	_, runErr := runner.Run(context.Background(), monty.RunOptions{
		Telemetry: handler,
		Functions: map[string]monty.ExternalFunction{
			"host_raise": func(context.Context, monty.Call) (monty.Result, error) {
				return monty.Raise(monty.Exception{Type: "ValueError", Arg: &arg}), nil
			},
		},
	})
	if runErr == nil {
		t.Fatal("expected the raised exception to surface as a run error")
	}

	call := findSpan(recorder.Ended(), "monty.call")
	if call == nil {
		t.Fatal("no monty.call span recorded")
	}
	if call.Status().Code.String() != "Error" {
		t.Errorf("monty.call status = %v, want Error for a raised exception", call.Status())
	}
	if excType, ok := attrValue(call, "monty.exception_type"); !ok || excType != "ValueError" {
		t.Errorf("monty.exception_type = %q, %v, want \"ValueError\", true", excType, ok)
	}
}

// StartWait/WaitSpan.End are exercised by calling Handler's exported methods
// directly rather than through Runner.Run: there's no verified live-script
// trigger for the future-wait dispatch path from outside the core module
// (see telemetry_test.go's panickingHandler doc comment in the root
// package for the same constraint on the in-tree fake handlers), but
// StartWait/WaitSpan.End are ordinary exported methods, so a black-box
// unit test can invoke them without going through a real Run call at all.
func TestHandlerWaitSpanSuccess(t *testing.T) {
	handler, recorder := newTestHandler()
	ctx, span := handler.StartWait(context.Background(), monty.WaitInfo{PendingCallIDs: []uint32{7, 9}})
	span.End(nil)
	_ = ctx

	wait := findSpan(recorder.Ended(), "monty.wait")
	if wait == nil {
		t.Fatal("no monty.wait span recorded")
	}
	if wait.Status().Code.String() == "Error" {
		t.Errorf("successful wait got Error status: %+v", wait.Status())
	}
	if ids, ok := attrValue(wait, "monty.pending_call_ids"); !ok || ids != "[7,9]" {
		t.Errorf("monty.pending_call_ids = %q, %v, want \"[7,9]\", true", ids, ok)
	}
}

func TestHandlerWaitSpanFailure(t *testing.T) {
	handler, recorder := newTestHandler()
	_, span := handler.StartWait(context.Background(), monty.WaitInfo{PendingCallIDs: []uint32{1}})
	span.End(context.Canceled)

	wait := findSpan(recorder.Ended(), "monty.wait")
	if wait == nil {
		t.Fatal("no monty.wait span recorded")
	}
	if wait.Status().Code.String() != "Error" {
		t.Errorf("wait span status = %v, want Error", wait.Status())
	}
	if len(wait.Events()) == 0 {
		t.Error("expected span.RecordError to add an exception event")
	}
}

func TestHandlerRecordPrintAddsEventToActiveSpan(t *testing.T) {
	handler, recorder := newTestHandler()
	ctx, span := handler.Tracer.Start(context.Background(), "monty.run")
	handler.RecordPrint(ctx, "hello from python")
	span.End()

	root := findSpan(recorder.Ended(), "monty.run")
	if root == nil {
		t.Fatal("no monty.run span recorded")
	}
	events := root.Events()
	if len(events) != 1 || events[0].Name != "monty.print" {
		t.Fatalf("expected one monty.print event, got %+v", events)
	}
	found := false
	for _, a := range events[0].Attributes {
		if string(a.Key) == "monty.text" && a.Value.Emit() == "hello from python" {
			found = true
		}
	}
	if !found {
		t.Errorf("monty.print event missing monty.text attribute, got %+v", events[0].Attributes)
	}
}

func TestHandlerRecordPrintNoopWithoutActiveSpan(t *testing.T) {
	handler, _ := newTestHandler()
	// No panic, no span to attach to: RecordPrint must be a silent no-op.
	handler.RecordPrint(context.Background(), "orphaned print")
}

// TestHandlerCallbackSpanDirectError covers CallbackSpan.End's err != nil
// branch directly against the exported Handler/CallbackSpan methods. In
// practice dispatch.go's normalizeCallbackResult (dispatch.go:232-235)
// converts every error an ExternalFunction/OSHandler returns into a
// Raise()d Python exception before endCallbackSpan is ever called, so this
// branch is structurally unreachable through Runner.Run/Repl.FeedRun's
// public callback contract — the same reason slogCallbackSpan.End's err
// path is never exercised on the slog side either. TelemetryHandler's
// CallbackSpan.End still declares err error as part of its contract, so
// this is testing documented interface behavior, not dead code.
func TestHandlerCallbackSpanDirectError(t *testing.T) {
	handler, recorder := newTestHandler()
	ctx, span := handler.StartCallback(context.Background(), monty.CallbackInfo{FunctionName: "host_thing", CallID: 1})
	span.End(monty.Result{}, context.Canceled, monty.TruncatedPayload{})
	_ = ctx

	call := findSpan(recorder.Ended(), "monty.call")
	if call == nil {
		t.Fatal("no monty.call span recorded")
	}
	if call.Status().Code.String() != "Error" {
		t.Errorf("callback span status = %v, want Error", call.Status())
	}
	if len(call.Events()) == 0 {
		t.Error("expected span.RecordError to add an exception event")
	}
}

func TestHandlerZeroValueUsesDefaultTracer(t *testing.T) {
	var handler otelmonty.Handler // Tracer left nil: must fall back to otel.Tracer(instrumentationName).
	ctx, span := handler.StartExecution(context.Background(), monty.ExecutionInfo{ScriptName: "zero-value.py"})
	if span == nil {
		t.Fatal("StartExecution returned a nil span even with a zero-value Handler")
	}
	span.End(monty.Value{}, nil, monty.ExecutionTiming{}, monty.TruncatedPayload{})
	_ = ctx
}

func TestHandlerFeedRunUsesMontyFeedSpanName(t *testing.T) {
	repl, err := monty.NewRepl(monty.ReplOptions{ScriptName: "feed.py"})
	if err != nil {
		t.Fatalf("new repl: %v", err)
	}

	handler, recorder := newTestHandler()
	if _, err := repl.FeedRun(context.Background(), `1 + 1`, monty.FeedOptions{Telemetry: handler}); err != nil {
		t.Fatalf("feed run: %v", err)
	}

	if findSpan(recorder.Ended(), "monty.feed") == nil {
		t.Fatal("no monty.feed span recorded for Repl.FeedRun")
	}
}
