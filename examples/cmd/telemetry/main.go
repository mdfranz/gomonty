// Command telemetry demonstrates wiring monty.SlogHandler into a
// Runner.Run call for local structured logging — the pattern sparktea's
// codemode package (or any other gomonty consumer) can adapt directly: swap
// os.Stdout for whatever *slog.Logger the host application already uses,
// and pass RunOptions.Telemetry/TelemetryOptions through from wherever it
// constructs RunOptions today.
package main

import (
	"context"
	"fmt"
	"log"
	"log/slog"
	"os"

	monty "github.com/ewhauser/gomonty"
)

func main() {
	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{
		Level: slog.LevelDebug, // callback invocation and print() lines log at Debug
	}))

	runner, err := monty.New(`
print("starting work")
result = host_double(21)
print("done")
result
`, monty.CompileOptions{ScriptName: "telemetry-example.py"})
	if err != nil {
		log.Fatal(err)
	}

	value, err := runner.Run(context.Background(), monty.RunOptions{
		Telemetry: monty.SlogHandler{Logger: logger},
		TelemetryOptions: monty.TelemetryOptions{
			// Off by default in real usage — turned on here only to show
			// what recorded payloads look like in the log output. Argument/
			// result content can carry secrets; opt in deliberately.
			RecordArguments: true,
			RecordOutputs:   true,
		},
		Functions: map[string]monty.ExternalFunction{
			"host_double": func(_ context.Context, call monty.Call) (monty.Result, error) {
				n, _ := call.Args[0].Raw().(int64)
				return monty.Return(monty.Int(n * 2)), nil
			},
		},
	})
	if err != nil {
		log.Fatal(err)
	}

	fmt.Println("result:", value.Raw())
}
