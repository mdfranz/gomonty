// Command example demonstrates wiring otelmonty.Handler into a Runner.Run
// call, exporting via a standard OTLP/HTTP exporter. It has no
// backend-specific code: pointing this at Logfire (or any other
// OTLP-compatible backend) is purely a matter of setting
// OTEL_EXPORTER_OTLP_ENDPOINT and any required auth headers via
// OTEL_EXPORTER_OTLP_HEADERS, no code changes needed. With no collector
// configured, the OTLP exporter simply fails silently on send in the
// background — this example still runs and prints the trace id either way,
// so it also works as a smoke test with nothing listening.
package main

import (
	"context"
	"fmt"
	"log"

	monty "github.com/mdfranz/gomonty"
	"github.com/mdfranz/gomonty/otelmonty"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/trace"
)

func main() {
	ctx := context.Background()

	exporter, err := otlptracehttp.New(ctx)
	if err != nil {
		log.Fatalf("new OTLP exporter: %v", err)
	}
	tp := sdktrace.NewTracerProvider(sdktrace.WithBatcher(exporter))
	defer func() {
		if err := tp.Shutdown(ctx); err != nil {
			log.Printf("shutdown tracer provider: %v", err)
		}
	}()
	otel.SetTracerProvider(tp)

	runner, err := monty.New(`
print("starting work")
result = host_double(21)
print("done")
result
`, monty.CompileOptions{ScriptName: "otel-example.py"})
	if err != nil {
		log.Fatal(err)
	}

	handler := otelmonty.Handler{Tracer: tp.Tracer("otelmonty-example")}

	rootCtx, rootSpan := handler.Tracer.Start(ctx, "example.main")
	defer rootSpan.End()

	value, err := runner.Run(rootCtx, monty.RunOptions{
		Telemetry: handler,
		TelemetryOptions: monty.TelemetryOptions{
			// Off by default in real usage; on here only to show what a
			// recorded span attribute looks like. Argument/result content
			// can carry secrets — opt in deliberately.
			RecordArguments: true,
			RecordOutputs:   true,
		},
		Functions: map[string]monty.ExternalFunction{
			"host_double": func(ctx context.Context, call monty.Call) (monty.Result, error) {
				// A downstream span started from the context StartCallback
				// handed the callback attaches under monty.call, and would
				// join the trace of any real HTTP/DB client used here.
				_, span := trace.SpanFromContext(ctx).TracerProvider().Tracer("otelmonty-example").Start(ctx, "host.double")
				defer span.End()
				n, _ := call.Args[0].Raw().(int64)
				return monty.Return(monty.Int(n * 2)), nil
			},
		},
	})
	if err != nil {
		log.Fatal(err)
	}

	fmt.Println("result:", value.Raw())
	fmt.Println("trace id:", rootSpan.SpanContext().TraceID())
}
