package monty

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"
	"unicode/utf8"
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
	// End closes the span with the call's final result, error, timing
	// breakdown, and — only when TelemetryOptions.RecordOutputs is true —
	// a size-limited rendering of the result. output is the zero
	// TruncatedPayload{} otherwise; result itself is still the real Value
	// regardless (needed for status/error classification), so a handler
	// that wants raw content unconditionally can still read result.Raw()
	// — RecordOutputs governs what gomonty hands you pre-rendered by
	// default, not a hard technical barrier.
	End(result Value, err error, timing ExecutionTiming, output TruncatedPayload)
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
	// End closes the span with the callback's result, error, and — only
	// when TelemetryOptions.RecordOutputs is true — a size-limited
	// rendering of the result. See ExecutionSpan.End's output doc for the
	// same caveat: result itself is still the real Result regardless.
	End(result Result, err error, output TruncatedPayload)
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
	// Inputs is a size-limited rendering of RunOptions.Inputs /
	// FeedOptions's fed inputs — the zero TruncatedPayload{} unless
	// TelemetryOptions.RecordArguments is true.
	Inputs TruncatedPayload
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
	// Arguments is a size-limited Python-call-syntax rendering of the
	// call's positional and keyword arguments — the zero
	// TruncatedPayload{} unless TelemetryOptions.RecordArguments is true.
	Arguments TruncatedPayload
}

// WaitInfo describes a pending-future resolution wait, passed to
// [TelemetryHandler.StartWait].
type WaitInfo struct {
	// PendingCallIDs are the call ids being waited on.
	PendingCallIDs []uint32
}

// TelemetryOptions configures a [TelemetryHandler]'s behavior.
type TelemetryOptions struct {
	// PanicHandler, if non-nil, is called with the recovered value whenever
	// a TelemetryHandler method panics. A panicking handler never disrupts
	// script execution or alters a callback's return value regardless of
	// whether this is set — it only controls whether that panic is
	// observable instead of being silently discarded.
	PanicHandler func(recovered any)

	// RecordArguments enables ExecutionInfo.Inputs / CallbackInfo.Arguments.
	// False by default: Python inputs and call arguments can carry
	// credentials, so no payload content is rendered unless explicitly
	// opted into.
	RecordArguments bool
	// RecordOutputs enables the output TruncatedPayload passed to
	// ExecutionSpan.End / CallbackSpan.End. False by default, same
	// rationale as RecordArguments.
	RecordOutputs bool
	// MaxAttributeBytes caps every rendered payload at this many bytes,
	// truncating (at a valid UTF-8 boundary) and setting
	// TruncatedPayload.Truncated when exceeded. Zero/negative means
	// DefaultMaxAttributeBytes.
	MaxAttributeBytes int
}

// DefaultMaxAttributeBytes is used when TelemetryOptions.MaxAttributeBytes
// is zero/negative. gomonty-olly.md originally suggested 1024, matching
// upstream's own cap, but that's arguably aggressive for legitimate
// structured results (a returned list or dict routinely exceeds 1KB) — this
// starts more generous and is a reasonable thing to revisit against real
// gomonty/sparktea usage.
const DefaultMaxAttributeBytes = 4096

// TruncatedPayload is a size-limited textual rendering of arguments or a
// result, produced only when TelemetryOptions.RecordArguments/
// RecordOutputs is true. The zero value (Text == "", Truncated == false)
// is what every payload-bearing field gets by default — "not recorded",
// not "recorded as empty".
type TruncatedPayload struct {
	Text      string
	Truncated bool
}

// truncatedPayload returns the zero TruncatedPayload{} without calling
// render at all when !enabled — render is a closure, not a precomputed
// string, specifically so the (potentially non-trivial) rendering work
// never happens when RecordArguments/RecordOutputs is off, which is the
// default.
func truncatedPayload(enabled bool, render func() string, maxBytes int) TruncatedPayload {
	if !enabled {
		return TruncatedPayload{}
	}
	text := render()
	if maxBytes <= 0 {
		maxBytes = DefaultMaxAttributeBytes
	}
	if len(text) <= maxBytes {
		return TruncatedPayload{Text: text}
	}
	return TruncatedPayload{Text: truncateUTF8(text, maxBytes), Truncated: true}
}

// truncateUTF8 cuts s to at most maxBytes bytes, trimming back further if
// necessary so it never splits a multi-byte UTF-8 sequence. At most 3 extra
// bytes are trimmed (the longest UTF-8 sequence is 4 bytes).
func truncateUTF8(s string, maxBytes int) string {
	s = s[:maxBytes]
	for len(s) > 0 && !utf8.RuneStart(s[len(s)-1]) {
		s = s[:len(s)-1]
	}
	return s
}

// renderInputs renders a name->Value map as "name=value, ..." in
// deterministic (sorted-by-name) order, for ExecutionInfo.Inputs.
func renderInputs(inputs map[string]Value) string {
	if len(inputs) == 0 {
		return ""
	}
	names := make([]string, 0, len(inputs))
	for name := range inputs {
		names = append(names, name)
	}
	sort.Strings(names)
	var b strings.Builder
	for i, name := range names {
		if i > 0 {
			b.WriteString(", ")
		}
		b.WriteString(name)
		b.WriteByte('=')
		b.WriteString(inputs[name].String())
	}
	return b.String()
}

// renderArguments renders positional and keyword arguments in Python
// call syntax ("1, 2, key=3"), for CallbackInfo.Arguments.
func renderArguments(args []Value, kwargs Dict) string {
	parts := make([]string, 0, len(args)+len(kwargs))
	for _, a := range args {
		parts = append(parts, a.String())
	}
	for _, kv := range kwargs {
		parts = append(parts, fmt.Sprintf("%s=%s", kv.Key.String(), kv.Value.String()))
	}
	return strings.Join(parts, ", ")
}

// renderResult renders a callback Result for CallbackSpan.End's output
// payload: an exception result renders as "Type(arg)", otherwise the
// returned Value's own rendering.
func renderResult(r Result) string {
	if exc, ok := r.Raised(); ok && exc != nil {
		arg := ""
		if exc.Arg != nil {
			arg = *exc.Arg
		}
		return fmt.Sprintf("%s(%s)", exc.Type, arg)
	}
	return r.Value().String()
}

// printTelemetry bundles what consumeOpResult needs to call RecordPrint
// alongside the ordinary PrintCallback, without growing every intervening
// function's parameter list by two.
type printTelemetry struct {
	handler TelemetryHandler
	opts    TelemetryOptions
}

func recordPrint(ctx context.Context, pt printTelemetry, text string) {
	if pt.handler == nil {
		return
	}
	safeTelemetryCall(pt.opts.PanicHandler, func() {
		pt.handler.RecordPrint(ctx, text)
	})
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
	output := truncatedPayload(opts.RecordOutputs, result.String, opts.MaxAttributeBytes)
	safeTelemetryCall(opts.PanicHandler, func() {
		span.End(result, err, timing, output)
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
	output := truncatedPayload(opts.RecordOutputs, func() string { return renderResult(result) }, opts.MaxAttributeBytes)
	safeTelemetryCall(opts.PanicHandler, func() {
		span.End(result, err, output)
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
