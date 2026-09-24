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

			switch {
			case result.wirePending():
				waiter := result.waiterValue()
				if waiter == nil {
					return Value{}, restoreProgressOwner(current, fmt.Errorf("pending result for call %d is missing a waiter", current.CallID)), timing
				}
				waiters[current.CallID] = waiter
				progress, err = current.progressBase.resumeCall(ctx, mustMarshalCallResult(callResultPayload{Kind: "pending"}), cfg.print, printTelemetry{handler: cfg.telemetry, opts: cfg.telemetryOptions})
			case result.wireException() != nil:
				exception := result.wireException()
				progress, err = current.progressBase.resumeCall(ctx, mustMarshalCallResult(callResultPayload{
					Kind: "exception",
					Type: exception.Type,
					Arg:  exception.Arg,
				}), cfg.print, printTelemetry{handler: cfg.telemetry, opts: cfg.telemetryOptions})
			default:
				progress, err = current.progressBase.resumeCall(ctx, mustMarshalCallResult(callResultPayload{
					Kind:  "return",
					Value: result.wireValue(),
				}), cfg.print, printTelemetry{handler: cfg.telemetry, opts: cfg.telemetryOptions})
			}
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
			progress, err = current.progressBase.resumeFutures(ctx, mustMarshalFutureResults(results), cfg.print, printTelemetry{handler: cfg.telemetry, opts: cfg.telemetryOptions})
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
	if snapshot.IsOSFunction {
		if cfg.os == nil {
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
		result, err := normalizeCallbackResult(cfg.os(callCtx, OSCall{
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

func dispatchNameLookup(ctx context.Context, lookup *NameLookupSnapshot, cfg dispatchConfig) (Progress, error) {
	if handler, ok := cfg.functions[lookup.VariableName]; ok && handler != nil {
		value := FunctionValue(Function{Name: lookup.VariableName})
		return lookup.progressBase.resumeLookup(ctx, mustMarshalLookupResult(lookupResultPayload{
			Kind:  "value",
			Value: value,
		}), cfg.print, printTelemetry{handler: cfg.telemetry, opts: cfg.telemetryOptions})
	}

	return lookup.progressBase.resumeLookup(ctx, mustMarshalLookupResult(lookupResultPayload{
		Kind: "undefined",
	}), cfg.print, printTelemetry{handler: cfg.telemetry, opts: cfg.telemetryOptions})
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
	case result.kind == resultKindReturn && result.value.Kind() == valueKindNone:
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
	if restoreErr := restorable.restoreOwner(); restoreErr != nil {
		return errors.Join(err, restoreErr)
	}
	return err
}

func mustMarshalCallResult(payload callResultPayload) []byte {
	wireResult, err := payload.toWire()
	if err != nil {
		panic(err)
	}
	bytes, err := marshalWire(wireResult)
	if err != nil {
		panic(err)
	}
	return bytes
}

func mustMarshalLookupResult(payload lookupResultPayload) []byte {
	wireResult, err := payload.toWire()
	if err != nil {
		panic(err)
	}
	bytes, err := marshalWire(wireResult)
	if err != nil {
		panic(err)
	}
	return bytes
}

func mustMarshalFutureResults(results map[uint32]Result) []byte {
	payload, err := newWireFutureResults(results)
	if err != nil {
		panic(err)
	}
	bytes, err := marshalWire(payload)
	if err != nil {
		panic(err)
	}
	return bytes
}
