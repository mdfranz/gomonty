package monty

import (
	"context"
	"log/slog"
	"time"
)

// SlogHandler is a built-in [TelemetryHandler] backed by [log/slog], using
// only the standard library — see gomonty-olly.md §5. This is the local
// logging track; an OpenTelemetry/Logfire bridge is a separate, later
// piece of work, not part of this type.
//
// Execution start/finish log at Info/Error, callback invocation/return log
// at Debug/Info, wait spans (blocked resolving pending futures) log at
// Debug/Error, and captured print() output logs at Debug. Argument and
// output content is only included when the corresponding TelemetryOptions
// (RecordArguments/RecordOutputs) was enabled on the call — this handler
// logs exactly what it's handed (via ExecutionInfo.Inputs,
// CallbackInfo.Arguments, and each End's output TruncatedPayload) and never
// second-guesses that opt-in itself.
//
// The zero value is ready to use: Logger defaults to slog.Default() and
// DurationUnit defaults to DurationMilliseconds.
type SlogHandler struct {
	// Logger receives every log record. Defaults to slog.Default() if nil.
	Logger *slog.Logger
	// DurationUnit selects the unit every duration_* attribute is rendered
	// in. Defaults to DurationMilliseconds — DurationMicroseconds or
	// DurationNanoseconds preserve shorter local calls.
	DurationUnit DurationUnit
}

func (h SlogHandler) logger() *slog.Logger {
	if h.Logger != nil {
		return h.Logger
	}
	return slog.Default()
}

// DurationUnit selects the unit SlogHandler renders duration_* attributes
// in. The unit is baked into the attribute's key (duration_ms,
// duration_us, or duration_ns) so it's unambiguous from the log line alone.
type DurationUnit int

const (
	// DurationMilliseconds renders durations as whole milliseconds, e.g.
	// duration_ms=2. This is SlogHandler's default.
	DurationMilliseconds DurationUnit = iota
	// DurationNanoseconds renders durations as whole nanoseconds, e.g.
	// duration_ns=2041846.
	DurationNanoseconds
	// DurationMicroseconds renders durations as whole microseconds, e.g.
	// duration_us=2041.
	DurationMicroseconds
)

// appendDurationAttr appends one duration attribute — key suffixed with
// _ms, _us, or _ns per unit, and the value scaled to match — to attrs.
func appendDurationAttr(attrs []any, unit DurationUnit, key string, d time.Duration) []any {
	switch unit {
	case DurationNanoseconds:
		return append(attrs, key+"_ns", d.Nanoseconds())
	case DurationMicroseconds:
		return append(attrs, key+"_us", d.Microseconds())
	default:
		return append(attrs, key+"_ms", d.Milliseconds())
	}
}

// StartExecution implements TelemetryHandler.
func (h SlogHandler) StartExecution(ctx context.Context, info ExecutionInfo) (context.Context, ExecutionSpan) {
	attrs := []any{"script", info.ScriptName, "is_repl", info.IsRepl}
	attrs = appendPayloadAttrs(attrs, "inputs", info.Inputs)
	h.logger().InfoContext(ctx, "monty execution starting", attrs...)
	return ctx, slogExecutionSpan{logger: h.logger(), info: info, unit: h.DurationUnit}
}

// StartCallback implements TelemetryHandler.
func (h SlogHandler) StartCallback(ctx context.Context, info CallbackInfo) (context.Context, CallbackSpan) {
	attrs := []any{
		"function", info.FunctionName,
		"is_os_function", info.IsOSFunction,
		"is_method_call", info.IsMethodCall,
		"call_id", info.CallID,
	}
	attrs = appendPayloadAttrs(attrs, "arguments", info.Arguments)
	h.logger().DebugContext(ctx, "monty callback invoked", attrs...)
	return ctx, slogCallbackSpan{logger: h.logger(), info: info}
}

// StartWait implements TelemetryHandler.
func (h SlogHandler) StartWait(ctx context.Context, info WaitInfo) (context.Context, WaitSpan) {
	h.logger().DebugContext(ctx, "monty wait started", "pending_call_ids", info.PendingCallIDs)
	return ctx, slogWaitSpan{logger: h.logger(), info: info, start: time.Now(), unit: h.DurationUnit}
}

// RecordPrint implements TelemetryHandler.
func (h SlogHandler) RecordPrint(ctx context.Context, text string) {
	h.logger().DebugContext(ctx, "monty print", "text", text)
}

type slogWaitSpan struct {
	logger *slog.Logger
	info   WaitInfo
	start  time.Time
	unit   DurationUnit
}

// End implements WaitSpan.
func (s slogWaitSpan) End(err error) {
	attrs := []any{"pending_call_ids", s.info.PendingCallIDs}
	attrs = appendDurationAttr(attrs, s.unit, "duration", time.Since(s.start))
	if err != nil {
		s.logger.Error("monty wait failed", append(attrs, "error", err.Error())...)
		return
	}
	s.logger.Debug("monty wait finished", attrs...)
}

// appendPayloadAttrs appends key and key_truncated attrs for a
// TruncatedPayload, but only when it actually carries content — the zero
// TruncatedPayload{} (the "not recorded" case, the default) adds nothing,
// so a run with RecordArguments/RecordOutputs off logs no payload attrs at
// all rather than a pair of empty/false ones.
func appendPayloadAttrs(attrs []any, key string, payload TruncatedPayload) []any {
	if payload.Text == "" && !payload.Truncated {
		return attrs
	}
	return append(attrs, key, payload.Text, key+"_truncated", payload.Truncated)
}

type slogExecutionSpan struct {
	logger *slog.Logger
	info   ExecutionInfo
	unit   DurationUnit
}

// End implements ExecutionSpan.
func (s slogExecutionSpan) End(_ Value, err error, timing ExecutionTiming, output TruncatedPayload) {
	attrs := []any{
		"script", s.info.ScriptName,
		"is_repl", s.info.IsRepl,
	}
	attrs = appendDurationAttr(attrs, s.unit, "duration", timing.Total)
	attrs = appendDurationAttr(attrs, s.unit, "python_duration", timing.Python())
	attrs = appendDurationAttr(attrs, s.unit, "callback_duration", timing.Callback)
	attrs = appendDurationAttr(attrs, s.unit, "wait_duration", timing.Wait)
	attrs = appendPayloadAttrs(attrs, "output", output)
	if err != nil {
		s.logger.Error("monty execution failed", append(attrs, "error", err.Error())...)
		return
	}
	s.logger.Info("monty execution finished", attrs...)
}

type slogCallbackSpan struct {
	logger *slog.Logger
	info   CallbackInfo
}

// End implements CallbackSpan.
func (s slogCallbackSpan) End(result Result, err error, output TruncatedPayload) {
	attrs := []any{
		"function", s.info.FunctionName,
		"is_os_function", s.info.IsOSFunction,
		"call_id", s.info.CallID,
	}
	attrs = appendPayloadAttrs(attrs, "output", output)
	if err != nil {
		s.logger.Error("monty callback failed", append(attrs, "error", err.Error())...)
		return
	}
	if exc, ok := result.Raised(); ok && exc != nil {
		attrs = append(attrs, "exception_type", exc.Type)
	}
	s.logger.Info("monty callback returned", attrs...)
}
