package monty

import (
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

func (ffiBackend) newRunner(code string, opts CompileOptions) (runnerHandle, error) {
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

func (ffiBackend) loadRunner(data []byte) (runnerHandle, error) {
	result := ffi.LoadRunner(data)
	if result.Error != nil {
		return nil, newError(result.Error)
	}
	return &ffiRunner{h: result.Runner}, nil
}

func (ffiBackend) newRepl(opts ReplOptions) (replHandle, error) {
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

func (ffiBackend) loadProgress(data []byte) step {
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

func (r *ffiRunner) Start(opts StartOptions) step {
	wireOptions, err := newWireStartOptions(opts)
	if err != nil {
		return step{err: err, retained: true}
	}
	payload, err := marshalWire(wireOptions)
	if err != nil {
		return step{err: err, retained: true}
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

func (r *ffiRepl) FeedStart(code string, opts FeedStartOptions) step {
	wireOptions, err := newWireFeedOptions(opts)
	if err != nil {
		return step{err: err, retained: true}
	}
	payload, err := marshalWire(wireOptions)
	if err != nil {
		return step{err: err, retained: true}
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

func (p *ffiProgress) TakeRepl() (replHandle, error) {
	result := p.h.TakeRepl()
	if result.Error != nil {
		return nil, newError(result.Error)
	}
	return &ffiRepl{h: result.Repl}, nil
}

func (p *ffiProgress) ResumeCall(result Result) step {
	wireResult, err := newWireCallResult(result)
	if err != nil {
		return step{err: err, retained: true}
	}
	payload, err := marshalWire(wireResult)
	if err != nil {
		return step{err: err, retained: true}
	}
	return stepFromOp(p.h.ResumeCall(payload))
}

func (p *ffiProgress) ResumeLookup(value Value, found bool) step {
	var wireResult wireLookupResult
	if found {
		var err error
		if wireResult, err = newWireLookupResult(value); err != nil {
			return step{err: err, retained: true}
		}
	} else {
		wireResult = wireLookupResult{Kind: wireLookupResultUndefined}
	}
	payload, err := marshalWire(wireResult)
	if err != nil {
		return step{err: err, retained: true}
	}
	return stepFromOp(p.h.ResumeLookup(payload))
}

func (p *ffiProgress) ResumeFutures(results map[uint32]Result) step {
	wireResults, err := newWireFutureResults(results)
	if err != nil {
		return step{err: err, retained: true}
	}
	payload, err := marshalWire(wireResults)
	if err != nil {
		return step{err: err, retained: true}
	}
	return stepFromOp(p.h.ResumeFutures(payload))
}

// stepFromOp converts a native operation result into a step, decoding the
// progress description so nothing above the backend sees a wire payload.
func stepFromOp(result ffi.OpResult) step {
	st := step{prints: result.Prints}
	if result.Repl != nil {
		st.repl = &ffiRepl{h: result.Repl}
	}
	if result.Progress != nil {
		st.progress = &ffiProgress{h: result.Progress}
	}
	if result.Error != nil {
		st.err = newError(result.Error)
		return st
	}
	if result.Progress == nil {
		return st
	}
	desc, err := decodeProgressDesc(result.ProgressPayload)
	if err != nil {
		st.err = err
		return st
	}
	st.desc = desc
	return st
}

func decodeProgressDesc(payload []byte) (progressDesc, error) {
	if len(payload) == 0 {
		return progressDesc{}, errors.New("monty ffi returned no progress payload")
	}

	var wire wireProgressPayload
	if err := unmarshalWire(payload, &wire); err != nil {
		return progressDesc{}, fmt.Errorf("invalid progress payload: %w", err)
	}

	desc := progressDesc{scriptName: wire.ScriptName, isRepl: wire.IsRepl}
	switch wire.Variant {
	case wireProgressFunctionCall:
		args, err := valuesFromWire(wire.Args)
		if err != nil {
			return progressDesc{}, err
		}
		kwargs, err := dictFromWirePairs(wire.Kwargs)
		if err != nil {
			return progressDesc{}, err
		}
		desc.kind = progressFunctionCall
		desc.functionName = wire.FunctionName
		desc.args = args
		desc.kwargs = kwargs
		desc.callID = wire.CallID
		desc.isOSFunction = wire.IsOSFunction
		desc.isMethodCall = wire.IsMethodCall
	case wireProgressNameLookup:
		desc.kind = progressNameLookup
		desc.variableName = wire.VariableName
	case wireProgressFuture:
		desc.kind = progressFuture
		desc.pendingCallIDs = append([]uint32(nil), wire.PendingCallIDs...)
	case wireProgressComplete:
		if wire.Output == nil {
			return progressDesc{}, errors.New("complete progress payload is missing output")
		}
		output, err := wire.Output.toPublic()
		if err != nil {
			return progressDesc{}, err
		}
		desc.kind = progressComplete
		desc.output = output
	default:
		return progressDesc{}, fmt.Errorf("unknown progress variant %q", wire.Variant)
	}
	return desc, nil
}
