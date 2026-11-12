package remote

import (
	"context"
	"fmt"
	"sort"
	"time"

	monty "github.com/mdfranz/gomonty"
	montyv1 "github.com/mdfranz/gomonty/remote/internal/montyv1"
)

// abandonTimeout bounds how long giving up on a paused snippet may take.
const abandonTimeout = 10 * time.Second

type pauseKind int

const (
	// pausedOnCall is a host function call or an OS call, answered by
	// ResumeCall.
	pausedOnCall pauseKind = iota
	// pausedOnName is a name the sandbox wants from the host, answered by
	// ResumeLookup.
	pausedOnName
	// pausedOnFutures is every task blocked on host futures, answered by
	// ResumeFutures.
	pausedOnFutures
)

func (k pauseKind) String() string {
	switch k {
	case pausedOnCall:
		return "a call"
	case pausedOnName:
		return "a name lookup"
	default:
		return "futures"
	}
}

// suspension is a snippet paused on something the host must answer. It is the
// ProgressHandle behind monty.Snapshot, NameLookupSnapshot and FutureSnapshot.
// The session stays with repl, which is also where the answer's outcome is
// turned into the next Step.
type suspension struct {
	repl   *replHandle
	kind   pauseKind
	callID uint32
	// name is what a "not found" answer names: the function, or the module of
	// an import.
	name string
}

var _ monty.ProgressHandle = (*suspension)(nil)

// Close does nothing: the session belongs to the REPL handle, which outlives
// this pause.
func (s *suspension) Close() {}

func (s *suspension) Dump() ([]byte, error) {
	return nil, fmt.Errorf("%w: Dump", ErrUnsupported)
}

// TakeRepl gives up on the paused snippet and returns the session ready for
// another one. The host layer calls it to abandon a snippet, for example after
// a host function failed or the caller's context ended.
func (s *suspension) TakeRepl() (monty.ReplHandle, error) {
	ctx, cancel := context.WithTimeout(context.Background(), abandonTimeout)
	defer cancel()
	if _, err := s.repl.abort(ctx, "the host abandoned the snippet"); err != nil {
		return nil, err
	}
	return s.repl, nil
}

func (s *suspension) wrongResume(method string) monty.Step {
	return monty.Step{
		Err:      fmt.Errorf("remote: %s cannot answer a pause on %s", method, s.kind),
		Retained: true,
	}
}

func (s *suspension) ResumeCall(ctx context.Context, result monty.Result) monty.Step {
	if s.kind != pausedOnCall {
		return s.wrongResume("ResumeCall")
	}
	b := &arenaBuilder{}
	ext, err := encodeResult(b, result, s.callID, s.name)
	if err != nil {
		// Nothing was sent, so the pause stands and can be answered again.
		return monty.Step{Err: err, Retained: true}
	}
	return s.repl.proceed(ctx, &montyv1.ClientRequest{Kind: &montyv1.ClientRequest_ResumeCall{
		ResumeCall: &montyv1.ResumeCall{CallId: s.callID, Result: ext, Values: b.arena()},
	}})
}

func (s *suspension) ResumeLookup(ctx context.Context, value monty.Value, found bool) monty.Step {
	if s.kind != pausedOnName {
		return s.wrongResume("ResumeLookup")
	}
	b := &arenaBuilder{}
	lookup := &montyv1.ResumeNameLookup{}
	if found {
		idx, err := b.encode(value)
		if err != nil {
			return monty.Step{Err: err, Retained: true}
		}
		lookup.Kind = &montyv1.ResumeNameLookup_Value{Value: idx}
	} else {
		lookup.Kind = &montyv1.ResumeNameLookup_Undefined{Undefined: &montyv1.Unit{}}
	}
	lookup.Values = b.arena()
	return s.repl.proceed(ctx, &montyv1.ClientRequest{Kind: &montyv1.ClientRequest_ResumeNameLookup{ResumeNameLookup: lookup}})
}

func (s *suspension) ResumeFutures(ctx context.Context, results map[uint32]monty.Result) monty.Step {
	if s.kind != pausedOnFutures {
		return s.wrongResume("ResumeFutures")
	}
	ids := make([]uint32, 0, len(results))
	for id := range results {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })

	b := &arenaBuilder{}
	futures := make([]*montyv1.FutureResult, 0, len(ids))
	for _, id := range ids {
		ext, err := encodeResult(b, results[id], id, "")
		if err != nil {
			return monty.Step{Err: fmt.Errorf("call %d: %w", id, err), Retained: true}
		}
		futures = append(futures, &montyv1.FutureResult{CallId: id, Result: ext})
	}
	return s.repl.proceed(ctx, &montyv1.ClientRequest{Kind: &montyv1.ClientRequest_ResumeFutures{
		ResumeFutures: &montyv1.ResumeFutures{Results: futures, Values: b.arena()},
	}})
}

// encodeResult turns a host's answer into the protocol's ExtFunctionResult,
// adding any value to b's arena. name is what a "not found" answer names.
func encodeResult(b *arenaBuilder, r monty.Result, callID uint32, name string) (*montyv1.ExtFunctionResult, error) {
	switch {
	case r.IsPending():
		return &montyv1.ExtFunctionResult{Kind: &montyv1.ExtFunctionResult_Future{Future: callID}}, nil
	case r.IsNotFound():
		return &montyv1.ExtFunctionResult{Kind: &montyv1.ExtFunctionResult_NotFound{NotFound: name}}, nil
	}
	if exc, ok := r.Raised(); ok {
		raised := &montyv1.RaisedException{ExcType: exc.Type, Message: exc.Arg}
		return &montyv1.ExtFunctionResult{Kind: &montyv1.ExtFunctionResult_Error{Error: raised}}, nil
	}
	if file, ok := r.ModuleFile(); ok {
		return &montyv1.ExtFunctionResult{Kind: &montyv1.ExtFunctionResult_ModuleFile{
			ModuleFile: &montyv1.ModuleFile{Path: file.Path, Source: file.Source},
		}}, nil
	}
	idx, err := b.encode(r.Value())
	if err != nil {
		return nil, err
	}
	return &montyv1.ExtFunctionResult{Kind: &montyv1.ExtFunctionResult_ReturnValue{ReturnValue: idx}}, nil
}
