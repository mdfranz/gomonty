package monty

import (
	"context"
	"log/slog"
)

// SlogHandler is a built-in [TelemetryHandler] backed by [log/slog], using
// only the standard library — see gomonty-olly.md §5. This is the local
// logging track; an OpenTelemetry/Logfire bridge is a separate, later
// piece of work, not part of this type.
//
// Execution start/finish log at Info/Error, callback invocation/return log
// at Debug/Info, and captured print() output logs at Debug. Argument and
// output content is only included when the corresponding TelemetryOptions
// (RecordArguments/RecordOutputs) was enabled on the call — this handler
// logs exactly what it's handed (via ExecutionInfo.Inputs,
// CallbackInfo.Arguments, and each End's output TruncatedPayload) and never
// second-guesses that opt-in itself.
//
// The zero value is ready to use: Logger defaults to slog.Default().
type SlogHandler struct {
	// Logger receives every log record. Defaults to slog.Default() if nil.
	Logger *slog.Logger
}

func (h SlogHandler) logger() *slog.Logger {
	if h.Logger != nil {
		return h.Logger
	}
	return slog.Default()
}

// StartExecution implements TelemetryHandler.
func (h SlogHandler) StartExecution(ctx context.Context, info ExecutionInfo) (context.Context, ExecutionSpan) {
	attrs := []any{"script", info.ScriptName, "is_repl", info.IsRepl}
	attrs = appendPayloadAttrs(attrs, "inputs", info.Inputs)
	h.logger().InfoContext(ctx, "monty execution starting", attrs...)
	return ctx, slogExecutionSpan{logger: h.logger(), info: info}
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

// StartWait implements TelemetryHandler. Not in this adapter's scope (see
// gomonty-olly.md §5's Scope list — wait spans aren't mentioned): returns a
// nil span, so End is never called and nothing is logged.
func (h SlogHandler) StartWait(ctx context.Context, _ WaitInfo) (context.Context, WaitSpan) {
	return ctx, nil
}

// RecordPrint implements TelemetryHandler.
func (h SlogHandler) RecordPrint(ctx context.Context, text string) {
	h.logger().DebugContext(ctx, "monty print", "text", text)
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
}

// End implements ExecutionSpan.
func (s slogExecutionSpan) End(_ Value, err error, timing ExecutionTiming, output TruncatedPayload) {
	attrs := []any{
		"script", s.info.ScriptName,
		"is_repl", s.info.IsRepl,
		"duration_ms", timing.Total.Milliseconds(),
		"python_duration_ms", timing.Python().Milliseconds(),
		"callback_duration_ms", timing.Callback.Milliseconds(),
		"wait_duration_ms", timing.Wait.Milliseconds(),
	}
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
