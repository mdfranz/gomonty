package otelmonty

import (
	monty "github.com/mdfranz/gomonty"
	"go.opentelemetry.io/otel/attribute"
)

// Attribute keys, all under the monty.* namespace used by gomonty-olly.md
// itself (e.g. monty.python_duration_ms) rather than an OTel semantic
// convention, since none of the existing conventions cover an embedded
// Python interpreter's call/wait spans.
const (
	attrScriptName       = "monty.script_name"
	attrIsRepl           = "monty.is_repl"
	attrInputs           = "monty.inputs"
	attrInputsTruncated  = "monty.inputs_truncated"
	attrFunctionName     = "monty.function_name"
	attrIsOSFunction     = "monty.is_os_function"
	attrIsMethodCall     = "monty.is_method_call"
	attrCallID           = "monty.call_id"
	attrArguments        = "monty.arguments"
	attrArgumentsTrunc   = "monty.arguments_truncated"
	attrPendingCallIDs   = "monty.pending_call_ids"
	attrCallbackDuration = "monty.callback_duration_ms"
	attrWaitDuration     = "monty.wait_duration_ms"
	attrPythonDuration   = "monty.python_duration_ms"
	attrOutput           = "monty.output"
	attrOutputTruncated  = "monty.output_truncated"
	attrExceptionType    = "monty.exception_type"
	attrPrintText        = "monty.text"
)

// executionAttrs returns the ExecutionInfo attributes shared by
// StartExecution and ExecutionSpan.End.
func executionAttrs(info monty.ExecutionInfo) []attribute.KeyValue {
	attrs := []attribute.KeyValue{
		attribute.String(attrScriptName, info.ScriptName),
		attribute.Bool(attrIsRepl, info.IsRepl),
	}
	return appendPayloadAttrs(attrs, attrInputs, attrInputsTruncated, info.Inputs)
}

// callbackAttrs returns the CallbackInfo attributes shared by StartCallback
// and CallbackSpan.End.
func callbackAttrs(info monty.CallbackInfo) []attribute.KeyValue {
	attrs := []attribute.KeyValue{
		attribute.String(attrFunctionName, info.FunctionName),
		attribute.Bool(attrIsOSFunction, info.IsOSFunction),
		attribute.Bool(attrIsMethodCall, info.IsMethodCall),
		attribute.Int64(attrCallID, int64(info.CallID)),
	}
	return appendPayloadAttrs(attrs, attrArguments, attrArgumentsTrunc, info.Arguments)
}

// waitAttrs returns the WaitInfo attributes shared by StartWait and
// WaitSpan.End.
func waitAttrs(info monty.WaitInfo) []attribute.KeyValue {
	ids := make([]int64, len(info.PendingCallIDs))
	for i, id := range info.PendingCallIDs {
		ids[i] = int64(id)
	}
	return []attribute.KeyValue{attribute.Int64Slice(attrPendingCallIDs, ids)}
}

// timingAttrs renders an ExecutionTiming's breakdown as root-span-only
// attributes, in milliseconds to match SlogHandler's default unit and
// gomonty-olly.md's own monty.python_duration_ms naming. Total isn't
// included: the span's own start/end timestamps already carry it.
func timingAttrs(t monty.ExecutionTiming) []attribute.KeyValue {
	return []attribute.KeyValue{
		attribute.Int64(attrCallbackDuration, t.Callback.Milliseconds()),
		attribute.Int64(attrWaitDuration, t.Wait.Milliseconds()),
		attribute.Int64(attrPythonDuration, t.Python().Milliseconds()),
	}
}

// appendPayloadAttrs appends key/keyTruncated attrs for a TruncatedPayload,
// but only when it actually carries content: the zero TruncatedPayload{}
// (the default, "not recorded" case) adds nothing, mirroring
// SlogHandler.appendPayloadAttrs in telemetry_slog.go.
func appendPayloadAttrs(attrs []attribute.KeyValue, key, truncatedKey string, payload monty.TruncatedPayload) []attribute.KeyValue {
	if payload.Text == "" && !payload.Truncated {
		return attrs
	}
	return append(attrs, attribute.String(key, payload.Text), attribute.Bool(truncatedKey, payload.Truncated))
}
