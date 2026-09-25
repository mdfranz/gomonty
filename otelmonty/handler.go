package otelmonty

import (
	"context"

	monty "github.com/mdfranz/gomonty"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
)

// instrumentationName identifies this package's Tracer/spans to whatever
// TracerProvider they're registered with.
const instrumentationName = "github.com/mdfranz/gomonty/otelmonty"

// Handler is a monty.TelemetryHandler backed by OpenTelemetry tracing (see
// gomonty-olly.md §6). It produces a monty.run/monty.feed root span per
// Runner.Run/Repl.FeedRun call, with monty.call and monty.wait child spans
// for host callbacks and future-wait time — parent/child nesting comes for
// free from ordinary context.Context propagation through Tracer.Start, so a
// span created inside an ExternalFunction/OSHandler using the context
// StartCallback hands it attaches under monty.call automatically.
//
// The zero value is ready to use: Tracer defaults to
// otel.Tracer(instrumentationName), using whichever TracerProvider is
// registered (globally, via otel.SetTracerProvider, unless Tracer is set
// explicitly) at call time.
type Handler struct {
	// Tracer creates every span this handler starts. Defaults to
	// otel.Tracer(instrumentationName) when nil.
	Tracer trace.Tracer
}

func (h Handler) tracer() trace.Tracer {
	if h.Tracer != nil {
		return h.Tracer
	}
	return otel.Tracer(instrumentationName)
}

// StartExecution implements monty.TelemetryHandler.
func (h Handler) StartExecution(ctx context.Context, info monty.ExecutionInfo) (context.Context, monty.ExecutionSpan) {
	name := "monty.run"
	if info.IsRepl {
		name = "monty.feed"
	}
	ctx, span := h.tracer().Start(ctx, name,
		trace.WithSpanKind(trace.SpanKindInternal),
		trace.WithAttributes(executionAttrs(info)...),
	)
	return ctx, otelExecutionSpan{span: span}
}

// StartCallback implements monty.TelemetryHandler.
func (h Handler) StartCallback(ctx context.Context, info monty.CallbackInfo) (context.Context, monty.CallbackSpan) {
	ctx, span := h.tracer().Start(ctx, "monty.call",
		trace.WithSpanKind(trace.SpanKindInternal),
		trace.WithAttributes(callbackAttrs(info)...),
	)
	return ctx, otelCallbackSpan{span: span}
}

// StartWait implements monty.TelemetryHandler.
func (h Handler) StartWait(ctx context.Context, info monty.WaitInfo) (context.Context, monty.WaitSpan) {
	ctx, span := h.tracer().Start(ctx, "monty.wait",
		trace.WithSpanKind(trace.SpanKindInternal),
		trace.WithAttributes(waitAttrs(info)...),
	)
	return ctx, otelWaitSpan{span: span}
}

// RecordPrint implements monty.TelemetryHandler. OTel has no free-floating
// log-record primitive tied to a span the way log/slog does, so captured
// print() output becomes a span event on whatever span is active in ctx
// (per gomonty-olly.md §3, monty.print is a log event, not its own span).
// If ctx carries no recording span — not expected in normal use, since
// StartExecution always runs first — the event is silently skipped rather
// than fabricating a span for it.
func (h Handler) RecordPrint(ctx context.Context, text string) {
	span := trace.SpanFromContext(ctx)
	if !span.IsRecording() {
		return
	}
	span.AddEvent("monty.print", trace.WithAttributes(attribute.String(attrPrintText, text)))
}
