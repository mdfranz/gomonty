package main

import (
	"context"
	"fmt"
	"time"

	monty "github.com/mdfranz/gomonty"
	"github.com/mdfranz/gomonty/vfs"
)

// A sandbox belongs to one REPL session or one script invocation.
type sandbox struct {
	os      monty.OSHandler
	modules map[string]monty.Module
}

func newSandbox() *sandbox {
	fs := vfs.NewMemoryFS()
	fs.AddText("/sandbox/readme.txt", "Welcome to shmonty's in-memory filesystem.\n")
	fs.AddText("/sandbox/examples/hello.txt", "hello\n")
	return &sandbox{
		os: vfs.Handler(fs, vfs.MapEnvironment{"HOME": "/sandbox", "SHMONTY_ENV": "sandbox"}),
		modules: map[string]monty.Module{"hostmod": {Name: "hostmod", Attrs: monty.Dict{
			{Key: monty.String("name"), Value: monty.String("shmonty")},
			{Key: monty.String("root"), Value: monty.String("/sandbox")},
		}}},
	}
}

// bindArguments rejects missing, duplicate, and unknown arguments so Python
// callers get a catchable TypeError instead of a Go callback failure.
func bindArguments(call monty.Call, names ...string) ([]monty.Value, error) {
	if len(call.Args) > len(names) {
		return nil, fmt.Errorf("too many positional arguments")
	}
	values := make([]monty.Value, len(names))
	seen := make([]bool, len(names))
	for i, value := range call.Args {
		values[i], seen[i] = value, true
	}
	for _, pair := range call.Kwargs {
		key, ok := pair.Key.Raw().(string)
		index := -1
		if ok {
			for i, name := range names {
				if key == name {
					index = i
					break
				}
			}
		}
		if index < 0 {
			return nil, fmt.Errorf("unexpected keyword argument %q", key)
		}
		if seen[index] {
			return nil, fmt.Errorf("multiple values for argument %q", key)
		}
		values[index], seen[index] = pair.Value, true
	}
	for i, name := range names {
		if !seen[i] {
			return nil, fmt.Errorf("missing argument %q", name)
		}
	}
	return values, nil
}

func hostCalc(_ context.Context, call monty.Call) (monty.Result, error) {
	args, err := bindArguments(call, "op", "a", "b")
	if err != nil {
		return hostException("TypeError", err.Error()), nil
	}
	op, ok := args[0].Raw().(string)
	if !ok {
		return hostException("TypeError", "op must be a string"), nil
	}
	numbers := make([]float64, 2)
	for i, arg := range args[1:] {
		switch n := arg.Raw().(type) {
		case int64:
			numbers[i] = float64(n)
		case float64:
			numbers[i] = n
		default:
			return hostException("TypeError", "a and b must be numbers"), nil
		}
	}
	a, b := numbers[0], numbers[1]
	var result float64
	switch op {
	case "add":
		result = a + b
	case "sub":
		result = a - b
	case "mul":
		result = a * b
	case "div":
		if b == 0 {
			return hostException("ZeroDivisionError", "division by zero"), nil
		}
		result = a / b
	default:
		return hostException("ValueError", "unknown operation: "+op), nil
	}
	return monty.Return(monty.Float(result)), nil
}

func hostEcho(_ context.Context, call monty.Call) (monty.Result, error) {
	args, err := bindArguments(call, "value")
	if err != nil {
		return hostException("TypeError", err.Error()), nil
	}
	return monty.Return(monty.DictValue(monty.Dict{
		{Key: monty.String("value"), Value: args[0]},
		{Key: monty.String("source"), Value: monty.String("shmonty")},
	})), nil
}

func hostFail(_ context.Context, call monty.Call) (monty.Result, error) {
	args, err := bindArguments(call, "msg")
	if err != nil {
		return hostException("TypeError", err.Error()), nil
	}
	msg, ok := args[0].Raw().(string)
	if !ok {
		return hostException("TypeError", "msg must be a string"), nil
	}
	return hostException("ValueError", msg), nil
}

func hostException(kind, message string) monty.Result {
	return monty.Raise(monty.Exception{Type: kind, Arg: &message})
}

// delayWaiter resolves with value once its delay has elapsed. The deadline is
// fixed when host_sleep is called, so waiting again after another future
// resolved first never restarts the delay.
type delayWaiter struct {
	deadline time.Time
	value    monty.Value
}

func (w delayWaiter) Wait(ctx context.Context) monty.Result {
	timer := time.NewTimer(time.Until(w.deadline))
	defer timer.Stop()
	select {
	case <-timer.C:
		return monty.Return(w.value)
	case <-ctx.Done():
		return hostException("TimeoutError", ctx.Err().Error())
	}
}

// hostSleep is an async host function: it returns a pending future that
// resolves with value after the delay, so scripts can await it and
// asyncio.gather several, exercising futures end to end.
func hostSleep(_ context.Context, call monty.Call) (monty.Result, error) {
	names := []string{"seconds"}
	if len(call.Args)+len(call.Kwargs) > 1 {
		names = append(names, "value")
	}
	args, err := bindArguments(call, names...)
	if err != nil {
		return hostException("TypeError", err.Error()), nil
	}
	var seconds float64
	switch n := args[0].Raw().(type) {
	case int64:
		seconds = float64(n)
	case float64:
		seconds = n
	default:
		return hostException("TypeError", "seconds must be a number"), nil
	}
	if seconds < 0 || seconds > maxHostSleepSeconds {
		return hostException("ValueError", fmt.Sprintf("seconds must be between 0 and %d", maxHostSleepSeconds)), nil
	}
	value := monty.None()
	if len(args) > 1 {
		value = args[1]
	}
	return monty.Pending(delayWaiter{deadline: time.Now().Add(time.Duration(seconds * float64(time.Second))), value: value}), nil
}

// maxHostSleepSeconds keeps a typo from parking a session for long.
const maxHostSleepSeconds = 30
