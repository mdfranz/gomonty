// Package otelmonty bridges gomonty's execution telemetry to standard
// OpenTelemetry tracing.
//
// It implements monty.TelemetryHandler using go.opentelemetry.io/otel,
// producing a monty.run/monty.feed root span with monty.call/monty.wait
// children per gomonty-olly.md §3. Callers pass a trace-bearing
// context.Context into Runner.Run/Repl.FeedRun so gomonty's spans join
// their existing trace. Exporting to any OTLP-compatible backend,
// including Logfire, needs only a standard OpenTelemetry exporter — this
// package has no backend-specific code.
//
// This is an isolated submodule (its own go.mod) so the OpenTelemetry SDK
// never becomes a dependency of gomonty's own root module.
package otelmonty
