package main

import (
	"context"
	"fmt"
	"os"

	monty "github.com/mdfranz/gomonty"
	"github.com/mdfranz/gomonty/otelmonty"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
)

// initOTelTelemetry builds an otelmonty.Handler exporting via standard
// OTLP/HTTP when OTEL_EXPORTER_OTLP_ENDPOINT is set — the same env var the
// exporter itself reads, per the OTel spec — so pointing shmonty at Logfire
// or any other OTLP backend needs no shmonty-specific config, matching
// otelmonty's own zero-backend-code design (gomonty-olly.md §6). When
// unset, or when the exporter fails to initialize, it returns a nil
// handler and a no-op shutdown, so callers can defer the shutdown func
// unconditionally and fall back to local /debug telemetry either way.
func initOTelTelemetry(ctx context.Context) (monty.TelemetryHandler, func(context.Context) error) {
	noop := func(context.Context) error { return nil }

	if os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT") == "" {
		return nil, noop
	}

	exporter, err := otlptracehttp.New(ctx)
	if err != nil {
		fmt.Fprintln(os.Stderr, "otel: failed to create exporter, falling back to local telemetry:", err)
		return nil, noop
	}

	// Resource attributes mark every span from this process with where the
	// interpreter ran, so remote and local sessions can be told apart.
	attrs := []attribute.KeyValue{attribute.String("monty.worker", workerKind())}
	if remoteEndpoint != "" {
		attrs = append(attrs, attribute.String("monty.worker.endpoint", remoteEndpoint))
	}
	res, err := resource.Merge(resource.Default(), resource.NewSchemaless(attrs...))
	if err != nil {
		res = resource.Default()
	}
	tp := sdktrace.NewTracerProvider(sdktrace.WithBatcher(exporter), sdktrace.WithResource(res))
	otel.SetTracerProvider(tp)
	return otelmonty.Handler{Tracer: tp.Tracer("shmonty")}, tp.Shutdown
}
