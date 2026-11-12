package monty

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"
)

var (
	// ErrClosed reports that a handle-backed object no longer owns a native handle.
	ErrClosed = errors.New("monty handle is closed")
	// ErrConcurrentUse reports that the same handle-backed object is already being used.
	ErrConcurrentUse = errors.New("monty handle is already in use")
)

// Runner is the compiled runner entrypoint for Monty code.
type Runner struct {
	state *runnerState
}

// Repl is a stateful Monty REPL session.
type Repl struct {
	state *replState
}

// Progress is the low-level pause/resume execution interface.
type Progress interface {
	isProgress()
}

// Snapshot is a paused external function, method call, or OS call.
type Snapshot struct {
	progressBase
	FunctionName string
	Args         []Value
	Kwargs       Dict
	CallID       uint32
	IsOSFunction bool
	IsMethodCall bool
}

// NameLookupSnapshot is a paused unresolved-name lookup.
type NameLookupSnapshot struct {
	progressBase
	VariableName string
}

// FutureSnapshot is a paused future-resolution state.
type FutureSnapshot struct {
	progressBase
	pendingCallIDs []uint32
}

// Complete is a finished execution result.
type Complete struct {
	Output     Value
	ScriptName string
	IsRepl     bool
}

type runnerState struct {
	mu     sync.Mutex
	handle runnerHandle
	// scriptName is set once at construction and never mutated, so it's
	// safe to read without holding mu.
	scriptName string
}

type replState struct {
	mu       sync.Mutex
	handle   replHandle
	inFlight bool
	// scriptName is set once at construction and never mutated, so it's
	// safe to read without holding mu.
	scriptName string
}

type progressState struct {
	mu     sync.Mutex
	handle progressHandle
	owner  *replState
}

type progressBase struct {
	state      *progressState
	scriptName string
	isRepl     bool
}

// New compiles Monty code into a reusable runner.
func New(code string, opts CompileOptions) (*Runner, error) {
	return newRunnerWith(defaultBackend, code, opts)
}

func newRunnerWith(b backend, code string, opts CompileOptions) (*Runner, error) {
	handle, err := b.newRunner(code, newWireCompileOptions(opts))
	if err != nil {
		return nil, err
	}
	return &Runner{
		state: &runnerState{handle: handle, scriptName: opts.ScriptName},
	}, nil
}

// LoadRunner restores a serialized runner.
func LoadRunner(data []byte) (*Runner, error) {
	return loadRunnerWith(defaultBackend, data)
}

func loadRunnerWith(b backend, data []byte) (*Runner, error) {
	data, err := stripDumpHeader(data)
	if err != nil {
		return nil, err
	}
	handle, err := b.loadRunner(data)
	if err != nil {
		return nil, err
	}
	return &Runner{
		state: &runnerState{handle: handle},
	}, nil
}

// LoadSnapshot restores a serialized non-REPL or REPL snapshot without an owner REPL.
func LoadSnapshot(data []byte) (Progress, error) {
	return loadSnapshotWith(defaultBackend, data)
}

func loadSnapshotWith(b backend, data []byte) (Progress, error) {
	data, err := stripDumpHeader(data)
	if err != nil {
		return nil, err
	}
	result := b.loadProgress(data)
	if result.err != nil {
		result.discard()
		return nil, result.err
	}
	if result.progress == nil {
		result.discard()
		return nil, fmt.Errorf("monty backend returned no progress handle")
	}
	return progressFromResult(result.progress, result.desc, nil)
}

// LoadReplSnapshot restores a serialized REPL snapshot and also returns a base
// REPL handle that can be used to abandon the in-flight snippet.
func LoadReplSnapshot(data []byte) (Progress, *Repl, error) {
	return loadReplSnapshotWith(defaultBackend, data)
}

func loadReplSnapshotWith(b backend, data []byte) (Progress, *Repl, error) {
	data, err := stripDumpHeader(data)
	if err != nil {
		return nil, nil, err
	}
	owner := &Repl{state: &replState{}}

	primary := b.loadProgress(data)
	if primary.err != nil {
		primary.discard()
		return nil, nil, primary.err
	}
	if primary.progress == nil {
		primary.discard()
		return nil, nil, fmt.Errorf("monty backend returned no progress handle")
	}

	backup := b.loadProgress(data)
	if backup.err != nil || backup.progress == nil {
		backup.discard()
		primary.progress.Close()
		if backup.err != nil {
			return nil, nil, backup.err
		}
		return nil, nil, fmt.Errorf("monty backend returned no progress handle")
	}

	repl, err := backup.progress.takeRepl()
	backup.progress.Close()
	if err != nil {
		primary.progress.Close()
		return nil, nil, err
	}
	owner.state.restore(repl)

	progress, err := progressFromResult(primary.progress, primary.desc, owner.state)
	if err != nil {
		return nil, nil, err
	}
	return progress, owner, nil
}

// NewRepl constructs an empty REPL session.
func NewRepl(opts ReplOptions) (*Repl, error) {
	return newReplWith(defaultBackend, opts)
}

func newReplWith(b backend, opts ReplOptions) (*Repl, error) {
	handle, err := b.newRepl(newWireReplOptions(opts))
	if err != nil {
		return nil, err
	}
	return &Repl{
		state: &replState{handle: handle, scriptName: opts.ScriptName},
	}, nil
}

// Dump serializes the runner.
func (r *Runner) Dump() ([]byte, error) {
	handle, release, err := r.state.borrow()
	if err != nil {
		return nil, err
	}
	defer release()

	data, err := handle.dump()
	if err != nil {
		return nil, err
	}
	return addDumpHeader(data), nil
}

// TypeCheck runs static type checking on the compiled runner.
func (r *Runner) TypeCheck(prefix string) error {
	handle, release, err := r.state.borrow()
	if err != nil {
		return err
	}
	defer release()

	return handle.typeCheck(prefix)
}

// Start begins low-level runner execution.
func (r *Runner) Start(ctx context.Context, opts StartOptions) (Progress, error) {
	return r.start(ctx, opts, nil, printTelemetry{})
}

func (r *Runner) start(ctx context.Context, opts StartOptions, print PrintCallback, pt printTelemetry) (Progress, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	handle, release, err := r.state.borrow()
	if err != nil {
		return nil, err
	}
	defer release()

	wireOptions, err := newWireStartOptions(opts)
	if err != nil {
		return nil, err
	}

	return consumeOpResult(ctx, handle.start(wireOptions), nil, print, pt)
}

// Run executes the runner through the high-level host callback loop.
func (r *Runner) Run(ctx context.Context, opts RunOptions) (Value, error) {
	start := time.Now()
	execCtx, span := startExecutionSpan(ctx, opts.Telemetry, ExecutionInfo{
		ScriptName: r.state.scriptName,
		IsRepl:     false,
		Inputs: truncatedPayload(opts.TelemetryOptions.RecordArguments, func() string {
			return renderInputs(opts.Inputs)
		}, opts.TelemetryOptions.MaxAttributeBytes),
	}, opts.TelemetryOptions)

	progress, err := r.start(execCtx, StartOptions{
		Inputs: opts.Inputs,
		Limits: opts.Limits,
	}, opts.Print, printTelemetry{handler: opts.Telemetry, opts: opts.TelemetryOptions})
	if err != nil {
		endExecutionSpan(span, Value{}, err, ExecutionTiming{Total: time.Since(start)}, opts.TelemetryOptions)
		return Value{}, err
	}
	value, runErr, timing := dispatchLoop(execCtx, progress, dispatchConfig{
		functions:        opts.Functions,
		os:               opts.OS,
		modules:          opts.Modules,
		print:            opts.Print,
		telemetry:        opts.Telemetry,
		telemetryOptions: opts.TelemetryOptions,
	})
	endExecutionSpan(span, value, runErr, ExecutionTiming{
		Total:    time.Since(start),
		Callback: timing.callback,
		Wait:     timing.wait,
	}, opts.TelemetryOptions)
	return value, runErr
}

// Dump serializes the REPL session.
func (r *Repl) Dump() ([]byte, error) {
	handle, release, err := r.state.borrow()
	if err != nil {
		return nil, err
	}
	defer release()

	data, err := handle.dump()
	if err != nil {
		return nil, err
	}
	return addDumpHeader(data), nil
}

// FeedStart begins low-level REPL snippet execution.
func (r *Repl) FeedStart(ctx context.Context, code string, opts FeedStartOptions) (Progress, error) {
	return r.feedStart(ctx, code, opts, nil, printTelemetry{})
}

func (r *Repl) feedStart(ctx context.Context, code string, opts FeedStartOptions, print PrintCallback, pt printTelemetry) (Progress, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	handle, err := r.state.takeForExecution()
	if err != nil {
		return nil, err
	}

	wireOptions, err := newWireFeedOptions(opts)
	if err != nil {
		// Nothing has run yet, so give the session back.
		r.state.restore(handle)
		return nil, err
	}

	progress, consumeErr := consumeOpResult(ctx, handle.feedStart(code, wireOptions), r.state, print, pt)
	if consumeErr != nil {
		return nil, consumeErr
	}
	return progress, nil
}

// FeedRun executes a REPL snippet through the high-level host callback loop.
func (r *Repl) FeedRun(ctx context.Context, code string, opts FeedOptions) (Value, error) {
	start := time.Now()
	execCtx, span := startExecutionSpan(ctx, opts.Telemetry, ExecutionInfo{
		ScriptName: r.state.scriptName,
		IsRepl:     true,
		Inputs: truncatedPayload(opts.TelemetryOptions.RecordArguments, func() string {
			return renderInputs(opts.Inputs)
		}, opts.TelemetryOptions.MaxAttributeBytes),
	}, opts.TelemetryOptions)

	progress, err := r.feedStart(execCtx, code, FeedStartOptions{Inputs: opts.Inputs}, opts.Print, printTelemetry{handler: opts.Telemetry, opts: opts.TelemetryOptions})
	if err != nil {
		endExecutionSpan(span, Value{}, err, ExecutionTiming{Total: time.Since(start)}, opts.TelemetryOptions)
		return Value{}, err
	}
	value, runErr, timing := dispatchLoop(execCtx, progress, dispatchConfig{
		functions:        opts.Functions,
		os:               opts.OS,
		modules:          opts.Modules,
		print:            opts.Print,
		telemetry:        opts.Telemetry,
		telemetryOptions: opts.TelemetryOptions,
	})
	endExecutionSpan(span, value, runErr, ExecutionTiming{
		Total:    time.Since(start),
		Callback: timing.callback,
		Wait:     timing.wait,
	}, opts.TelemetryOptions)
	return value, runErr
}

// Dump serializes the current snapshot.
func (s *Snapshot) Dump() ([]byte, error) {
	return s.progressBase.dump()
}

// Dump serializes the current name-lookup snapshot.
func (s *NameLookupSnapshot) Dump() ([]byte, error) {
	return s.progressBase.dump()
}

// Dump serializes the current future snapshot.
func (s *FutureSnapshot) Dump() ([]byte, error) {
	return s.progressBase.dump()
}

// ResumeReturn resumes a function or OS snapshot with a return value.
func (s *Snapshot) ResumeReturn(ctx context.Context, value Value) (Progress, error) {
	wireResult, err := newWireCallResult(Return(value))
	if err != nil {
		return nil, err
	}
	return s.progressBase.resumeCall(ctx, wireResult, nil, printTelemetry{})
}

// ResumeException resumes a function or OS snapshot by raising an exception.
func (s *Snapshot) ResumeException(ctx context.Context, exception Exception) (Progress, error) {
	wireResult, err := newWireCallResult(Raise(exception))
	if err != nil {
		return nil, err
	}
	return s.progressBase.resumeCall(ctx, wireResult, nil, printTelemetry{})
}

// ResumePending resumes a function or OS snapshot with a pending future.
func (s *Snapshot) ResumePending(ctx context.Context) (Progress, error) {
	return s.progressBase.resumeCall(ctx, wireCallResult{Kind: wireCallResultPending}, nil, printTelemetry{})
}

// ResumeValue resumes a name lookup with a resolved value.
func (s *NameLookupSnapshot) ResumeValue(ctx context.Context, value Value) (Progress, error) {
	wireResult, err := newWireLookupResult(value)
	if err != nil {
		return nil, err
	}
	return s.progressBase.resumeLookup(ctx, wireResult, nil, printTelemetry{})
}

// ResumeUndefined resumes a name lookup as undefined.
func (s *NameLookupSnapshot) ResumeUndefined(ctx context.Context) (Progress, error) {
	return s.progressBase.resumeLookup(ctx, wireLookupResult{Kind: wireLookupResultUndefined}, nil, printTelemetry{})
}

// PendingCallIDs returns the unresolved call IDs for the future snapshot.
func (s *FutureSnapshot) PendingCallIDs() []uint32 {
	return append([]uint32(nil), s.pendingCallIDs...)
}

// ResumeResults resumes a future snapshot with zero or more resolved call IDs.
func (s *FutureSnapshot) ResumeResults(ctx context.Context, results map[uint32]Result) (Progress, error) {
	wireResults, err := newWireFutureResults(results)
	if err != nil {
		return nil, err
	}
	return s.progressBase.resumeFutures(ctx, wireResults, nil, printTelemetry{})
}

func (s *Snapshot) isProgress()           {}
func (s *NameLookupSnapshot) isProgress() {}
func (s *FutureSnapshot) isProgress()     {}
func (s *Complete) isProgress()           {}

func (s *Snapshot) restoreOwner() error {
	return s.progressBase.restoreOwner()
}

func (s *NameLookupSnapshot) restoreOwner() error {
	return s.progressBase.restoreOwner()
}

func (s *FutureSnapshot) restoreOwner() error {
	return s.progressBase.restoreOwner()
}

func (b *progressBase) dump() ([]byte, error) {
	handle, release, err := b.state.borrow()
	if err != nil {
		return nil, err
	}
	defer release()

	data, err := handle.dump()
	if err != nil {
		return nil, err
	}
	return addDumpHeader(data), nil
}

func (b *progressBase) resumeCall(ctx context.Context, result wireCallResult, print PrintCallback, pt printTelemetry) (Progress, error) {
	if err := ctx.Err(); err != nil {
		if restoreErr := b.restoreOwner(); restoreErr != nil {
			return nil, errors.Join(err, restoreErr)
		}
		return nil, err
	}

	handle, owner, err := b.state.take()
	if err != nil {
		return nil, err
	}
	defer handle.Close()

	return consumeOpResult(ctx, handle.resumeCall(result), owner, print, pt)
}

func (b *progressBase) resumeLookup(ctx context.Context, result wireLookupResult, print PrintCallback, pt printTelemetry) (Progress, error) {
	if err := ctx.Err(); err != nil {
		if restoreErr := b.restoreOwner(); restoreErr != nil {
			return nil, errors.Join(err, restoreErr)
		}
		return nil, err
	}

	handle, owner, err := b.state.take()
	if err != nil {
		return nil, err
	}
	defer handle.Close()

	return consumeOpResult(ctx, handle.resumeLookup(result), owner, print, pt)
}

func (b *progressBase) resumeFutures(ctx context.Context, results wireFutureResults, print PrintCallback, pt printTelemetry) (Progress, error) {
	if err := ctx.Err(); err != nil {
		if restoreErr := b.restoreOwner(); restoreErr != nil {
			return nil, errors.Join(err, restoreErr)
		}
		return nil, err
	}

	handle, owner, err := b.state.take()
	if err != nil {
		return nil, err
	}
	defer handle.Close()

	return consumeOpResult(ctx, handle.resumeFutures(results), owner, print, pt)
}

func (b *progressBase) restoreOwner() error {
	handle, owner, err := b.state.take()
	if err != nil {
		return err
	}
	defer handle.Close()

	if owner == nil {
		return nil
	}
	repl, err := handle.takeRepl()
	if err != nil {
		owner.clear()
		return err
	}
	owner.restore(repl)
	return nil
}

func (s *runnerState) borrow() (runnerHandle, func(), error) {
	if s == nil || !s.mu.TryLock() {
		return nil, nil, ErrConcurrentUse
	}
	if s.handle == nil {
		s.mu.Unlock()
		return nil, nil, ErrClosed
	}
	return s.handle, s.mu.Unlock, nil
}

func (s *replState) borrow() (replHandle, func(), error) {
	if s == nil || !s.mu.TryLock() {
		return nil, nil, ErrConcurrentUse
	}
	if s.handle == nil {
		s.mu.Unlock()
		if s.inFlight {
			return nil, nil, ErrConcurrentUse
		}
		return nil, nil, ErrClosed
	}
	return s.handle, s.mu.Unlock, nil
}

func (s *replState) takeForExecution() (replHandle, error) {
	if s == nil || !s.mu.TryLock() {
		return nil, ErrConcurrentUse
	}
	defer s.mu.Unlock()

	if s.handle == nil {
		if s.inFlight {
			return nil, ErrConcurrentUse
		}
		return nil, ErrClosed
	}
	handle := s.handle
	s.handle = nil
	s.inFlight = true
	return handle, nil
}

func (s *replState) restore(handle replHandle) {
	s.mu.Lock()
	old := s.handle
	s.handle = handle
	s.inFlight = false
	s.mu.Unlock()
	if old != nil {
		old.Close()
	}
}

func (s *replState) clear() {
	s.mu.Lock()
	old := s.handle
	s.handle = nil
	s.inFlight = false
	s.mu.Unlock()
	if old != nil {
		old.Close()
	}
}

func (s *progressState) borrow() (progressHandle, func(), error) {
	if s == nil || !s.mu.TryLock() {
		return nil, nil, ErrConcurrentUse
	}
	if s.handle == nil {
		s.mu.Unlock()
		return nil, nil, ErrClosed
	}
	return s.handle, s.mu.Unlock, nil
}

func (s *progressState) take() (progressHandle, *replState, error) {
	if s == nil || !s.mu.TryLock() {
		return nil, nil, ErrConcurrentUse
	}
	defer s.mu.Unlock()

	if s.handle == nil {
		return nil, nil, ErrClosed
	}
	handle := s.handle
	s.handle = nil
	return handle, s.owner, nil
}

func consumeOpResult(ctx context.Context, result opResult, owner *replState, print PrintCallback, pt printTelemetry) (Progress, error) {
	if result.prints != "" {
		if print != nil {
			print("stdout", result.prints)
		}
		recordPrint(ctx, pt, result.prints)
	}

	if result.err != nil {
		if owner != nil {
			if result.repl != nil {
				owner.restore(result.repl)
			} else {
				owner.clear()
			}
		} else if result.repl != nil {
			result.repl.Close()
		}
		if result.progress != nil {
			result.progress.Close()
		}
		return nil, result.err
	}

	if result.progress == nil {
		if owner != nil {
			owner.clear()
		}
		if result.repl != nil {
			result.repl.Close()
		}
		return nil, fmt.Errorf("monty backend returned no progress handle")
	}

	return progressFromResult(result.progress, result.desc, owner)
}

func progressFromResult(handle progressHandle, desc *wireProgressPayload, owner *replState) (Progress, error) {
	if desc == nil {
		handle.Close()
		if owner != nil {
			owner.clear()
		}
		return nil, fmt.Errorf("monty backend returned no progress payload")
	}

	state := &progressState{
		handle: handle,
		owner:  owner,
	}
	base := progressBase{
		state:      state,
		scriptName: desc.ScriptName,
		isRepl:     desc.IsRepl,
	}

	switch desc.Variant {
	case wireProgressFunctionCall:
		args, err := valuesFromWire(desc.Args)
		if err != nil {
			handle.Close()
			if owner != nil {
				owner.clear()
			}
			return nil, err
		}
		kwargs, err := dictFromWirePairs(desc.Kwargs)
		if err != nil {
			handle.Close()
			if owner != nil {
				owner.clear()
			}
			return nil, err
		}
		return &Snapshot{
			progressBase: base,
			FunctionName: desc.FunctionName,
			Args:         args,
			Kwargs:       kwargs,
			CallID:       desc.CallID,
			IsOSFunction: desc.IsOSFunction,
			IsMethodCall: desc.IsMethodCall,
		}, nil
	case wireProgressNameLookup:
		return &NameLookupSnapshot{
			progressBase: base,
			VariableName: desc.VariableName,
		}, nil
	case wireProgressFuture:
		return &FutureSnapshot{
			progressBase:   base,
			pendingCallIDs: append([]uint32(nil), desc.PendingCallIDs...),
		}, nil
	case wireProgressComplete:
		if desc.Output == nil {
			handle.Close()
			if owner != nil {
				owner.clear()
			}
			return nil, fmt.Errorf("complete progress payload is missing output")
		}
		output, err := desc.Output.toPublic()
		if err != nil {
			handle.Close()
			if owner != nil {
				owner.clear()
			}
			return nil, err
		}
		if owner != nil {
			repl, err := handle.takeRepl()
			handle.Close()
			state.handle = nil
			if err != nil {
				owner.clear()
				return nil, err
			}
			owner.restore(repl)
		} else {
			handle.Close()
			state.handle = nil
		}
		return &Complete{
			Output:     output,
			ScriptName: desc.ScriptName,
			IsRepl:     desc.IsRepl,
		}, nil
	default:
		handle.Close()
		if owner != nil {
			owner.clear()
		}
		return nil, fmt.Errorf("unknown progress variant %q", desc.Variant)
	}
}

func optionalString(value string) *string {
	if value == "" {
		return nil
	}
	return &value
}

func optionalInt(value int) *int {
	if value == 0 {
		return nil
	}
	return &value
}
