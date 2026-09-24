package monty

import (
	"context"
	"time"
)

// TelemetryHandler receives execution telemetry from [Runner.Run] and
// [Repl.FeedRun]. A nil handler — the default via [RunOptions.Telemetry] /
// [FeedOptions.Telemetry] — means telemetry is fully disabled: no spans are
// created and no telemetry-related code runs on the execution path.
//
// Implementations must be safe for concurrent use if the same handler is
// shared across multiple Runner/Repl instances.
type TelemetryHandler interface {
	// StartExecution begins the root span for one Runner.Run or
	// Repl.FeedRun call. The returned context carries that span and should
	// be used for the rest of the call's dispatch loop; the returned
	// ExecutionSpan's End must be called exactly once when the call
	// returns.
	StartExecution(ctx context.Context, info ExecutionInfo) (context.Context, ExecutionSpan)

	// StartCallback begins a child span for one external-function or
	// OS-handler invocation. The returned context is passed into the
	// callback itself, so spans it creates (e.g. for an HTTP or database
	// call) attach as its children.
	StartCallback(ctx context.Context, info CallbackInfo) (context.Context, CallbackSpan)

	// StartWait begins a child span covering time blocked waiting for
	// pending futures to resolve.
	StartWait(ctx context.Context, info WaitInfo) (context.Context, WaitSpan)

	// RecordPrint reports captured Python print() output. It fires once
	// per dispatch/resume step with whatever text was captured during that
	// step, not once per print() call.
	RecordPrint(ctx context.Context, text string)
}

// ExecutionSpan represents the root span started by
// [TelemetryHandler.StartExecution].
type ExecutionSpan interface {
	// End closes the span with the call's final result, error, and timing
	// breakdown.
	End(result Value, err error, timing ExecutionTiming)
}

// ExecutionTiming breaks one execution's wall time down into the phases
// described in gomonty-olly.md §3: time spent inside the Rust/FFI
// interpreter, time spent in host callbacks, and time spent blocked
// waiting for pending futures.
type ExecutionTiming struct {
	// Total is the full StartExecution-to-End wall time.
	Total time.Duration
	// Callback is cumulative time spent inside external-function/OS-handler
	// invocations (the sum of every StartCallback...End interval).
	Callback time.Duration
	// Wait is cumulative time spent blocked resolving pending futures (the
	// sum of every StartWait...End interval).
	Wait time.Duration
}

// Python returns Total - Callback - Wait: time spent inside the Rust/FFI
// interpreter itself, excluding host callback and future-wait time. This is
// gomonty-olly.md's monty.python_duration_ms, computed on demand rather
// than precomputed.
func (t ExecutionTiming) Python() time.Duration {
	return t.Total - t.Callback - t.Wait
}

// CallbackSpan represents a child span started by
// [TelemetryHandler.StartCallback].
type CallbackSpan interface {
	// End closes the span with the callback's result and error.
	End(result Result, err error)
}

// WaitSpan represents a child span started by
// [TelemetryHandler.StartWait].
type WaitSpan interface {
	// End closes the span, non-nil err if waiting failed (e.g. context
	// cancellation) rather than resolving normally.
	End(err error)
}

// ExecutionInfo describes one Runner.Run or Repl.FeedRun invocation, passed
// to [TelemetryHandler.StartExecution].
type ExecutionInfo struct {
	// ScriptName is the runner's or REPL's configured script name.
	ScriptName string
	// IsRepl is true for Repl.FeedRun, false for Runner.Run.
	IsRepl bool
}

// CallbackInfo describes one external-function or OS-handler invocation,
// passed to [TelemetryHandler.StartCallback].
type CallbackInfo struct {
	// FunctionName is the external function name, or the OS function name
	// (see OSFunction) for an OS call.
	FunctionName string
	// IsOSFunction is true for an OS handler call, false for a plain
	// external function.
	IsOSFunction bool
	// IsMethodCall is true if Monty routed this as a method call rather
	// than a plain function call. gomonty currently dispatches both the
	// same way (by function name, not host-object identity) — see
	// dispatch.go — so this reflects Monty's own classification, not a
	// gomonty routing distinction.
	IsMethodCall bool
	// CallID is the call's correlation id, matching Call.CallID /
	// OSCall.CallID.
	CallID uint32
}

// WaitInfo describes a pending-future resolution wait, passed to
// [TelemetryHandler.StartWait].
type WaitInfo struct {
	// PendingCallIDs are the call ids being waited on.
	PendingCallIDs []uint32
}

// TelemetryOptions configures a [TelemetryHandler]'s behavior. It is
// otherwise deliberately minimal for now: payload recording and truncation
// options (RecordArguments, RecordOutputs, MaxAttributeBytes) land with a
// later milestone — see gomonty-olly.md and the gomonty issue tracker.
type TelemetryOptions struct {
	// PanicHandler, if non-nil, is called with the recovered value whenever
	// a TelemetryHandler method panics. A panicking handler never disrupts
	// script execution or alters a callback's return value regardless of
	// whether this is set — it only controls whether that panic is
	// observable instead of being silently discarded.
	PanicHandler func(recovered any)
}

// The start*Span/end*Span helpers below are nil-safe: with telemetry ==
// nil (the default), they're no-ops that return ctx unchanged and a nil
// span, so call sites never need their own nil check. They also funnel
// every TelemetryHandler invocation through safeTelemetryCall, so a
// panicking handler can never disrupt script execution — see
// gomonty-olly.md §7.

// safeTelemetryCall invokes f, recovering any panic so a broken
// TelemetryHandler can never disrupt script execution. If f panics before
// assigning its captured return values, those values simply keep their
// zero value — callers pre-seed them with the "telemetry did nothing"
// fallback (e.g. the original ctx, a nil span) before calling this.
func safeTelemetryCall(panicHandler func(recovered any), f func()) {
	defer func() {
		if r := recover(); r != nil && panicHandler != nil {
			panicHandler(r)
		}
	}()
	f()
}

func startExecutionSpan(ctx context.Context, telemetry TelemetryHandler, info ExecutionInfo, opts TelemetryOptions) (context.Context, ExecutionSpan) {
	if telemetry == nil {
		return ctx, nil
	}
	resultCtx, span := ctx, ExecutionSpan(nil)
	safeTelemetryCall(opts.PanicHandler, func() {
		resultCtx, span = telemetry.StartExecution(ctx, info)
	})
	return resultCtx, span
}

func endExecutionSpan(span ExecutionSpan, result Value, err error, timing ExecutionTiming, opts TelemetryOptions) {
	if span == nil {
		return
	}
	safeTelemetryCall(opts.PanicHandler, func() {
		span.End(result, err, timing)
	})
}

func startCallbackSpan(ctx context.Context, telemetry TelemetryHandler, info CallbackInfo, opts TelemetryOptions) (context.Context, CallbackSpan) {
	if telemetry == nil {
		return ctx, nil
	}
	resultCtx, span := ctx, CallbackSpan(nil)
	safeTelemetryCall(opts.PanicHandler, func() {
		resultCtx, span = telemetry.StartCallback(ctx, info)
	})
	return resultCtx, span
}

func endCallbackSpan(span CallbackSpan, result Result, err error, opts TelemetryOptions) {
	if span == nil {
		return
	}
	safeTelemetryCall(opts.PanicHandler, func() {
		span.End(result, err)
	})
}

func startWaitSpan(ctx context.Context, telemetry TelemetryHandler, info WaitInfo, opts TelemetryOptions) (context.Context, WaitSpan) {
	if telemetry == nil {
		return ctx, nil
	}
	resultCtx, span := ctx, WaitSpan(nil)
	safeTelemetryCall(opts.PanicHandler, func() {
		resultCtx, span = telemetry.StartWait(ctx, info)
	})
	return resultCtx, span
}

func endWaitSpan(span WaitSpan, err error, opts TelemetryOptions) {
	if span == nil {
		return
	}
	safeTelemetryCall(opts.PanicHandler, func() {
		span.End(err)
	})
}
