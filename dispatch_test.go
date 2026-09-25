package monty

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"testing"
)

// recordingWaitHandler is a minimal TelemetryHandler recording only
// StartWait/End calls, for testing waitForFutureResults in isolation
// without needing a real Python script that triggers Monty's async
// future-resolution path (unverified/untested territory in this
// codebase as of this test).
type recordingWaitHandler struct {
	mu      sync.Mutex
	started []WaitInfo
	ended   []error
}

func (h *recordingWaitHandler) StartExecution(ctx context.Context, _ ExecutionInfo) (context.Context, ExecutionSpan) {
	return ctx, nil
}

func (h *recordingWaitHandler) StartCallback(ctx context.Context, _ CallbackInfo) (context.Context, CallbackSpan) {
	return ctx, nil
}

func (h *recordingWaitHandler) StartWait(ctx context.Context, info WaitInfo) (context.Context, WaitSpan) {
	h.mu.Lock()
	h.started = append(h.started, info)
	h.mu.Unlock()
	return ctx, recordingWaitSpan{h: h}
}

func (h *recordingWaitHandler) RecordPrint(context.Context, string) {}

type recordingWaitSpan struct {
	h *recordingWaitHandler
}

func (s recordingWaitSpan) End(err error) {
	s.h.mu.Lock()
	s.h.ended = append(s.h.ended, err)
	s.h.mu.Unlock()
}

type fakeWaiter struct {
	result Result
}

func (w fakeWaiter) Wait(context.Context) Result {
	return w.result
}

func TestWaitForFutureResultsRecordsWaitSpan(t *testing.T) {
	handler := &recordingWaitHandler{}
	waiters := map[uint32]Waiter{
		7: fakeWaiter{result: Return(Int(42))},
	}

	results, err := waitForFutureResults(context.Background(), []uint32{7}, waiters, dispatchConfig{telemetry: handler})
	if err != nil {
		t.Fatalf("waitForFutureResults: %v", err)
	}
	if len(results) != 1 || results[7].kind != resultKindReturn {
		t.Fatalf("unexpected results: %#v", results)
	}

	if len(handler.started) != 1 {
		t.Fatalf("expected 1 StartWait call, got %d", len(handler.started))
	}
	if got := handler.started[0].PendingCallIDs; len(got) != 1 || got[0] != 7 {
		t.Fatalf("unexpected WaitInfo.PendingCallIDs: %#v", got)
	}
	if len(handler.ended) != 1 {
		t.Fatalf("expected 1 End call, got %d", len(handler.ended))
	}
	if handler.ended[0] != nil {
		t.Fatalf("expected End(nil), got End(%v)", handler.ended[0])
	}
}

func TestWaitForFutureResultsRecordsWaitSpanOnCancellation(t *testing.T) {
	handler := &recordingWaitHandler{}
	waiters := map[uint32]Waiter{
		// blockingWaiter never resolves on its own; only ctx cancellation
		// ends the wait.
		9: blockingWaiter{},
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := waitForFutureResults(ctx, []uint32{9}, waiters, dispatchConfig{telemetry: handler})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled, got %v", err)
	}

	if len(handler.ended) != 1 {
		t.Fatalf("expected 1 End call, got %d", len(handler.ended))
	}
	if !errors.Is(handler.ended[0], context.Canceled) {
		t.Fatalf("expected End(context.Canceled), got End(%v)", handler.ended[0])
	}
}

func TestSlogHandlerLogsWaitSpan(t *testing.T) {
	var buf bytes.Buffer
	handler := SlogHandler{Logger: slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))}
	waiters := map[uint32]Waiter{
		7: fakeWaiter{result: Return(Int(42))},
	}

	if _, err := waitForFutureResults(context.Background(), []uint32{7}, waiters, dispatchConfig{telemetry: handler}); err != nil {
		t.Fatalf("waitForFutureResults: %v", err)
	}

	out := buf.String()
	if !strings.Contains(out, "monty wait started") {
		t.Errorf("log missing wait-started message:\n%s", out)
	}
	if !strings.Contains(out, "monty wait finished") {
		t.Errorf("log missing wait-finished message:\n%s", out)
	}
	if !strings.Contains(out, "pending_call_ids=[7]") {
		t.Errorf("log missing pending_call_ids attribute:\n%s", out)
	}
	if !strings.Contains(out, "duration_ms=") {
		t.Errorf("log missing duration_ms attribute (DurationUnit's default):\n%s", out)
	}
}

func TestSlogHandlerLogsWaitSpanWithNanosecondDuration(t *testing.T) {
	var buf bytes.Buffer
	handler := SlogHandler{
		Logger:       slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})),
		DurationUnit: DurationNanoseconds,
	}
	waiters := map[uint32]Waiter{
		7: fakeWaiter{result: Return(Int(42))},
	}

	if _, err := waitForFutureResults(context.Background(), []uint32{7}, waiters, dispatchConfig{telemetry: handler}); err != nil {
		t.Fatalf("waitForFutureResults: %v", err)
	}

	out := buf.String()
	if !strings.Contains(out, "duration_ns=") {
		t.Errorf("log missing duration_ns attribute:\n%s", out)
	}
	if strings.Contains(out, "duration_ms=") {
		t.Errorf("log unexpectedly contains a duration_ms attribute:\n%s", out)
	}
}

func TestSlogHandlerLogsWaitSpanFailure(t *testing.T) {
	var buf bytes.Buffer
	handler := SlogHandler{Logger: slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))}
	waiters := map[uint32]Waiter{
		9: blockingWaiter{},
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if _, err := waitForFutureResults(ctx, []uint32{9}, waiters, dispatchConfig{telemetry: handler}); !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled, got %v", err)
	}

	out := buf.String()
	if !strings.Contains(out, "monty wait failed") {
		t.Errorf("log missing wait-failed message:\n%s", out)
	}
	if !strings.Contains(out, "level=ERROR") {
		t.Errorf("wait failure was not logged at Error level:\n%s", out)
	}
}

type blockingWaiter struct{}

func (blockingWaiter) Wait(ctx context.Context) Result {
	<-ctx.Done()
	// waitForFutureResults's outcomes channel is buffered to the waiter
	// count, so this send never blocks even though waitForFutureResults
	// itself already returned via the ctx.Done() case and won't read it.
	return Result{}
}

// panickingWaitHandler panics in exactly one of StartWait/WaitSpan.End,
// completing the panic-recovery coverage started in telemetry_test.go's
// TestTelemetryPanicNeverBreaksScriptExecution (which covers the other
// four TelemetryHandler methods via a live script).
type panickingWaitHandler struct {
	panicIn string // "StartWait" or "WaitEnd"
	onPanic func(recovered any)
}

func (h panickingWaitHandler) StartExecution(ctx context.Context, _ ExecutionInfo) (context.Context, ExecutionSpan) {
	return ctx, nil
}

func (h panickingWaitHandler) StartCallback(ctx context.Context, _ CallbackInfo) (context.Context, CallbackSpan) {
	return ctx, nil
}

func (h panickingWaitHandler) StartWait(ctx context.Context, _ WaitInfo) (context.Context, WaitSpan) {
	if h.panicIn == "StartWait" {
		panic("boom: StartWait")
	}
	return ctx, panickingWaitSpan(h)
}

func (h panickingWaitHandler) RecordPrint(context.Context, string) {}

type panickingWaitSpan panickingWaitHandler

func (s panickingWaitSpan) End(error) {
	if s.panicIn == "WaitEnd" {
		panic("boom: WaitSpan.End")
	}
}

func TestWaitForFutureResultsPanicNeverBreaksResolution(t *testing.T) {
	for _, panicIn := range []string{"StartWait", "WaitEnd"} {
		t.Run(panicIn, func(t *testing.T) {
			var mu sync.Mutex
			var recovered []any
			handler := panickingWaitHandler{
				panicIn: panicIn,
				onPanic: func(r any) {
					mu.Lock()
					recovered = append(recovered, r)
					mu.Unlock()
				},
			}
			waiters := map[uint32]Waiter{
				7: fakeWaiter{result: Return(Int(42))},
			}

			results, err := waitForFutureResults(context.Background(), []uint32{7}, waiters, dispatchConfig{
				telemetry:        handler,
				telemetryOptions: TelemetryOptions{PanicHandler: handler.onPanic},
			})
			if err != nil {
				t.Fatalf("waitForFutureResults: %v (panic in %s must not disrupt resolution)", err, panicIn)
			}
			if len(results) != 1 || results[7].kind != resultKindReturn {
				t.Fatalf("unexpected results: %#v", results)
			}

			mu.Lock()
			defer mu.Unlock()
			if len(recovered) != 1 {
				t.Fatalf("expected exactly 1 recovered panic routed to PanicHandler, got %d: %v", len(recovered), recovered)
			}
		})
	}
}
