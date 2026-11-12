package otelmonty_test

import (
	"context"
	"testing"

	monty "github.com/mdfranz/gomonty"
	"github.com/mdfranz/gomonty/otelmonty"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
)

func TestHandlerRecordsMetrics(t *testing.T) {
	reader := sdkmetric.NewManualReader()
	mp := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
	handler := otelmonty.Handler{Meter: mp.Meter("test")}

	runner, err := monty.New(`host_value()`, monty.CompileOptions{ScriptName: "m.py"})
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	_, err = runner.Run(context.Background(), monty.RunOptions{
		Telemetry: handler,
		Functions: map[string]monty.ExternalFunction{
			"host_value": func(context.Context, monty.Call) (monty.Result, error) {
				return monty.Return(monty.Int(1)), nil
			},
		},
	})
	if err != nil {
		t.Fatalf("run: %v", err)
	}

	var rm metricdata.ResourceMetrics
	if err := reader.Collect(context.Background(), &rm); err != nil {
		t.Fatalf("collect: %v", err)
	}
	counts := map[string]uint64{}
	for _, sm := range rm.ScopeMetrics {
		for _, m := range sm.Metrics {
			switch d := m.Data.(type) {
			case metricdata.Histogram[float64]:
				for _, dp := range d.DataPoints {
					counts[m.Name] += dp.Count
				}
			case metricdata.Sum[int64]:
				for _, dp := range d.DataPoints {
					counts[m.Name] += uint64(dp.Value)
				}
			}
		}
	}
	for _, name := range []string{
		"monty.execution.duration",
		"monty.execution.python_duration",
		"monty.execution.callback_duration",
		"monty.execution.wait_duration",
		"monty.callback.duration",
		"monty.executions",
	} {
		if counts[name] != 1 {
			t.Errorf("%s count = %d, want 1", name, counts[name])
		}
	}
}
