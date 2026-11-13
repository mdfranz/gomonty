package monty

import "context"

// Backend is the seam between the host-facing API (Runner, Repl, the progress
// snapshots and the dispatch loop) and whatever actually runs the interpreter.
// The default backend (backend_ffi.go) drives the native library in-process;
// another backend can drive a remote worker.
//
// The interfaces work in typed Go values, not in a backend's wire format, so
// each backend owns its own encoding.
//
// EXPERIMENTAL: this API exists so that backends can live in other modules,
// such as the remote worker client. It may change before it is declared
// stable. Set CompileOptions.Backend or ReplOptions.Backend to use one; the
// zero value means the native library.
//
// Calls that can run for a while take the caller's context: Start, FeedStart
// and the resumes. A backend that can stop the work should return, with the
// context's error, once it is done. The native-library backend cannot interrupt
// a running script and relies on resource limits instead. A remote backend
// cannot tell a stalled worker from a slow one, so cancelling the context ends
// its session.
//
// Ownership rules every backend must follow, because the host-facing layer
// (runnerState, replState, progressState) relies on them:
//
//   - Close is idempotent, and safe after a resume has consumed the handle.
//   - Resume and feed calls consume the handle they are called on, except when
//     the returned Step has Retained set (the arguments could not be encoded
//     before anything was sent), in which case the handle is still usable.
type Backend interface {
	NewRunner(code string, opts CompileOptions) (RunnerHandle, error)
	LoadRunner(data []byte) (RunnerHandle, error)
	NewRepl(opts ReplOptions) (ReplHandle, error)
	// LoadProgress restores a dumped snapshot. The Step carries the restored
	// progress handle and its description, like any other Step.
	LoadProgress(data []byte) Step
}

// RunnerHandle is a compiled runner.
type RunnerHandle interface {
	Close()
	Dump() ([]byte, error)
	TypeCheck(prefix string) error
	// Start begins one execution. It does not consume the runner.
	Start(ctx context.Context, opts StartOptions) Step
}

// ReplHandle is a REPL session.
type ReplHandle interface {
	Close()
	Dump() ([]byte, error)
	// FeedStart begins one snippet. It consumes the handle; the session comes
	// back through the returned Step or through ProgressHandle.TakeRepl.
	FeedStart(ctx context.Context, code string, opts FeedStartOptions) Step
}

// ProgressHandle is an execution paused at a call, name lookup or future.
type ProgressHandle interface {
	Close()
	Dump() ([]byte, error)
	// TakeRepl returns the REPL session a paused snippet belongs to.
	TakeRepl() (ReplHandle, error)
	ResumeCall(ctx context.Context, result Result) Step
	// ResumeLookup answers a name lookup; found is false for "undefined".
	ResumeLookup(ctx context.Context, value Value, found bool) Step
	ResumeFutures(ctx context.Context, results map[uint32]Result) Step
}

// PrintSegment is one run of print output on a single stream, "stdout" or
// "stderr".
type PrintSegment struct {
	Stream string
	Text   string
}

// Step is the outcome of starting or resuming an execution.
type Step struct {
	// Progress is the next paused state, nil if the backend returned none.
	Progress ProgressHandle
	// Desc describes Progress; meaningful only when Progress is non-nil and Err
	// is nil.
	Desc ProgressDesc
	// Prints is what the program printed during this step, all streams joined.
	Prints string
	// PrintSegments is the same output split into runs by stream, in order.
	// A backend that can tell stdout from stderr sets it; the host layer then
	// reports each run to PrintCallback under its own stream. Prints must
	// still hold the joined text. Empty means all of Prints is stdout.
	PrintSegments []PrintSegment
	// Repl is a REPL session handed back with this step (on the error path, or
	// alongside a completed snippet).
	Repl ReplHandle
	Err  error
	// Retained reports that Err came from encoding the call's arguments, before
	// anything reached the interpreter, so the handle was not consumed.
	Retained bool
}

// ProgressKind says which pause, or completion, a ProgressDesc describes.
type ProgressKind uint8

const (
	ProgressFunctionCall ProgressKind = iota
	ProgressNameLookup
	ProgressFuture
	ProgressComplete
)

// ProgressDesc is the typed form of a paused or completed execution.
type ProgressDesc struct {
	Kind       ProgressKind
	ScriptName string
	IsRepl     bool

	// ProgressFunctionCall
	FunctionName string
	Args         []Value
	Kwargs       Dict
	CallID       uint32
	IsOSFunction bool
	IsMethodCall bool

	// ProgressNameLookup
	VariableName string

	// ProgressFuture
	PendingCallIDs []uint32

	// ProgressComplete
	Output Value
}

// backendOrDefault returns b, or the native-library backend when b is nil.
func backendOrDefault(b Backend) Backend {
	if b == nil {
		return ffiBackend{}
	}
	return b
}
