package monty

// backend creates and restores the execution objects behind Runner, Repl and
// Progress. The native FFI library is the only implementation today; the
// interface is the seam that lets another one, such as a remote worker, take
// its place without changing the public API.
//
// The interfaces speak gomonty's own wire structs, not encoded bytes, so an
// implementation chooses its own encoding. The FFI backend marshals them to
// MessagePack.
type backend interface {
	newRunner(code string, opts wireCompileOptions) (runnerHandle, error)
	loadRunner(data []byte) (runnerHandle, error)
	newRepl(opts wireReplOptions) (replHandle, error)
	// loadProgress restores a serialized snapshot. The result carries the
	// restored progress handle and its description.
	loadProgress(data []byte) opResult
}

// runnerHandle is a compiled runner owned by a backend.
type runnerHandle interface {
	// Close releases the handle. It must be safe to call exactly once.
	Close()
	dump() ([]byte, error)
	typeCheck(prefix string) error
	start(opts wireStartOptions) opResult
}

// replHandle is a REPL session owned by a backend.
type replHandle interface {
	// Close releases the handle. It must be safe to call exactly once.
	Close()
	dump() ([]byte, error)
	// feedStart runs a snippet up to its first pause. It consumes the handle:
	// the caller must not use or Close it afterwards. The session comes back
	// in the result's repl field, either on failure or once the snippet is
	// complete and the progress handle gives it back through takeRepl.
	feedStart(code string, opts wireFeedOptions) opResult
}

// progressHandle is a paused execution owned by a backend.
type progressHandle interface {
	// Close releases the handle. It must be safe to call exactly once.
	Close()
	dump() ([]byte, error)
	// The resume methods continue execution. They do not consume the handle;
	// the caller closes it afterwards.
	resumeCall(result wireCallResult) opResult
	resumeLookup(result wireLookupResult) opResult
	resumeFutures(results wireFutureResults) opResult
	// takeRepl returns the REPL session a finished or abandoned snippet was
	// running in.
	takeRepl() (replHandle, error)
}

// opResult is the outcome of starting or resuming execution.
type opResult struct {
	// prints is text the script printed since the last result.
	prints string
	// err is set when the operation failed. repl and progress may still be
	// set, so the caller can release or reclaim them.
	err error
	// repl is the session handed back after a failure, when the backend can.
	repl replHandle
	// progress and desc describe the next pause or the completed run.
	progress progressHandle
	desc     *wireProgressPayload
}

// discard releases any handles the result still holds.
func (r opResult) discard() {
	if r.progress != nil {
		r.progress.Close()
	}
	if r.repl != nil {
		r.repl.Close()
	}
}

// defaultBackend is the backend used by the exported constructors.
var defaultBackend backend = ffiBackend{}
