package monty

import (
	"context"
	"errors"
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

type blockingWaiter struct{}

func (blockingWaiter) Wait(ctx context.Context) Result {
	<-ctx.Done()
	// waitForFutureResults's outcomes channel is buffered to the waiter
	// count, so this send never blocks even though waitForFutureResults
	// itself already returned via the ctx.Done() case and won't read it.
	return Result{}
}
