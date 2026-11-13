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
	handle RunnerHandle
	// scriptName is set once at construction and never mutated, so it's
	// safe to read without holding mu.
	scriptName string
}

type replState struct {
	mu       sync.Mutex
	handle   ReplHandle
	inFlight bool
	// scriptName is set once at construction and never mutated, so it's
	// safe to read without holding mu.
	scriptName string
}

type progressState struct {
	mu     sync.Mutex
	handle ProgressHandle
	owner  *replState
}

type progressBase struct {
	state      *progressState
	scriptName string
	isRepl     bool
}

// New compiles Monty code into a reusable runner.
func New(code string, opts CompileOptions) (*Runner, error) {
	handle, err := backendOrDefault(opts.Backend).NewRunner(code, opts)
	if err != nil {
		return nil, err
	}
	return &Runner{
		state: &runnerState{handle: handle, scriptName: opts.ScriptName},
	}, nil
}

// LoadRunner restores a serialized runner.
func LoadRunner(data []byte) (*Runner, error) {
	return loadRunner(nil, data)
}

func loadRunner(b Backend, data []byte) (*Runner, error) {
	data, err := stripDumpHeader(data)
	if err != nil {
		return nil, err
	}
	handle, err := backendOrDefault(b).LoadRunner(data)
	if err != nil {
		return nil, err
	}
	return &Runner{
		state: &runnerState{handle: handle},
	}, nil
}

// LoadSnapshot restores a serialized non-REPL or REPL snapshot without an owner REPL.
func LoadSnapshot(data []byte) (Progress, error) {
	return loadSnapshot(nil, data)
}

func loadSnapshot(b Backend, data []byte) (Progress, error) {
	data, err := stripDumpHeader(data)
	if err != nil {
		return nil, err
	}
	st := backendOrDefault(b).LoadProgress(data)
	if st.Err != nil {
		closeStep(st)
		return nil, st.Err
	}
	return progressFromStep(st, nil)
}

// LoadReplSnapshot restores a serialized REPL snapshot and also returns a base
// REPL handle that can be used to abandon the in-flight snippet.
func LoadReplSnapshot(data []byte) (Progress, *Repl, error) {
	return loadReplSnapshot(nil, data)
}

func loadReplSnapshot(b Backend, data []byte) (Progress, *Repl, error) {
	data, err := stripDumpHeader(data)
	if err != nil {
		return nil, nil, err
	}
	b = backendOrDefault(b)
	owner := &Repl{state: &replState{}}

	primary := b.LoadProgress(data)
	if primary.Err != nil {
		closeStep(primary)
		return nil, nil, primary.Err
	}

	backup := b.LoadProgress(data)
	if backup.Err != nil {
		closeStep(backup)
		closeStep(primary)
		return nil, nil, backup.Err
	}

	repl, err := backup.Progress.TakeRepl()
	backup.Progress.Close()
	if err != nil {
		closeStep(primary)
		return nil, nil, err
	}
	owner.state.restore(repl)

	progress, err := progressFromStep(primary, owner.state)
	if err != nil {
		return nil, nil, err
	}
	return progress, owner, nil
}

// NewRepl constructs an empty REPL session.
func NewRepl(opts ReplOptions) (*Repl, error) {
	handle, err := backendOrDefault(opts.Backend).NewRepl(opts)
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

	data, err := handle.Dump()
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

	return handle.TypeCheck(prefix)
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

	return consumeStep(ctx, handle.Start(printSinkContext(ctx, print), opts), nil, print, pt)
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

	data, err := handle.Dump()
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

	st := handle.FeedStart(printSinkContext(ctx, print), code, opts)
	if st.Retained {
		// The options could not be encoded, so the session was not consumed.
		r.state.restore(handle)
		return nil, st.Err
	}
	handle.Close()
	return consumeStep(ctx, st, r.state, print, pt)
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
	return s.progressBase.resumeCall(ctx, Return(value), nil, printTelemetry{})
}

// ResumeException resumes a function or OS snapshot by raising an exception.
func (s *Snapshot) ResumeException(ctx context.Context, exception Exception) (Progress, error) {
	return s.progressBase.resumeCall(ctx, Raise(exception), nil, printTelemetry{})
}

// ResumePending resumes a function or OS snapshot with a pending future.
func (s *Snapshot) ResumePending(ctx context.Context) (Progress, error) {
	return s.progressBase.resumeCall(ctx, Result{kind: resultKindPending}, nil, printTelemetry{})
}

// ResumeValue resumes a name lookup with a resolved value.
func (s *NameLookupSnapshot) ResumeValue(ctx context.Context, value Value) (Progress, error) {
	return s.progressBase.resumeLookup(ctx, value, true, nil, printTelemetry{})
}

// ResumeUndefined resumes a name lookup as undefined.
func (s *NameLookupSnapshot) ResumeUndefined(ctx context.Context) (Progress, error) {
	return s.progressBase.resumeLookup(ctx, Value{}, false, nil, printTelemetry{})
}

// PendingCallIDs returns the unresolved call IDs for the future snapshot.
func (s *FutureSnapshot) PendingCallIDs() []uint32 {
	return append([]uint32(nil), s.pendingCallIDs...)
}

// ResumeResults resumes a future snapshot with zero or more resolved call IDs.
func (s *FutureSnapshot) ResumeResults(ctx context.Context, results map[uint32]Result) (Progress, error) {
	return s.progressBase.resumeFutures(ctx, results, nil, printTelemetry{})
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

	data, err := handle.Dump()
	if err != nil {
		return nil, err
	}
	return addDumpHeader(data), nil
}

func (b *progressBase) resumeCall(ctx context.Context, result Result, print PrintCallback, pt printTelemetry) (Progress, error) {
	return b.resume(ctx, print, pt, func(h ProgressHandle) Step { return h.ResumeCall(printSinkContext(ctx, print), result) })
}

func (b *progressBase) resumeLookup(ctx context.Context, value Value, found bool, print PrintCallback, pt printTelemetry) (Progress, error) {
	return b.resume(ctx, print, pt, func(h ProgressHandle) Step { return h.ResumeLookup(printSinkContext(ctx, print), value, found) })
}

func (b *progressBase) resumeFutures(ctx context.Context, results map[uint32]Result, print PrintCallback, pt printTelemetry) (Progress, error) {
	return b.resume(ctx, print, pt, func(h ProgressHandle) Step { return h.ResumeFutures(printSinkContext(ctx, print), results) })
}

// resume takes the paused handle, resumes it through call and consumes the
// resulting Step. A Step that retained the handle (the answer could not be
// encoded) puts it back, so the snapshot stays usable.
func (b *progressBase) resume(ctx context.Context, print PrintCallback, pt printTelemetry, call func(ProgressHandle) Step) (Progress, error) {
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

	st := call(handle)
	if st.Retained {
		b.state.putBack(handle)
		return nil, st.Err
	}
	handle.Close()
	return consumeStep(ctx, st, owner, print, pt)
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
	repl, err := handle.TakeRepl()
	if err != nil {
		owner.clear()
		return err
	}
	owner.restore(repl)
	return nil
}

func (s *runnerState) borrow() (RunnerHandle, func(), error) {
	if s == nil || !s.mu.TryLock() {
		return nil, nil, ErrConcurrentUse
	}
	if s.handle == nil {
		s.mu.Unlock()
		return nil, nil, ErrClosed
	}
	return s.handle, s.mu.Unlock, nil
}

func (s *replState) borrow() (ReplHandle, func(), error) {
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

func (s *replState) takeForExecution() (ReplHandle, error) {
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

func (s *replState) restore(handle ReplHandle) {
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

func (s *progressState) borrow() (ProgressHandle, func(), error) {
	if s == nil || !s.mu.TryLock() {
		return nil, nil, ErrConcurrentUse
	}
	if s.handle == nil {
		s.mu.Unlock()
		return nil, nil, ErrClosed
	}
	return s.handle, s.mu.Unlock, nil
}

func (s *progressState) take() (ProgressHandle, *replState, error) {
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

// putBack returns a handle that take removed but a resume did not consume.
func (s *progressState) putBack(handle ProgressHandle) {
	s.mu.Lock()
	s.handle = handle
	s.mu.Unlock()
}

// closeStep releases whatever handles a Step carries.
func closeStep(st Step) {
	if st.Progress != nil {
		st.Progress.Close()
	}
	if st.Repl != nil {
		st.Repl.Close()
	}
}

func consumeStep(ctx context.Context, st Step, owner *replState, print PrintCallback, pt printTelemetry) (Progress, error) {
	if st.Prints != "" {
		if print != nil && !st.PrintsDelivered {
			if len(st.PrintSegments) == 0 {
				print("stdout", st.Prints)
			}
			for _, segment := range st.PrintSegments {
				print(segment.Stream, segment.Text)
			}
		}
		recordPrint(ctx, pt, st.Prints)
	}

	if st.Err != nil {
		if owner != nil {
			if st.Repl != nil {
				owner.restore(st.Repl)
			} else {
				owner.clear()
			}
		} else if st.Repl != nil {
			st.Repl.Close()
		}
		if st.Progress != nil {
			st.Progress.Close()
		}
		return nil, st.Err
	}

	if st.Progress == nil {
		if owner != nil {
			owner.clear()
		}
		if st.Repl != nil {
			st.Repl.Close()
		}
		return nil, fmt.Errorf("monty backend returned no progress handle")
	}

	return progressFromStep(st, owner)
}

func progressFromStep(st Step, owner *replState) (Progress, error) {
	handle := st.Progress
	if handle == nil {
		if owner != nil {
			owner.clear()
		}
		return nil, fmt.Errorf("monty backend returned no progress handle")
	}
	desc := st.Desc

	state := &progressState{
		handle: handle,
		owner:  owner,
	}
	base := progressBase{
		state:      state,
		scriptName: desc.ScriptName,
		isRepl:     desc.IsRepl,
	}

	switch desc.Kind {
	case ProgressFunctionCall:
		return &Snapshot{
			progressBase: base,
			FunctionName: desc.FunctionName,
			Args:         desc.Args,
			Kwargs:       desc.Kwargs,
			CallID:       desc.CallID,
			IsOSFunction: desc.IsOSFunction,
			IsMethodCall: desc.IsMethodCall,
		}, nil
	case ProgressNameLookup:
		return &NameLookupSnapshot{
			progressBase: base,
			VariableName: desc.VariableName,
		}, nil
	case ProgressFuture:
		return &FutureSnapshot{
			progressBase:   base,
			pendingCallIDs: desc.PendingCallIDs,
		}, nil
	case ProgressComplete:
		if owner != nil {
			repl, err := handle.TakeRepl()
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
			Output:     desc.Output,
			ScriptName: desc.ScriptName,
			IsRepl:     desc.IsRepl,
		}, nil
	default:
		handle.Close()
		if owner != nil {
			owner.clear()
		}
		return nil, fmt.Errorf("unknown progress kind %d", desc.Kind)
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
