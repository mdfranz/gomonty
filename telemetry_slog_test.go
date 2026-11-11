package monty_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"strings"
	"testing"

	monty "github.com/mdfranz/gomonty"
)

func newTestSlogHandler(t *testing.T) (monty.SlogHandler, *bytes.Buffer) {
	t.Helper()
	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
	return monty.SlogHandler{Logger: logger}, &buf
}

func TestSlogHandlerSuccessfulRun(t *testing.T) {
	handler, buf := newTestSlogHandler(t)

	runner, err := monty.New(`40 + 2`, monty.CompileOptions{ScriptName: "slog-success.py"})
	if err != nil {
		t.Fatalf("compile: %v", err)
	}

	value, runErr := runner.Run(context.Background(), monty.RunOptions{Telemetry: handler})
	if runErr != nil {
		t.Fatalf("run: %v", runErr)
	}
	if got, ok := value.Raw().(int64); !ok || got != 42 {
		t.Fatalf("unexpected result: %#v", value.Raw())
	}

	out := buf.String()
	if !strings.Contains(out, "monty execution starting") {
		t.Errorf("log missing start message:\n%s", out)
	}
	if !strings.Contains(out, "monty execution finished") {
		t.Errorf("log missing finish message:\n%s", out)
	}
	if !strings.Contains(out, "script=slog-success.py") {
		t.Errorf("log missing script attribute:\n%s", out)
	}
	if strings.Contains(out, "monty execution failed") {
		t.Errorf("successful run logged a failure:\n%s", out)
	}
}

func TestSlogHandlerExternalCall(t *testing.T) {
	handler, buf := newTestSlogHandler(t)

	runner, err := monty.New(`host_value()`, monty.CompileOptions{ScriptName: "slog-callback.py"})
	if err != nil {
		t.Fatalf("compile: %v", err)
	}

	_, runErr := runner.Run(context.Background(), monty.RunOptions{
		Telemetry: handler,
		Functions: map[string]monty.ExternalFunction{
			"host_value": func(context.Context, monty.Call) (monty.Result, error) {
				return monty.Return(monty.Int(1)), nil
			},
		},
	})
	if runErr != nil {
		t.Fatalf("run: %v", runErr)
	}

	out := buf.String()
	if !strings.Contains(out, "monty callback invoked") {
		t.Errorf("log missing callback-invoked message:\n%s", out)
	}
	if !strings.Contains(out, "monty callback returned") {
		t.Errorf("log missing callback-returned message:\n%s", out)
	}
	if !strings.Contains(out, "function=host_value") {
		t.Errorf("log missing function attribute:\n%s", out)
	}
}

func TestSlogHandlerPythonError(t *testing.T) {
	handler, buf := newTestSlogHandler(t)

	runner, err := monty.New(`raise ValueError("boom")`, monty.CompileOptions{ScriptName: "slog-error.py"})
	if err != nil {
		t.Fatalf("compile: %v", err)
	}

	_, runErr := runner.Run(context.Background(), monty.RunOptions{Telemetry: handler})
	if runErr == nil {
		t.Fatal("expected a runtime error")
	}

	out := buf.String()
	if !strings.Contains(out, "monty execution failed") {
		t.Errorf("log missing failure message:\n%s", out)
	}
	if !strings.Contains(out, "level=ERROR") {
		t.Errorf("failure was not logged at Error level:\n%s", out)
	}
	if !strings.Contains(out, "error=") {
		t.Errorf("log missing error attribute:\n%s", out)
	}
}

func TestSlogHandlerDurationUnitDefaultsToMilliseconds(t *testing.T) {
	handler, buf := newTestSlogHandler(t)

	runner, err := monty.New(`40 + 2`, monty.CompileOptions{ScriptName: "slog-duration-ms.py"})
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	if _, runErr := runner.Run(context.Background(), monty.RunOptions{Telemetry: handler}); runErr != nil {
		t.Fatalf("run: %v", runErr)
	}

	out := buf.String()
	if !strings.Contains(out, "duration_ms=") {
		t.Errorf("log missing duration_ms attribute:\n%s", out)
	}
	if strings.Contains(out, "duration_ns=") {
		t.Errorf("log unexpectedly contains a duration_ns attribute:\n%s", out)
	}
}

func TestSlogHandlerDurationUnitNanoseconds(t *testing.T) {
	handler, buf := newTestSlogHandler(t)
	handler.DurationUnit = monty.DurationNanoseconds

	runner, err := monty.New(`40 + 2`, monty.CompileOptions{ScriptName: "slog-duration-ns.py"})
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	if _, runErr := runner.Run(context.Background(), monty.RunOptions{Telemetry: handler}); runErr != nil {
		t.Fatalf("run: %v", runErr)
	}

	out := buf.String()
	if !strings.Contains(out, "duration_ns=") {
		t.Errorf("log missing duration_ns attribute:\n%s", out)
	}
	if strings.Contains(out, "duration_ms=") {
		t.Errorf("log unexpectedly contains a duration_ms attribute:\n%s", out)
	}
}

func TestSlogHandlerDurationUnitMicroseconds(t *testing.T) {
	handler, buf := newTestSlogHandler(t)
	handler.DurationUnit = monty.DurationMicroseconds

	runner, err := monty.New(`40 + 2`, monty.CompileOptions{ScriptName: "slog-duration-us.py"})
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	if _, runErr := runner.Run(context.Background(), monty.RunOptions{Telemetry: handler}); runErr != nil {
		t.Fatalf("run: %v", runErr)
	}

	out := buf.String()
	for _, key := range []string{"duration_us=", "python_duration_us=", "callback_duration_us=", "wait_duration_us="} {
		if !strings.Contains(out, key) {
			t.Errorf("log missing %s attribute:\n%s", key, out)
		}
	}
	if strings.Contains(out, "duration_ns=") || strings.Contains(out, "duration_ms=") {
		t.Errorf("log contains an unexpected duration unit:\n%s", out)
	}
}

func TestSlogHandlerRespectsRecordArgumentsOptIn(t *testing.T) {
	handler, buf := newTestSlogHandler(t)

	runner, err := monty.New(`host_echo("do-not-log-me")`, monty.CompileOptions{ScriptName: "slog-no-payload.py"})
	if err != nil {
		t.Fatalf("compile: %v", err)
	}

	_, runErr := runner.Run(context.Background(), monty.RunOptions{
		Telemetry: handler,
		// TelemetryOptions left at its zero value: RecordArguments off.
		Functions: map[string]monty.ExternalFunction{
			"host_echo": func(_ context.Context, call monty.Call) (monty.Result, error) {
				return monty.Return(call.Args[0]), nil
			},
		},
	})
	if runErr != nil {
		t.Fatalf("run: %v", runErr)
	}

	if strings.Contains(buf.String(), "do-not-log-me") {
		t.Errorf("argument content was logged despite RecordArguments being off:\n%s", buf.String())
	}
}

func TestSlogHandlerPrintCaptureOptions(t *testing.T) {
	for _, tc := range []struct {
		name string
		opts monty.TelemetryOptions
		want string
	}{
		{name: "default off"},
		{name: "enabled", opts: monty.TelemetryOptions{RecordPrint: true}, want: "secret\n"},
		{name: "truncated", opts: monty.TelemetryOptions{RecordPrint: true, MaxAttributeBytes: 3}, want: "sec"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var buf bytes.Buffer
			handler := monty.SlogHandler{Logger: slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))}
			runner, err := monty.New(`print("secret")`, monty.CompileOptions{})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := runner.Run(context.Background(), monty.RunOptions{Telemetry: handler, TelemetryOptions: tc.opts}); err != nil {
				t.Fatal(err)
			}
			var prints []string
			decoder := json.NewDecoder(&buf)
			for {
				var record map[string]any
				if err := decoder.Decode(&record); err == io.EOF {
					break
				} else if err != nil {
					t.Fatal(err)
				}
				if record["msg"] == "monty print" {
					text, ok := record["text"].(string)
					if !ok {
						t.Fatalf("print record missing text: %+v", record)
					}
					prints = append(prints, text)
				}
			}
			if !tc.opts.RecordPrint {
				if len(prints) != 0 {
					t.Errorf("printed content leaked to logs: %q", prints)
				}
			} else if len(prints) != 1 || prints[0] != tc.want {
				t.Errorf("print records = %q, want [%q]", prints, tc.want)
			}
		})
	}
}
