package monty

import (
	"context"
	"errors"
	"fmt"
	"time"
)

type dispatchConfig struct {
	functions        map[string]ExternalFunction
	os               OSHandler
	modules          map[string]Module
	print            PrintCallback
	telemetry        TelemetryHandler
	telemetryOptions TelemetryOptions
}

type restorableProgress interface {
	restoreOwner() error
}

type waitOutcome struct {
	callID uint32
	result Result
}

// dispatchTiming accumulates cumulative callback and future-wait time
// across an entire dispatchLoop call, for ExecutionTiming on the root
// span. It's a running total, not per-call: a script making several
// sequential external calls sums all of their durations here.
type dispatchTiming struct {
	callback time.Duration
	wait     time.Duration
}

func dispatchLoop(ctx context.Context, progress Progress, cfg dispatchConfig) (Value, error, dispatchTiming) {
	waiters := make(map[uint32]Waiter)
	var timing dispatchTiming

	for {
		if err := ctx.Err(); err != nil {
			return Value{}, restoreProgressOwner(progress, err), timing
		}

		switch current := progress.(type) {
		case *Complete:
			return current.Output, nil, timing
		case *Snapshot:
			callStart := time.Now()
			result, err := dispatchSnapshot(ctx, current, cfg)
			timing.callback += time.Since(callStart)
			if err != nil {
				return Value{}, restoreProgressOwner(current, err), timing
			}

			if result.wirePending() {
				waiter := result.waiterValue()
				if waiter == nil {
					return Value{}, restoreProgressOwner(current, fmt.Errorf("pending result for call %d is missing a waiter", current.CallID)), timing
				}
				waiters[current.CallID] = waiter
			}
			progress, err = current.progressBase.resumeCall(ctx, result, cfg.print, printTelemetry{handler: cfg.telemetry, opts: cfg.telemetryOptions})
			if err != nil {
				return Value{}, err, timing
			}
		case *NameLookupSnapshot:
			next, err := dispatchNameLookup(ctx, current, cfg)
			if err != nil {
				return Value{}, restoreProgressOwner(current, err), timing
			}
			progress = next
		case *FutureSnapshot:
			waitStart := time.Now()
			results, err := waitForFutureResults(ctx, current.PendingCallIDs(), waiters, cfg)
			timing.wait += time.Since(waitStart)
			if err != nil {
				return Value{}, restoreProgressOwner(current, err), timing
			}
			progress, err = current.progressBase.resumeFutures(ctx, results, cfg.print, printTelemetry{handler: cfg.telemetry, opts: cfg.telemetryOptions})
			if err != nil {
				return Value{}, err, timing
			}
			for callID, result := range results {
				delete(waiters, callID)
				if result.wirePending() && result.waiterValue() != nil {
					waiters[callID] = result.waiterValue()
				}
			}
		default:
			return Value{}, fmt.Errorf("unsupported progress type %T", current), timing
		}
	}
}

func dispatchSnapshot(ctx context.Context, snapshot *Snapshot, cfg dispatchConfig) (Result, error) {
	if snapshot.IsOSFunction && isSystemSleep(snapshot.FunctionName) {
		return dispatchSystemSleep(ctx, snapshot, cfg)
	}
	if snapshot.IsOSFunction {
		osHandler := cfg.os
		if snapshot.FunctionName == string(OSImport) || snapshot.FunctionName == string(OSImportStubs) {
			osHandler = importHandler(cfg.modules, cfg.os)
		}
		if osHandler == nil && snapshot.FunctionName == string(OSGetTypeHints) {
			// A host that does not handle type hints gives {}.
			return Return(DictValue(nil)), nil
		}
		if osHandler == nil {
			// No call was actually attempted (no OS handler configured at
			// all), so there's nothing to instrument here.
			message := fmt.Sprintf("OS function %s called but no OS handler was provided", snapshot.FunctionName)
			return Raise(Exception{Type: "NotImplementedError", Arg: &message}), nil
		}
		callCtx, span := startCallbackSpan(ctx, cfg.telemetry, CallbackInfo{
			FunctionName: snapshot.FunctionName,
			IsOSFunction: true,
			IsMethodCall: snapshot.IsMethodCall,
			CallID:       snapshot.CallID,
			Arguments: truncatedPayload(cfg.telemetryOptions.RecordArguments, func() string {
				return renderArguments(snapshot.Args, snapshot.Kwargs)
			}, cfg.telemetryOptions.MaxAttributeBytes),
		}, cfg.telemetryOptions)
		result, err := normalizeCallbackResult(osHandler(callCtx, OSCall{
			Function: OSFunction(snapshot.FunctionName),
			Args:     snapshot.Args,
			Kwargs:   snapshot.Kwargs,
			CallID:   snapshot.CallID,
		}))
		endCallbackSpan(span, result, err, cfg.telemetryOptions)
		return result, err
	}

	// Upstream now routes a method call to a host-object receiver by
	// identity (a uuid, not carried on Snapshot yet) rather than passing
	// self as Args[0] — gomonty does not yet track registered class
	// instances, so a method call is dispatched the same way as a plain
	// external function, keyed by name only. IsMethodCall stays available
	// so callers driving the low-level Start/Progress API directly can
	// tell the two apart.
	handler, ok := cfg.functions[snapshot.FunctionName]
	if !ok {
		// No call was actually attempted (no matching handler registered),
		// so there's nothing to instrument here.
		message := fmt.Sprintf("unable to find %q in external functions", snapshot.FunctionName)
		return Raise(Exception{Type: "LookupError", Arg: &message}), nil
	}
	callCtx, span := startCallbackSpan(ctx, cfg.telemetry, CallbackInfo{
		FunctionName: snapshot.FunctionName,
		IsOSFunction: false,
		IsMethodCall: snapshot.IsMethodCall,
		CallID:       snapshot.CallID,
		Arguments: truncatedPayload(cfg.telemetryOptions.RecordArguments, func() string {
			return renderArguments(snapshot.Args, snapshot.Kwargs)
		}, cfg.telemetryOptions.MaxAttributeBytes),
	}, cfg.telemetryOptions)
	result, err := normalizeCallbackResult(handler(callCtx, Call{
		FunctionName: snapshot.FunctionName,
		Args:         snapshot.Args,
		Kwargs:       snapshot.Kwargs,
		CallID:       snapshot.CallID,
		IsMethodCall: snapshot.IsMethodCall,
	}))
	endCallbackSpan(span, result, err, cfg.telemetryOptions)
	return result, err
}

// OS function names for sleeps under upstream's default SleepMode::System.
// The host waits itself and never involves its OS handler.
const (
	osSystemSleep      = "system.sleep"
	osSystemAsyncSleep = "system.async_sleep"
)

func isSystemSleep(name string) bool {
	return name == osSystemSleep || name == osSystemAsyncSleep
}

// sleepWaiter resolves with None once its delay has elapsed.
// The deadline is fixed when the call arrives, so repeated Waits (the loop
// re-waits after another future resolves first) never restart the delay.
type sleepWaiter struct{ deadline time.Time }

func (w sleepWaiter) Wait(ctx context.Context) Result {
	if err := sleepContext(ctx, time.Until(w.deadline)); err != nil {
		return Raise(exceptionFromError(err))
	}
	return Return(None())
}

// sleepContext waits for d, or until ctx ends, whichever comes first.
func sleepContext(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return ctx.Err()
	}
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

// sleepDuration reads the delay in seconds that upstream sends as the only
// argument, treating missing, negative and NaN values as zero. Upstream has
// already capped it.
func sleepDuration(args []Value) time.Duration {
	if len(args) == 0 {
		return 0
	}
	seconds, ok := args[0].Raw().(float64)
	if !ok || !(seconds > 0) {
		return 0
	}
	const maxSeconds = float64(1<<62) / float64(time.Second)
	if seconds > maxSeconds {
		seconds = maxSeconds
	}
	return time.Duration(seconds * float64(time.Second))
}

// dispatchSystemSleep answers system.sleep by waiting and system.async_sleep
// with a pending future that resolves after the delay, so other tasks run
// meanwhile. Both are recorded as OS callback spans; for the async form the
// span covers only scheduling, and the wait shows up as a monty.wait span.
// Upstream does not charge sleeping against MaxDuration.
func dispatchSystemSleep(ctx context.Context, snapshot *Snapshot, cfg dispatchConfig) (Result, error) {
	callCtx, span := startCallbackSpan(ctx, cfg.telemetry, CallbackInfo{
		FunctionName: snapshot.FunctionName,
		IsOSFunction: true,
		IsMethodCall: snapshot.IsMethodCall,
		CallID:       snapshot.CallID,
		Arguments: truncatedPayload(cfg.telemetryOptions.RecordArguments, func() string {
			return renderArguments(snapshot.Args, snapshot.Kwargs)
		}, cfg.telemetryOptions.MaxAttributeBytes),
	}, cfg.telemetryOptions)
	delay := sleepDuration(snapshot.Args)
	if snapshot.FunctionName == osSystemAsyncSleep {
		result := Pending(sleepWaiter{deadline: time.Now().Add(delay)})
		endCallbackSpan(span, result, nil, cfg.telemetryOptions)
		return result, nil
	}
	if err := sleepContext(callCtx, delay); err != nil {
		endCallbackSpan(span, Result{}, err, cfg.telemetryOptions)
		return Result{}, err
	}
	result := Return(None())
	endCallbackSpan(span, result, nil, cfg.telemetryOptions)
	return result, nil
}

func dispatchNameLookup(ctx context.Context, lookup *NameLookupSnapshot, cfg dispatchConfig) (Progress, error) {
	pt := printTelemetry{handler: cfg.telemetry, opts: cfg.telemetryOptions}
	if handler, ok := cfg.functions[lookup.VariableName]; ok && handler != nil {
		value := FunctionValue(Function{Name: lookup.VariableName})
		return lookup.progressBase.resumeLookup(ctx, value, true, cfg.print, pt)
	}

	return lookup.progressBase.resumeLookup(ctx, Value{}, false, cfg.print, pt)
}

func waitForFutureResults(ctx context.Context, pending []uint32, waiters map[uint32]Waiter, cfg dispatchConfig) (map[uint32]Result, error) {
	currentWaiters := make(map[uint32]Waiter, len(pending))
	for _, callID := range pending {
		waiter, ok := waiters[callID]
		if !ok {
			continue
		}
		currentWaiters[callID] = waiter
	}
	if len(currentWaiters) == 0 {
		return nil, fmt.Errorf("no waiters registered for pending call IDs %v", pending)
	}

	waitCtx, span := startWaitSpan(ctx, cfg.telemetry, WaitInfo{PendingCallIDs: pending}, cfg.telemetryOptions)

	outcomes := make(chan waitOutcome, len(currentWaiters))
	for callID, waiter := range currentWaiters {
		go func(callID uint32, waiter Waiter) {
			outcomes <- waitOutcome{
				callID: callID,
				result: waiter.Wait(waitCtx),
			}
		}(callID, waiter)
	}

	var first waitOutcome
	select {
	case <-waitCtx.Done():
		err := waitCtx.Err()
		endWaitSpan(span, err, cfg.telemetryOptions)
		return nil, err
	case first = <-outcomes:
	}

	results := map[uint32]Result{
		first.callID: first.result,
	}
	for {
		select {
		case outcome := <-outcomes:
			results[outcome.callID] = outcome.result
		default:
			endWaitSpan(span, nil, cfg.telemetryOptions)
			return results, nil
		}
	}
}

func normalizeCallbackResult(result Result, err error) (Result, error) {
	if err != nil {
		return Raise(exceptionFromError(err)), nil
	}
	switch {
	case result.wirePending() && result.waiterValue() == nil:
		return Result{}, errors.New("pending callback result is missing a waiter")
	case result.wireException() == nil && result.kind == resultKindException:
		return Result{}, errors.New("exception callback result is missing an exception")
	case result.kind == resultKindReturn && result.value.Kind() == ValueKindNone:
		return Return(None()), nil
	default:
		return result, nil
	}
}

func restoreProgressOwner(progress Progress, err error) error {
	restorable, ok := progress.(restorableProgress)
	if !ok {
		return err
	}
	// ErrClosed means the snapshot was already consumed by a resume, which
	// restored or cleared its owner itself, so there is nothing left to do.
	if restoreErr := restorable.restoreOwner(); restoreErr != nil && !errors.Is(restoreErr, ErrClosed) {
		return errors.Join(err, restoreErr)
	}
	return err
}

// importHandler answers the OS calls the sandbox makes for host modules:
// OSImport asks for a module by top-level name and OSImportStubs for the type
// stubs of names about to be type-checked. modules is consulted first, then
// fallback (which may be nil). An import neither resolves raises
// ModuleNotFoundError, as the sandbox does for any unknown module, and with
// no stubs known OSImportStubs answers an empty dict.
func importHandler(modules map[string]Module, fallback OSHandler) OSHandler {
	return func(ctx context.Context, call OSCall) (Result, error) {
		switch call.Function {
		case OSImport:
			name, _ := stringArg(call.Args, 0)
			if module, ok := modules[name]; ok {
				return Return(ModuleValue(module)), nil
			}
			if fallback != nil {
				return fallback(ctx, call)
			}
			return NotFound(), nil
		case OSImportStubs:
			var names []Value
			if len(call.Args) > 0 {
				names, _ = call.Args[0].Raw().([]Value)
			}
			stubs := make(Dict, 0, len(names))
			complete := true
			for _, name := range names {
				text, _ := name.Raw().(string)
				module, ok := modules[text]
				if !ok {
					complete = false
					continue
				}
				if module.Stub != "" {
					stubs = append(stubs, Pair{Key: String(text), Value: String(module.Stub)})
				}
			}
			if !complete && fallback != nil {
				return fallback(ctx, call)
			}
			return Return(DictValue(stubs)), nil
		default:
			return Result{}, fmt.Errorf("importHandler: unexpected OS function %q", call.Function)
		}
	}
}

func stringArg(args []Value, index int) (string, bool) {
	if index >= len(args) {
		return "", false
	}
	text, ok := args[index].Raw().(string)
	return text, ok
}
