package monty

// backend is the seam between the host-facing API (Runner, Repl, the progress
// snapshots and the dispatch loop) and whatever actually runs the interpreter.
// The default backend (backend_ffi.go) drives the native library in-process;
// another backend can drive a remote worker.
//
// The interfaces work in typed Go values, not in a backend's wire format, so
// each backend owns its own encoding. They are unexported on purpose: the
// first non-FFI backend decides what, if anything, to export.
//
// Ownership rules every backend must follow, because the host-facing layer
// (runnerState, replState, progressState) relies on them:
//
//   - Close is idempotent, and safe after a resume has consumed the handle.
//   - Resume and feed calls consume the handle they are called on, except when
//     the returned step has retained set (the arguments could not be encoded
//     before anything was sent), in which case the handle is still usable.
type backend interface {
	newRunner(code string, opts CompileOptions) (runnerHandle, error)
	loadRunner(data []byte) (runnerHandle, error)
	newRepl(opts ReplOptions) (replHandle, error)
	// loadProgress restores a dumped snapshot. The step carries the restored
	// progress handle and its description, like any other step.
	loadProgress(data []byte) step
}

// runnerHandle is a compiled runner.
type runnerHandle interface {
	Close()
	Dump() ([]byte, error)
	TypeCheck(prefix string) error
	// Start begins one execution. It does not consume the runner.
	Start(opts StartOptions) step
}

// replHandle is a REPL session.
type replHandle interface {
	Close()
	Dump() ([]byte, error)
	// FeedStart begins one snippet. It consumes the handle; the session comes
	// back through the returned step or through progressHandle.TakeRepl.
	FeedStart(code string, opts FeedStartOptions) step
}

// progressHandle is an execution paused at a call, name lookup or future.
type progressHandle interface {
	Close()
	Dump() ([]byte, error)
	// TakeRepl returns the REPL session a paused snippet belongs to.
	TakeRepl() (replHandle, error)
	ResumeCall(result Result) step
	// ResumeLookup answers a name lookup; found is false for "undefined".
	ResumeLookup(value Value, found bool) step
	ResumeFutures(results map[uint32]Result) step
}

// step is the outcome of starting or resuming an execution.
type step struct {
	// progress is the next paused state, nil if the backend returned none.
	progress progressHandle
	// desc describes progress; meaningful only when progress is non-nil and err
	// is nil.
	desc progressDesc
	// prints is what the program printed during this step.
	prints string
	// repl is a REPL session handed back with this step (on the error path, or
	// alongside a completed snippet).
	repl replHandle
	err  error
	// retained reports that err came from encoding the call's arguments, before
	// anything reached the interpreter, so the handle was not consumed.
	retained bool
}

type progressKind uint8

const (
	progressFunctionCall progressKind = iota
	progressNameLookup
	progressFuture
	progressComplete
)

// progressDesc is the typed form of a paused or completed execution.
type progressDesc struct {
	kind       progressKind
	scriptName string
	isRepl     bool

	// progressFunctionCall
	functionName string
	args         []Value
	kwargs       Dict
	callID       uint32
	isOSFunction bool
	isMethodCall bool

	// progressNameLookup
	variableName string

	// progressFuture
	pendingCallIDs []uint32

	// progressComplete
	output Value
}

// backendOrDefault returns b, or the native-library backend when b is nil.
func backendOrDefault(b backend) backend {
	if b == nil {
		return ffiBackend{}
	}
	return b
}
