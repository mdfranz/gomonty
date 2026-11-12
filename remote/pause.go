package remote

import (
	"context"
	"errors"
	"fmt"
	"time"

	monty "github.com/mdfranz/gomonty"
	montyv1 "github.com/mdfranz/gomonty/remote/internal/montyv1"
)

// pause hands a suspension to the host layer as a Step. The host layer's
// dispatch loop answers it through the suspension and carries on.
func (h *replHandle) pause(s *suspension, desc monty.ProgressDesc, prints string) monty.Step {
	desc.ScriptName = h.scriptName
	desc.IsRepl = true
	return monty.Step{Progress: s, Desc: desc, Prints: prints}
}

// pickAll resolves arena indexes to values.
func pickAll(values []monty.Value, indexes []uint32) ([]monty.Value, error) {
	out := make([]monty.Value, len(indexes))
	for i, idx := range indexes {
		v, err := pick(values, idx)
		if err != nil {
			return nil, err
		}
		out[i] = v
	}
	return out, nil
}

// pauseOnCall is a call to a host function.
func (h *replHandle) pauseOnCall(ctx context.Context, call *montyv1.FunctionCall, prints string) monty.Step {
	if call.GetObjectId() != nil {
		// A method on a host object. gomonty has no way to route one yet (#57).
		return h.abortFeed(ctx, "a method call on a host object", prints)
	}
	values, err := decodeArena(call.GetValues())
	if err != nil {
		return h.abortFeedWith(ctx, err, "the remote backend could not read the call", prints)
	}
	args, err := pickAll(values, call.GetArgs())
	if err != nil {
		return h.abortFeedWith(ctx, err, "the remote backend could not read the call", prints)
	}
	kwargs := make(monty.Dict, 0, len(call.GetKwargs()))
	for _, pair := range call.GetKwargs() {
		key, kerr := pick(values, pair.GetKey())
		value, verr := pick(values, pair.GetValue())
		if err := errors.Join(kerr, verr); err != nil {
			return h.abortFeedWith(ctx, err, "the remote backend could not read the call", prints)
		}
		kwargs = append(kwargs, monty.Pair{Key: key, Value: value})
	}
	name := call.GetFunctionName()
	return h.pause(&suspension{repl: h, kind: pausedOnCall, callID: call.GetCallId(), name: name}, monty.ProgressDesc{
		Kind:         monty.ProgressFunctionCall,
		FunctionName: name,
		Args:         args,
		Kwargs:       kwargs,
		CallID:       call.GetCallId(),
	}, prints)
}

// pauseOnOS is an OS call. Most are passed to the host's OS handler under the
// names and arguments the native library uses; some the backend answers itself.
func (h *replHandle) pauseOnOS(ctx context.Context, call *montyv1.OsCall, prints string) monty.Step {
	values, err := decodeArena(call.GetValues())
	if err != nil {
		return h.abortFeedWith(ctx, err, "the remote backend could not read the OS call", prints)
	}
	plan, err := planOSCall(call, values)
	if err != nil {
		return h.abortFeedWith(ctx, err, "the remote backend could not read the OS call", prints)
	}
	if plan.local != askHost {
		return h.answerLocally(ctx, call.GetCallId(), plan, prints)
	}

	name := plan.name
	if plan.name == "import" && len(plan.args) > 0 {
		// An import that nobody finds is reported against the module's name.
		if module, ok := plan.args[0].Raw().(string); ok {
			name = module
		}
	}
	return h.pause(&suspension{repl: h, kind: pausedOnCall, callID: call.GetCallId(), name: name}, monty.ProgressDesc{
		Kind:         monty.ProgressFunctionCall,
		FunctionName: plan.name,
		Args:         plan.args,
		Kwargs:       plan.kwargs,
		CallID:       call.GetCallId(),
		IsOSFunction: true,
	}, prints)
}

// answerLocally answers an OS call without asking the host, then carries on
// with whatever the worker does next.
func (h *replHandle) answerLocally(ctx context.Context, callID uint32, plan osPlan, prints string) monty.Step {
	b := &arenaBuilder{}
	var ext *montyv1.ExtFunctionResult
	switch plan.local {
	case answerAfterSleep:
		if err := sleep(ctx, plan.sleep); err != nil {
			// Giving up mid-sleep leaves the worker waiting on us.
			return monty.Step{Err: h.sess.fail(err), Prints: prints}
		}
		idx, err := b.encode(monty.None())
		if err != nil {
			return monty.Step{Err: err, Prints: prints}
		}
		ext = &montyv1.ExtFunctionResult{Kind: &montyv1.ExtFunctionResult_ReturnValue{ReturnValue: idx}}
	default:
		ext = &montyv1.ExtFunctionResult{Kind: &montyv1.ExtFunctionResult_NotHandled{NotHandled: &montyv1.Unit{}}}
	}
	t, err := h.sess.call(ctx, &montyv1.ClientRequest{Kind: &montyv1.ClientRequest_ResumeCall{
		ResumeCall: &montyv1.ResumeCall{CallId: callID, Result: ext, Values: b.arena()},
	}})
	if err != nil {
		return monty.Step{Err: err, Prints: prints}
	}
	t.prints = prints + t.prints
	return h.stepFor(ctx, t)
}

// sleep waits for d, or until ctx ends.
func sleep(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return ctx.Err()
	}
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-timer.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// pauseOnName is a name the sandbox wants from the host.
func (h *replHandle) pauseOnName(ctx context.Context, lookup *montyv1.NameLookup, prints string) monty.Step {
	if lookup.GetObjectId() != nil {
		return h.abortFeed(ctx, "an attribute of a host object", prints)
	}
	name := lookup.GetName()
	return h.pause(&suspension{repl: h, kind: pausedOnName, name: name}, monty.ProgressDesc{
		Kind:         monty.ProgressNameLookup,
		VariableName: name,
	}, prints)
}

// pauseOnFutures is every sandbox task blocked on host futures.
func (h *replHandle) pauseOnFutures(f *montyv1.ResolveFutures, prints string) monty.Step {
	ids := append([]uint32(nil), f.GetPendingCallIds()...)
	return h.pause(&suspension{repl: h, kind: pausedOnFutures, name: fmt.Sprintf("futures %v", ids)}, monty.ProgressDesc{
		Kind:           monty.ProgressFuture,
		PendingCallIDs: ids,
	}, prints)
}
