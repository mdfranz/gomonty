package monty

import "context"

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
	// End closes the span with the call's final result and error.
	End(result Value, err error)
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
// deliberately minimal for now: payload recording and truncation options
// (RecordArguments, RecordOutputs, MaxAttributeBytes) land with a later
// milestone — see gomonty-olly.md and the gomonty issue tracker.
type TelemetryOptions struct{}
