package monty

import (
	"context"
	"errors"
	"fmt"

	"github.com/mdfranz/gomonty/internal/ffi"
)

// ffiBackend runs the interpreter in-process through the native shared
// library, exchanging MessagePack payloads with it.
type ffiBackend struct{}

type ffiRunner struct{ h *ffi.Runner }
type ffiRepl struct{ h *ffi.Repl }
type ffiProgress struct{ h *ffi.Progress }

func (ffiBackend) NewRunner(code string, opts CompileOptions) (RunnerHandle, error) {
	payload, err := marshalWire(newWireCompileOptions(opts))
	if err != nil {
		return nil, err
	}
	result := ffi.NewRunner([]byte(code), payload)
	if result.Error != nil {
		return nil, newError(result.Error)
	}
	return &ffiRunner{h: result.Runner}, nil
}

func (ffiBackend) LoadRunner(data []byte) (RunnerHandle, error) {
	result := ffi.LoadRunner(data)
	if result.Error != nil {
		return nil, newError(result.Error)
	}
	return &ffiRunner{h: result.Runner}, nil
}

func (ffiBackend) NewRepl(opts ReplOptions) (ReplHandle, error) {
	payload, err := marshalWire(newWireReplOptions(opts))
	if err != nil {
		return nil, err
	}
	result := ffi.NewRepl(payload)
	if result.Error != nil {
		return nil, newError(result.Error)
	}
	return &ffiRepl{h: result.Repl}, nil
}

func (ffiBackend) LoadProgress(data []byte) Step {
	return stepFromOp(ffi.LoadProgress(data))
}

func (r *ffiRunner) Close() { r.h.Close() }

func (r *ffiRunner) Dump() ([]byte, error) {
	data, err := r.h.Dump()
	if err != nil {
		return nil, newError(err)
	}
	return data, nil
}

func (r *ffiRunner) TypeCheck(prefix string) error {
	return newError(r.h.TypeCheck([]byte(prefix)))
}

func (r *ffiRunner) Start(_ context.Context, opts StartOptions) Step {
	wireOptions, err := newWireStartOptions(opts)
	if err != nil {
		return Step{Err: err, Retained: true}
	}
	payload, err := marshalWire(wireOptions)
	if err != nil {
		return Step{Err: err, Retained: true}
	}
	return stepFromOp(r.h.Start(payload))
}

func (r *ffiRepl) Close() { r.h.Close() }

func (r *ffiRepl) Dump() ([]byte, error) {
	data, err := r.h.Dump()
	if err != nil {
		return nil, newError(err)
	}
	return data, nil
}

func (r *ffiRepl) FeedStart(_ context.Context, code string, opts FeedStartOptions) Step {
	wireOptions, err := newWireFeedOptions(opts)
	if err != nil {
		return Step{Err: err, Retained: true}
	}
	payload, err := marshalWire(wireOptions)
	if err != nil {
		return Step{Err: err, Retained: true}
	}
	return stepFromOp(r.h.FeedStart([]byte(code), payload))
}

func (p *ffiProgress) Close() { p.h.Close() }

func (p *ffiProgress) Dump() ([]byte, error) {
	data, err := p.h.Dump()
	if err != nil {
		return nil, newError(err)
	}
	return data, nil
}

func (p *ffiProgress) TakeRepl() (ReplHandle, error) {
	result := p.h.TakeRepl()
	if result.Error != nil {
		return nil, newError(result.Error)
	}
	return &ffiRepl{h: result.Repl}, nil
}

func (p *ffiProgress) ResumeCall(_ context.Context, result Result) Step {
	wireResult, err := newWireCallResult(result)
	if err != nil {
		return Step{Err: err, Retained: true}
	}
	payload, err := marshalWire(wireResult)
	if err != nil {
		return Step{Err: err, Retained: true}
	}
	return stepFromOp(p.h.ResumeCall(payload))
}

func (p *ffiProgress) ResumeLookup(_ context.Context, value Value, found bool) Step {
	var wireResult wireLookupResult
	if found {
		var err error
		if wireResult, err = newWireLookupResult(value); err != nil {
			return Step{Err: err, Retained: true}
		}
	} else {
		wireResult = wireLookupResult{Kind: wireLookupResultUndefined}
	}
	payload, err := marshalWire(wireResult)
	if err != nil {
		return Step{Err: err, Retained: true}
	}
	return stepFromOp(p.h.ResumeLookup(payload))
}

func (p *ffiProgress) ResumeFutures(_ context.Context, results map[uint32]Result) Step {
	wireResults, err := newWireFutureResults(results)
	if err != nil {
		return Step{Err: err, Retained: true}
	}
	payload, err := marshalWire(wireResults)
	if err != nil {
		return Step{Err: err, Retained: true}
	}
	return stepFromOp(p.h.ResumeFutures(payload))
}

// stepFromOp converts a native operation result into a Step, decoding the
// progress description so nothing above the backend sees a wire payload.
func stepFromOp(result ffi.OpResult) Step {
	st := Step{Prints: result.Prints}
	if result.Repl != nil {
		st.Repl = &ffiRepl{h: result.Repl}
	}
	if result.Progress != nil {
		st.Progress = &ffiProgress{h: result.Progress}
	}
	if result.Error != nil {
		st.Err = newError(result.Error)
		return st
	}
	if result.Progress == nil {
		return st
	}
	desc, err := decodeProgressDesc(result.ProgressPayload)
	if err != nil {
		st.Err = err
		return st
	}
	st.Desc = desc
	return st
}

func decodeProgressDesc(payload []byte) (ProgressDesc, error) {
	if len(payload) == 0 {
		return ProgressDesc{}, errors.New("monty ffi returned no progress payload")
	}

	var wire wireProgressPayload
	if err := unmarshalWire(payload, &wire); err != nil {
		return ProgressDesc{}, fmt.Errorf("invalid progress payload: %w", err)
	}

	desc := ProgressDesc{ScriptName: wire.ScriptName, IsRepl: wire.IsRepl}
	switch wire.Variant {
	case wireProgressFunctionCall:
		args, err := valuesFromWire(wire.Args)
		if err != nil {
			return ProgressDesc{}, err
		}
		kwargs, err := dictFromWirePairs(wire.Kwargs)
		if err != nil {
			return ProgressDesc{}, err
		}
		desc.Kind = ProgressFunctionCall
		desc.FunctionName = wire.FunctionName
		desc.Args = args
		desc.Kwargs = kwargs
		desc.CallID = wire.CallID
		desc.IsOSFunction = wire.IsOSFunction
		desc.IsMethodCall = wire.IsMethodCall
	case wireProgressNameLookup:
		desc.Kind = ProgressNameLookup
		desc.VariableName = wire.VariableName
	case wireProgressFuture:
		desc.Kind = ProgressFuture
		desc.PendingCallIDs = append([]uint32(nil), wire.PendingCallIDs...)
	case wireProgressComplete:
		if wire.Output == nil {
			return ProgressDesc{}, errors.New("complete progress payload is missing output")
		}
		output, err := wire.Output.toPublic()
		if err != nil {
			return ProgressDesc{}, err
		}
		desc.Kind = ProgressComplete
		desc.Output = output
	default:
		return ProgressDesc{}, fmt.Errorf("unknown progress variant %q", wire.Variant)
	}
	return desc, nil
}
