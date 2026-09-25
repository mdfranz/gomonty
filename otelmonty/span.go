package otelmonty

import (
	monty "github.com/ewhauser/gomonty"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
)

// otelExecutionSpan implements monty.ExecutionSpan.
type otelExecutionSpan struct {
	span trace.Span
}

// End implements monty.ExecutionSpan. It leaves the span's status Unset on
// success (OTel convention: most backends treat Unset as success absent an
// explicit error) and sets it to codes.Error, with the error recorded, on
// failure.
func (s otelExecutionSpan) End(_ monty.Value, err error, timing monty.ExecutionTiming, output monty.TruncatedPayload) {
	defer s.span.End()
	s.span.SetAttributes(timingAttrs(timing)...)
	s.span.SetAttributes(appendPayloadAttrs(nil, attrOutput, attrOutputTruncated, output)...)
	if err != nil {
		s.span.RecordError(err)
		s.span.SetStatus(codes.Error, err.Error())
	}
}

// otelCallbackSpan implements monty.CallbackSpan.
type otelCallbackSpan struct {
	span trace.Span
}

// End implements monty.CallbackSpan. A Go-level err and a Monty exception
// (result.Raised()) are orthogonal, matching SlogHandler's handling in
// telemetry_slog.go: either one marks the span as failed, and a raised
// Python exception also gets its type recorded as an attribute.
func (s otelCallbackSpan) End(result monty.Result, err error, output monty.TruncatedPayload) {
	defer s.span.End()
	s.span.SetAttributes(appendPayloadAttrs(nil, attrOutput, attrOutputTruncated, output)...)
	if err != nil {
		s.span.RecordError(err)
		s.span.SetStatus(codes.Error, err.Error())
		return
	}
	if exc, ok := result.Raised(); ok && exc != nil {
		arg := ""
		if exc.Arg != nil {
			arg = *exc.Arg
		}
		s.span.SetAttributes(attribute.String(attrExceptionType, exc.Type))
		s.span.SetStatus(codes.Error, exc.Type+"("+arg+")")
	}
}

// otelWaitSpan implements monty.WaitSpan.
type otelWaitSpan struct {
	span trace.Span
}

// End implements monty.WaitSpan.
func (s otelWaitSpan) End(err error) {
	defer s.span.End()
	if err != nil {
		s.span.RecordError(err)
		s.span.SetStatus(codes.Error, err.Error())
	}
}
