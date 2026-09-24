package monty_test

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	monty "github.com/ewhauser/gomonty"
)

// spanEvent records one Start/End pair observed by recordingHandler.
type spanEvent struct {
	kind     string // "execution", "callback", or "wait"
	id       int
	parentID int // 0 means no parent (root)
	ended    bool
	err      error
	timing   monty.ExecutionTiming // only set for kind == "execution"
	prints   []string
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
	mu       sync.Mutex
	nextID   int
	events   []*spanEvent
	prints   []string
	printCtx context.Context
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

func (h *recordingHandler) StartExecution(ctx context.Context, _ monty.ExecutionInfo) (context.Context, monty.ExecutionSpan) {
	ctx, ev := h.record("execution", ctx)
	return ctx, executionSpan{h: h, ev: ev}
}

func (h *recordingHandler) StartCallback(ctx context.Context, _ monty.CallbackInfo) (context.Context, monty.CallbackSpan) {
	ctx, ev := h.record("callback", ctx)
	return ctx, callbackSpan{h: h, ev: ev}
}

func (h *recordingHandler) StartWait(ctx context.Context, _ monty.WaitInfo) (context.Context, monty.WaitSpan) {
	ctx, ev := h.record("wait", ctx)
	return ctx, waitSpan{h: h, ev: ev}
}

func (h *recordingHandler) RecordPrint(_ context.Context, text string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.prints = append(h.prints, text)
}

type executionSpan struct {
	h  *recordingHandler
	ev *spanEvent
}

func (s executionSpan) End(_ monty.Value, err error, timing monty.ExecutionTiming) {
	s.h.mu.Lock()
	s.ev.timing = timing
	s.h.mu.Unlock()
	s.h.end(s.ev, err)
}

type callbackSpan struct {
	h  *recordingHandler
	ev *spanEvent
}

func (s callbackSpan) End(_ monty.Result, err error) {
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
