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
