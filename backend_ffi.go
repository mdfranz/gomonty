package monty

import (
	"fmt"

	"github.com/mdfranz/gomonty/internal/ffi"
)

// ffiBackend drives the native library through internal/ffi.
type ffiBackend struct{}

type (
	ffiRunner   struct{ h *ffi.Runner }
	ffiRepl     struct{ h *ffi.Repl }
	ffiProgress struct{ h *ffi.Progress }
)

func (ffiBackend) newRunner(code string, opts wireCompileOptions) (runnerHandle, error) {
	payload, err := marshalWire(opts)
	if err != nil {
		return nil, err
	}
	result := ffi.NewRunner([]byte(code), payload)
	if result.Error != nil {
		return nil, newError(result.Error)
	}
	return ffiRunner{result.Runner}, nil
}

func (ffiBackend) loadRunner(data []byte) (runnerHandle, error) {
	result := ffi.LoadRunner(data)
	if result.Error != nil {
		return nil, newError(result.Error)
	}
	return ffiRunner{result.Runner}, nil
}

func (ffiBackend) newRepl(opts wireReplOptions) (replHandle, error) {
	payload, err := marshalWire(opts)
	if err != nil {
		return nil, err
	}
	result := ffi.NewRepl(payload)
	if result.Error != nil {
		return nil, newError(result.Error)
	}
	return ffiRepl{result.Repl}, nil
}

func (ffiBackend) loadProgress(data []byte) opResult {
	return ffiOpResult(ffi.LoadProgress(data))
}

func (r ffiRunner) Close() { r.h.Close() }

func (r ffiRunner) dump() ([]byte, error) {
	data, ffiErr := r.h.Dump()
	if ffiErr != nil {
		return nil, newError(ffiErr)
	}
	return data, nil
}

func (r ffiRunner) typeCheck(prefix string) error {
	if ffiErr := r.h.TypeCheck([]byte(prefix)); ffiErr != nil {
		return newError(ffiErr)
	}
	return nil
}

func (r ffiRunner) start(opts wireStartOptions) opResult {
	payload, err := marshalWire(opts)
	if err != nil {
		return opResult{err: err}
	}
	return ffiOpResult(r.h.Start(payload))
}

func (r ffiRepl) Close() { r.h.Close() }

func (r ffiRepl) dump() ([]byte, error) {
	data, ffiErr := r.h.Dump()
	if ffiErr != nil {
		return nil, newError(ffiErr)
	}
	return data, nil
}

func (r ffiRepl) feedStart(code string, opts wireFeedOptions) opResult {
	payload, err := marshalWire(opts)
	if err != nil {
		// Nothing was consumed, so hand the session back to the caller.
		return opResult{err: err, repl: r}
	}
	result := r.h.FeedStart([]byte(code), payload)
	r.h.Close()
	return ffiOpResult(result)
}

func (p ffiProgress) Close() { p.h.Close() }

func (p ffiProgress) dump() ([]byte, error) {
	data, ffiErr := p.h.Dump()
	if ffiErr != nil {
		return nil, newError(ffiErr)
	}
	return data, nil
}

func (p ffiProgress) resumeCall(result wireCallResult) opResult {
	payload, err := marshalWire(result)
	if err != nil {
		return opResult{err: err}
	}
	return ffiOpResult(p.h.ResumeCall(payload))
}

func (p ffiProgress) resumeLookup(result wireLookupResult) opResult {
	payload, err := marshalWire(result)
	if err != nil {
		return opResult{err: err}
	}
	return ffiOpResult(p.h.ResumeLookup(payload))
}

func (p ffiProgress) resumeFutures(results wireFutureResults) opResult {
	payload, err := marshalWire(results)
	if err != nil {
		return opResult{err: err}
	}
	return ffiOpResult(p.h.ResumeFutures(payload))
}

func (p ffiProgress) takeRepl() (replHandle, error) {
	result := p.h.TakeRepl()
	if result.Error != nil {
		return nil, newError(result.Error)
	}
	return wrapFFIRepl(result.Repl), nil
}

// wrapFFIRepl returns a nil interface for a nil handle, so callers can test
// the result against nil.
func wrapFFIRepl(h *ffi.Repl) replHandle {
	if h == nil {
		return nil
	}
	return ffiRepl{h}
}

// ffiOpResult converts a native result: the error becomes a Go error, handles
// are wrapped, and the progress payload is decoded.
func ffiOpResult(result ffi.OpResult) opResult {
	out := opResult{prints: result.Prints, repl: wrapFFIRepl(result.Repl)}
	if result.Progress != nil {
		out.progress = ffiProgress{result.Progress}
	}
	if result.Error != nil {
		out.err = newError(result.Error)
		return out
	}
	if len(result.ProgressPayload) > 0 {
		var desc wireProgressPayload
		if err := unmarshalWire(result.ProgressPayload, &desc); err != nil {
			out.err = fmt.Errorf("invalid progress payload: %w", err)
			return out
		}
		out.desc = &desc
	}
	return out
}

var (
	_ backend        = ffiBackend{}
	_ runnerHandle   = ffiRunner{}
	_ replHandle     = ffiRepl{}
	_ progressHandle = ffiProgress{}
)
