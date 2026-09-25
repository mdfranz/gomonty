package monty_test

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	monty "github.com/mdfranz/gomonty"
)

// spanEvent records one Start/End pair observed by recordingHandler.
type spanEvent struct {
	kind      string // "execution", "callback", or "wait"
	id        int
	parentID  int // 0 means no parent (root)
	ended     bool
	err       error
	timing    monty.ExecutionTiming // only set for kind == "execution"
	arguments monty.TruncatedPayload
	output    monty.TruncatedPayload
}

type ctxSpanIDKey struct{}

func parentIDFromCtx(ctx context.Context) int {
	id, _ := ctx.Value(ctxSpanIDKey{}).(int)
	return id
}

// recordingHandler is a TelemetryHandler that records every span it's
// asked to start/end, including each span's parent id (read from the
// context it was started with) and whether End was called. This is what
// makes the sibling-vs-chained hierarchy assertions in this file possible.
type recordingHandler struct {
	mu     sync.Mutex
	nextID int
	events []*spanEvent
	prints []string
}

func (h *recordingHandler) record(kind string, ctx context.Context) (context.Context, *spanEvent) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.nextID++
	ev := &spanEvent{kind: kind, id: h.nextID, parentID: parentIDFromCtx(ctx)}
	h.events = append(h.events, ev)
	return context.WithValue(ctx, ctxSpanIDKey{}, ev.id), ev
}

func (h *recordingHandler) end(ev *spanEvent, err error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if ev.ended {
		panic(fmt.Sprintf("span %d (%s) ended twice", ev.id, ev.kind))
	}
	ev.ended = true
	ev.err = err
}

func (h *recordingHandler) StartExecution(ctx context.Context, info monty.ExecutionInfo) (context.Context, monty.ExecutionSpan) {
	ctx, ev := h.record("execution", ctx)
	ev.arguments = info.Inputs
	return ctx, executionSpan{h: h, ev: ev}
}

func (h *recordingHandler) StartCallback(ctx context.Context, info monty.CallbackInfo) (context.Context, monty.CallbackSpan) {
	ctx, ev := h.record("callback", ctx)
	ev.arguments = info.Arguments
	return ctx, callbackSpan{h: h, ev: ev}
}

func (h *recordingHandler) StartWait(ctx context.Context, _ monty.WaitInfo) (context.Context, monty.WaitSpan) {
	ctx, ev := h.record("wait", ctx)
	return ctx, waitSpan{h: h, ev: ev}
}

func (h *recordingHandler) RecordPrint(ctx context.Context, text string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.nextID++
	// Appended into the same ordered h.events timeline as spans (not just
	// h.prints) so tests can assert RecordPrint's position relative to
	// callback/wait spans, not just that it fired at all.
	ev := &spanEvent{
		kind:     "print",
		id:       h.nextID,
		parentID: parentIDFromCtx(ctx),
		ended:    true,
		output:   monty.TruncatedPayload{Text: text},
	}
	h.events = append(h.events, ev)
	h.prints = append(h.prints, text)
}

type executionSpan struct {
	h  *recordingHandler
	ev *spanEvent
}

func (s executionSpan) End(_ monty.Value, err error, timing monty.ExecutionTiming, output monty.TruncatedPayload) {
	s.h.mu.Lock()
	s.ev.timing = timing
	s.ev.output = output
	s.h.mu.Unlock()
	s.h.end(s.ev, err)
}

type callbackSpan struct {
	h  *recordingHandler
	ev *spanEvent
}

func (s callbackSpan) End(_ monty.Result, err error, output monty.TruncatedPayload) {
	s.h.mu.Lock()
	s.ev.output = output
	s.h.mu.Unlock()
	s.h.end(s.ev, err)
}

type waitSpan struct {
	h  *recordingHandler
	ev *spanEvent
}

func (s waitSpan) End(err error) {
	s.h.end(s.ev, err)
}

func (h *recordingHandler) executionEvent(t *testing.T) *spanEvent {
	t.Helper()
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, ev := range h.events {
		if ev.kind == "execution" {
			return ev
		}
	}
	t.Fatal("no execution span recorded")
	return nil
}

func (h *recordingHandler) callbackEvents() []*spanEvent {
	h.mu.Lock()
	defer h.mu.Unlock()
	var out []*spanEvent
	for _, ev := range h.events {
		if ev.kind == "callback" {
			out = append(out, ev)
		}
	}
	return out
}

func TestTelemetryCallbackSpansAreSiblingsNotChained(t *testing.T) {
	runner, err := monty.New(`
host_value()
host_value()
host_value()
`, monty.CompileOptions{ScriptName: "siblings.py"})
	if err != nil {
		t.Fatalf("compile: %v", err)
	}

	handler := &recordingHandler{}
	value, err := runner.Run(context.Background(), monty.RunOptions{
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
	if got, ok := value.Raw().(int64); !ok || got != 1 {
		t.Fatalf("unexpected result: %#v", value.Raw())
	}

	execEv := handler.executionEvent(t)
	callbacks := handler.callbackEvents()
	if len(callbacks) != 3 {
		t.Fatalf("expected 3 callback spans, got %d", len(callbacks))
	}
	for _, cb := range callbacks {
		if cb.parentID != execEv.id {
			t.Errorf("callback span %d has parent %d, want execution span %d (siblings, not chained)", cb.id, cb.parentID, execEv.id)
		}
		if !cb.ended {
			t.Errorf("callback span %d was never ended", cb.id)
		}
	}
	if !execEv.ended {
		t.Fatal("execution span was never ended")
	}
	if execEv.err != nil {
		t.Fatalf("execution span ended with unexpected error: %v", execEv.err)
	}
}

func TestTelemetryTimingBreakdownIncludesCallbackDelay(t *testing.T) {
	const delay = 30 * time.Millisecond

	runner, err := monty.New(`host_slow()`, monty.CompileOptions{ScriptName: "timing.py"})
	if err != nil {
		t.Fatalf("compile: %v", err)
	}

	handler := &recordingHandler{}
	_, err = runner.Run(context.Background(), monty.RunOptions{
		Telemetry: handler,
		Functions: map[string]monty.ExternalFunction{
			"host_slow": func(context.Context, monty.Call) (monty.Result, error) {
				time.Sleep(delay)
				return monty.Return(monty.None()), nil
			},
		},
	})
	if err != nil {
		t.Fatalf("run: %v", err)
	}

	timing := handler.executionEvent(t).timing
	if timing.Callback < delay {
		t.Errorf("Callback duration %v is less than the synthetic delay %v", timing.Callback, delay)
	}
	if timing.Total < timing.Callback {
		t.Errorf("Total %v is less than Callback %v", timing.Total, timing.Callback)
	}
	if got := timing.Python(); got < 0 {
		t.Errorf("Python() = %v, want >= 0 (Total - Callback - Wait)", got)
	}
}

func TestTelemetryExecutionSpanReflectsRuntimeError(t *testing.T) {
	runner, err := monty.New(`raise ValueError("boom")`, monty.CompileOptions{ScriptName: "error.py"})
	if err != nil {
		t.Fatalf("compile: %v", err)
	}

	handler := &recordingHandler{}
	_, runErr := runner.Run(context.Background(), monty.RunOptions{Telemetry: handler})
	if runErr == nil {
		t.Fatal("expected a runtime error")
	}

	ev := handler.executionEvent(t)
	if !ev.ended {
		t.Fatal("execution span was never ended")
	}
	if ev.err == nil {
		t.Fatal("execution span ended with a nil error despite the script raising")
	}
}

func TestTelemetryExecutionSpanReflectsFeedRunSyntaxError(t *testing.T) {
	repl, err := monty.NewRepl(monty.ReplOptions{ScriptName: "syntax.py"})
	if err != nil {
		t.Fatalf("new repl: %v", err)
	}

	handler := &recordingHandler{}
	_, runErr := repl.FeedRun(context.Background(), `def (((`, monty.FeedOptions{Telemetry: handler})
	if runErr == nil {
		t.Fatal("expected a syntax error")
	}

	ev := handler.executionEvent(t)
	if !ev.ended {
		t.Fatal("execution span was never ended")
	}
	if ev.err == nil {
		t.Fatal("execution span ended with a nil error despite the syntax error")
	}
}

func TestTelemetryExecutionSpanReflectsContextCancellation(t *testing.T) {
	runner, err := monty.New(`host_wait()`, monty.CompileOptions{ScriptName: "cancel.py"})
	if err != nil {
		t.Fatalf("compile: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	handler := &recordingHandler{}
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

	ev := handler.executionEvent(t)
	if !ev.ended {
		t.Fatal("execution span was never ended")
	}
	if !errors.Is(ev.err, context.Canceled) {
		t.Fatalf("execution span ended with %v, want context.Canceled", ev.err)
	}
}

// panickingHandler panics in exactly one of its four Run-reachable methods
// (StartExecution, ExecutionSpan.End, StartCallback, CallbackSpan.End —
// StartWait/WaitSpan.End are covered directly in dispatch_test.go, since
// this codebase has no verified live-script trigger for the future-wait
// path). Every other method behaves normally, so the script still runs to
// completion around the one deliberately broken call site.
type panickingHandler struct {
	panicIn string
	onPanic func(recovered any)
}

func (h panickingHandler) StartExecution(ctx context.Context, _ monty.ExecutionInfo) (context.Context, monty.ExecutionSpan) {
	if h.panicIn == "StartExecution" {
		panic("boom: StartExecution")
	}
	return ctx, panickingExecutionSpan(h)
}

func (h panickingHandler) StartCallback(ctx context.Context, _ monty.CallbackInfo) (context.Context, monty.CallbackSpan) {
	if h.panicIn == "StartCallback" {
		panic("boom: StartCallback")
	}
	return ctx, panickingCallbackSpan(h)
}

func (h panickingHandler) StartWait(ctx context.Context, _ monty.WaitInfo) (context.Context, monty.WaitSpan) {
	return ctx, nil
}

func (h panickingHandler) RecordPrint(context.Context, string) {}

type panickingExecutionSpan panickingHandler

func (s panickingExecutionSpan) End(monty.Value, error, monty.ExecutionTiming, monty.TruncatedPayload) {
	if s.panicIn == "End" {
		panic("boom: ExecutionSpan.End")
	}
}

type panickingCallbackSpan panickingHandler

func (s panickingCallbackSpan) End(monty.Result, error, monty.TruncatedPayload) {
	if s.panicIn == "CallbackEnd" {
		panic("boom: CallbackSpan.End")
	}
}

func TestTelemetryPanicNeverBreaksScriptExecution(t *testing.T) {
	for _, panicIn := range []string{"StartExecution", "End", "StartCallback", "CallbackEnd"} {
		t.Run(panicIn, func(t *testing.T) {
			runner, err := monty.New(`host_value()`, monty.CompileOptions{ScriptName: "panic.py"})
			if err != nil {
				t.Fatalf("compile: %v", err)
			}

			var recovered []any
			var mu sync.Mutex
			handler := panickingHandler{
				panicIn: panicIn,
				onPanic: func(r any) {
					mu.Lock()
					recovered = append(recovered, r)
					mu.Unlock()
				},
			}

			value, runErr := runner.Run(context.Background(), monty.RunOptions{
				Telemetry:        handler,
				TelemetryOptions: monty.TelemetryOptions{PanicHandler: handler.onPanic},
				Functions: map[string]monty.ExternalFunction{
					"host_value": func(context.Context, monty.Call) (monty.Result, error) {
						return monty.Return(monty.Int(1)), nil
					},
				},
			})
			if runErr != nil {
				t.Fatalf("run: %v (panic in %s must not disrupt execution)", runErr, panicIn)
			}
			if got, ok := value.Raw().(int64); !ok || got != 1 {
				t.Fatalf("unexpected result: %#v", value.Raw())
			}

			mu.Lock()
			defer mu.Unlock()
			if len(recovered) != 1 {
				t.Fatalf("expected exactly 1 recovered panic routed to PanicHandler, got %d: %v", len(recovered), recovered)
			}
		})
	}
}

func TestTelemetryNoPayloadRecordedByDefault(t *testing.T) {
	runner, err := monty.New(`host_echo(1, "secret", key="value")`, monty.CompileOptions{ScriptName: "no-payload.py"})
	if err != nil {
		t.Fatalf("compile: %v", err)
	}

	handler := &recordingHandler{}
	_, runErr := runner.Run(context.Background(), monty.RunOptions{
		Telemetry: handler,
		// TelemetryOptions left at its zero value: RecordArguments and
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

	handler.mu.Lock()
	defer handler.mu.Unlock()
	for _, ev := range handler.events {
		if ev.kind == "print" {
			continue
		}
		if ev.arguments != (monty.TruncatedPayload{}) {
			t.Errorf("span %d (%s) has non-zero arguments despite RecordArguments being unset: %#v", ev.id, ev.kind, ev.arguments)
		}
		if ev.output != (monty.TruncatedPayload{}) {
			t.Errorf("span %d (%s) has non-zero output despite RecordOutputs being unset: %#v", ev.id, ev.kind, ev.output)
		}
	}
}

func TestTelemetryPayloadTruncation(t *testing.T) {
	// "x" * 50 renders (with quotes) to well over 20 bytes, so this forces
	// truncation with a small MaxAttributeBytes; the plain small argument
	// forces the non-truncated, under-the-limit path in the same run.
	runner, err := monty.New(`
host_call("short")
host_call("x" * 50)
`, monty.CompileOptions{ScriptName: "truncation.py"})
	if err != nil {
		t.Fatalf("compile: %v", err)
	}

	handler := &recordingHandler{}
	_, runErr := runner.Run(context.Background(), monty.RunOptions{
		Telemetry: handler,
		TelemetryOptions: monty.TelemetryOptions{
			RecordArguments:   true,
			RecordOutputs:     true,
			MaxAttributeBytes: 20,
		},
		Functions: map[string]monty.ExternalFunction{
			"host_call": func(_ context.Context, call monty.Call) (monty.Result, error) {
				return monty.Return(call.Args[0]), nil
			},
		},
	})
	if runErr != nil {
		t.Fatalf("run: %v", runErr)
	}

	callbacks := handler.callbackEvents()
	if len(callbacks) != 2 {
		t.Fatalf("expected 2 callback spans, got %d", len(callbacks))
	}

	short, long := callbacks[0], callbacks[1]
	if short.arguments.Truncated {
		t.Errorf("short call's arguments should round-trip untruncated, got %#v", short.arguments)
	}
	if len(short.arguments.Text) == 0 {
		t.Error("short call's arguments.Text is empty")
	}
	if short.output.Truncated {
		t.Errorf("short call's output should round-trip untruncated, got %#v", short.output)
	}

	if !long.arguments.Truncated {
		t.Errorf("long call's arguments should be truncated, got %#v", long.arguments)
	}
	if len(long.arguments.Text) > 20 {
		t.Errorf("long call's arguments.Text exceeds MaxAttributeBytes: %d bytes", len(long.arguments.Text))
	}
	if !long.output.Truncated {
		t.Errorf("long call's output should be truncated, got %#v", long.output)
	}
	if len(long.output.Text) > 20 {
		t.Errorf("long call's output.Text exceeds MaxAttributeBytes: %d bytes", len(long.output.Text))
	}
}

func TestTelemetryPrintInterleavedWithCallbacks(t *testing.T) {
	runner, err := monty.New(`
print("before")
host_value()
print("after")
`, monty.CompileOptions{ScriptName: "print-interleave.py"})
	if err != nil {
		t.Fatalf("compile: %v", err)
	}

	var printed []string
	handler := &recordingHandler{}
	_, runErr := runner.Run(context.Background(), monty.RunOptions{
		Telemetry: handler,
		Print: func(stream, text string) {
			printed = append(printed, text)
		},
		Functions: map[string]monty.ExternalFunction{
			"host_value": func(context.Context, monty.Call) (monty.Result, error) {
				return monty.Return(monty.Int(1)), nil
			},
		},
	})
	if runErr != nil {
		t.Fatalf("run: %v", runErr)
	}

	if len(printed) == 0 {
		t.Fatal("opts.Print never fired")
	}

	handler.mu.Lock()
	defer handler.mu.Unlock()
	if len(handler.prints) == 0 {
		t.Fatal("RecordPrint never fired")
	}

	// Confirm ordering: the first print event precedes the callback span,
	// which precedes the last print event, in the unified events timeline.
	var firstPrintIdx, callbackIdx, lastPrintIdx = -1, -1, -1
	for i, ev := range handler.events {
		switch ev.kind {
		case "print":
			if firstPrintIdx == -1 {
				firstPrintIdx = i
			}
			lastPrintIdx = i
		case "callback":
			callbackIdx = i
		}
	}
	if firstPrintIdx == -1 || callbackIdx == -1 || lastPrintIdx == -1 {
		t.Fatalf("missing expected event kinds in timeline: %+v", handler.events)
	}
	if !(firstPrintIdx < callbackIdx && callbackIdx < lastPrintIdx) {
		t.Errorf("expected print, then callback, then print order; got indices print=%d callback=%d print=%d", firstPrintIdx, callbackIdx, lastPrintIdx)
	}
}
