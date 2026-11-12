package otelmonty

import (
	"context"

	monty "github.com/mdfranz/gomonty"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

// Metric names. Durations are recorded in seconds (the OTel convention) as
// float64, so sub-millisecond phases are not truncated.
const (
	metricExecutionDuration = "monty.execution.duration"
	metricPythonDuration    = "monty.execution.python_duration"
	metricCallbackTotal     = "monty.execution.callback_duration"
	metricWaitDuration      = "monty.execution.wait_duration"
	metricCallbackDuration  = "monty.callback.duration"
	metricExecutions        = "monty.executions"
)

const (
	attrOutcome = "monty.outcome"

	outcomeOK    = "ok"
	outcomeError = "error"
)

// durationBuckets spans 10µs to 60s, since interpreter-only phases are
// often well under the SDK's default 5ms first bucket.
var durationBuckets = []float64{
	0.00001, 0.00005, 0.0001, 0.0005, 0.001, 0.005, 0.01, 0.05,
	0.1, 0.5, 1, 5, 10, 60,
}

// instruments holds the metric instruments for one Handler call. Instrument
// creation is idempotent in the OTel API, so it is repeated per call to keep
// Handler's zero value usable; creation errors fall back to no-op behaviour
// (the API returns a usable instrument alongside any error).
type instruments struct {
	execution, python, callbackTotal, wait, callback metric.Float64Histogram
	executions                                       metric.Int64Counter
}

func newInstruments(m metric.Meter) instruments {
	hist := func(name, desc string) metric.Float64Histogram {
		h, _ := m.Float64Histogram(name,
			metric.WithUnit("s"),
			metric.WithDescription(desc),
			metric.WithExplicitBucketBoundaries(durationBuckets...),
		)
		return h
	}
	counter, _ := m.Int64Counter(metricExecutions,
		metric.WithUnit("{execution}"),
		metric.WithDescription("Completed Runner.Run / Repl.FeedRun calls."),
	)
	return instruments{
		execution:     hist(metricExecutionDuration, "Total wall time of one execution."),
		python:        hist(metricPythonDuration, "Execution time spent inside the interpreter."),
		callbackTotal: hist(metricCallbackTotal, "Execution time spent in host callbacks."),
		wait:          hist(metricWaitDuration, "Execution time spent waiting on pending futures."),
		callback:      hist(metricCallbackDuration, "Duration of one host callback."),
		executions:    counter,
	}
}

func outcomeOf(err error) string {
	if err != nil {
		return outcomeError
	}
	return outcomeOK
}

// recordExecution records the execution-level metrics. Attributes are
// limited to low-cardinality values: no script name, content or call ids.
func (i instruments) recordExecution(isRepl bool, err error, t monty.ExecutionTiming) {
	ctx := context.Background()
	opt := metric.WithAttributes(
		attribute.Bool(attrIsRepl, isRepl),
		attribute.String(attrOutcome, outcomeOf(err)),
	)
	i.execution.Record(ctx, t.Total.Seconds(), opt)
	i.python.Record(ctx, t.Python().Seconds(), opt)
	i.callbackTotal.Record(ctx, t.Callback.Seconds(), opt)
	i.wait.Record(ctx, t.Wait.Seconds(), opt)
	i.executions.Add(ctx, 1, opt)
}

// recordCallback records one host callback's duration. A raised Python
// exception counts as an error outcome and adds its type.
func (i instruments) recordCallback(info monty.CallbackInfo, result monty.Result, err error, seconds float64) {
	attrs := []attribute.KeyValue{
		attribute.String(attrFunctionName, info.FunctionName),
		attribute.Bool(attrIsOSFunction, info.IsOSFunction),
		attribute.String(attrOutcome, outcomeOf(err)),
	}
	if err == nil {
		if exc, ok := result.Raised(); ok && exc != nil {
			attrs[2] = attribute.String(attrOutcome, outcomeError)
			attrs = append(attrs, attribute.String(attrExceptionType, exc.Type))
		}
	}
	i.callback.Record(context.Background(), seconds, metric.WithAttributes(attrs...))
}
