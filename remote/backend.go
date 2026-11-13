package remote

import (
	"context"
	"errors"
	"fmt"
	"runtime"
	"sync/atomic"
	"time"

	monty "github.com/mdfranz/gomonty"
	montyv1 "github.com/mdfranz/gomonty/remote/internal/montyv1"
	"google.golang.org/protobuf/proto"
)

// Backend runs REPL sessions on a remote Monty worker. Pass it as
// monty.ReplOptions.Backend:
//
//	repl, err := monty.NewRepl(monty.ReplOptions{Backend: remote.NewBackend(transport)})
//
// Each REPL is one session on its own stream. A script can call the host's
// functions, read names from it, make OS calls (answered by the OSHandler, so
// vfs filesystems work unchanged) and wait on its futures, exactly as it can
// locally. Blocking sleeps are served by the backend itself.
//
// A REPL can be dumped and restored with monty.LoadRepl, and reset in place; see
// replHandle.Dump for what a snapshot is good for. Not supported yet: runners,
// and snapshots of a snippet that is paused (a dump is only of an idle REPL);
// methods and attributes
// of host objects, which stop the script with ErrUnsupported; async host calls
// started without waiting for the host, and asyncio sleeps. OS calls with no
// gomonty equivalent (appending to files, open and exclusive creation) are
// answered "not handled", so the script gets an ordinary Python error.
//
// A call's context bounds it: if the context ends while a feed is waiting on the
// worker, the call returns the context's error and the session is closed,
// because there is no way to stop one turn and keep the session. NewRepl takes
// no context, so opening a session is bounded by WithOpenTimeout instead.
type Backend struct {
	transport   Transport
	openTimeout time.Duration
}

var _ monty.Backend = (*Backend)(nil)

// defaultOpenTimeout bounds connecting and configuring a session.
const defaultOpenTimeout = 30 * time.Second

// Option configures a Backend.
type Option func(*Backend)

// WithOpenTimeout bounds how long NewRepl may take to open and configure a
// session. The default is 30 seconds; zero or a negative value means no limit.
func WithOpenTimeout(d time.Duration) Option {
	return func(b *Backend) { b.openTimeout = d }
}

// NewBackend returns a Backend that opens its sessions over t.
func NewBackend(t Transport, opts ...Option) *Backend {
	b := &Backend{transport: t, openTimeout: defaultOpenTimeout}
	for _, opt := range opts {
		opt(b)
	}
	return b
}

func (b *Backend) NewRunner(string, monty.CompileOptions) (monty.RunnerHandle, error) {
	return nil, fmt.Errorf("%w: runners (use a REPL)", ErrUnsupported)
}

func (b *Backend) LoadRunner([]byte) (monty.RunnerHandle, error) {
	return nil, fmt.Errorf("%w: loading a runner", ErrUnsupported)
}

func (b *Backend) LoadProgress([]byte) monty.Step {
	return monty.Step{Err: fmt.Errorf("%w: loading a snapshot", ErrUnsupported)}
}

// NewRepl opens a session and configures it. It fails if the worker cannot be
// reached or refuses the session.
func (b *Backend) NewRepl(opts monty.ReplOptions) (monty.ReplHandle, error) {
	return b.open(opts, nil)
}

var _ monty.ReplLoader = (*Backend)(nil)

// LoadRepl restores an idle REPL from the bytes of a remote handle's Dump. The
// worker decides whether it accepts them: a snapshot is opaque, and only valid
// on a worker running a compatible Monty. A dump written by the native library
// is refused here.
func (b *Backend) LoadRepl(data []byte, opts monty.ReplOptions) (monty.ReplHandle, error) {
	state, err := openDump(data)
	if err != nil {
		return nil, err
	}
	return b.open(opts, &montyv1.Load{State: state})
}

// open starts a session. With load set, the session is restored from it instead
// of starting empty.
func (b *Backend) open(opts monty.ReplOptions, load *montyv1.Load) (monty.ReplHandle, error) {
	// The session lives as long as this context. The open timeout cancels it
	// only until the session is configured.
	ctx, cancel := context.WithCancel(context.Background())
	var timedOut atomic.Bool
	var timer *time.Timer
	if b.openTimeout > 0 {
		timer = time.AfterFunc(b.openTimeout, func() {
			timedOut.Store(true)
			cancel()
		})
	}
	openErr := func(err error) error {
		if timedOut.Load() {
			return fmt.Errorf("remote: opening a session timed out after %v", b.openTimeout)
		}
		return err
	}

	stream, err := b.transport.Open(ctx)
	if err != nil {
		if timer != nil {
			timer.Stop()
		}
		cancel()
		return nil, openErr(err)
	}
	sess := newSession(stream, cancel)

	configure := &montyv1.Configure{
		ScriptName:      opts.ScriptName,
		Limits:          limitsToProto(opts.Limits),
		MontyVersion:    "gomonty",
		ProtocolVersion: protocolVersion,
	}
	sess.configure = configure
	request := proto.Clone(configure).(*montyv1.Configure)
	if load != nil {
		request.First = &montyv1.Configure_Load{Load: load}
	}
	t, err := sess.call(context.Background(), &montyv1.ClientRequest{Kind: &montyv1.ClientRequest_Configure{Configure: request}})
	if err != nil {
		if timer != nil {
			timer.Stop()
		}
		sess.close()
		return nil, openErr(err)
	}
	if timer != nil && !timer.Stop() {
		// The timeout fired just as the reply arrived, which cancelled the
		// session's context.
		sess.close()
		return nil, openErr(errors.New("remote: the session was cancelled while opening"))
	}
	switch k := t.end.GetKind().(type) {
	case *montyv1.WorkerEvent_Ok:
		name := opts.ScriptName
		if restored := t.end.GetRestoredScriptName(); restored != "" {
			name = restored
		}
		return newReplHandle(sess, name), nil
	case *montyv1.WorkerEvent_SessionRefused:
		sess.close()
		return nil, &SessionRefusedError{Reason: k.SessionRefused.GetMessage()}
	case *montyv1.WorkerEvent_Error:
		// A refused Load: the worker could not restore the bytes it was given.
		sess.close()
		return nil, pythonError(k.Error.GetException())
	default:
		sess.close()
		if load != nil {
			return nil, fmt.Errorf("%w: the dump is of a snippet that was paused, not of an idle REPL", ErrUnsupported)
		}
		return nil, fmt.Errorf("remote: unexpected reply to Configure: %T", k)
	}
}

// limitsToProto maps resource limits. MaxDuration is a per-run limit locally,
// which for a REPL is one feed, so it becomes the per-feed budget. A zero
// field is left unset.
func limitsToProto(l *monty.ResourceLimits) *montyv1.ResourceLimits {
	if l == nil {
		return nil
	}
	out := &montyv1.ResourceLimits{}
	set := func(dst **uint64, v int64) {
		if v > 0 {
			u := uint64(v)
			*dst = &u
		}
	}
	set(&out.MaxFeedDurationMicros, l.MaxDuration.Microseconds())
	set(&out.MaxMemoryBytes, int64(l.MaxMemory))
	set(&out.GcInterval, int64(l.GCInterval))
	set(&out.MaxRecursionDepth, int64(l.MaxRecursionDepth))
	set(&out.MaxSuspensions, int64(l.MaxSuspensions))
	return out
}

// replHandle is a REPL session. A REPL has no public Close, so like the local
// handles it is released by a finalizer when it becomes unreachable; closing
// the Transport ends every session at once. FeedStart consumes the handle it is called on
// and returns the session in a new one, which is how the local backend works
// too: the host layer closes the consumed handle afterwards, and that must not
// end a session that is still in use.
type replHandle struct {
	sess       *session
	scriptName string
	consumed   atomic.Bool
}

func newReplHandle(sess *session, scriptName string) *replHandle {
	h := &replHandle{sess: sess, scriptName: scriptName}
	// A consumed handle's Close does nothing, so only the handle that carries
	// the live session ends it.
	runtime.SetFinalizer(h, (*replHandle).Close)
	return h
}

func (h *replHandle) Close() {
	if h.consumed.Load() {
		return
	}
	h.sess.close()
}

// dumpTimeout bounds a Dump or a Reset, which take no context of their own.
const dumpTimeout = 30 * time.Second

// Dump asks the worker for a snapshot of the idle session and returns it in an
// envelope that only a remote backend will load. The session stays usable.
//
// The bytes are the worker's own and opaque: they are only valid on a worker
// with a compatible Monty, and a worker without session storage refuses with a
// PythonError.
func (h *replHandle) Dump() ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), dumpTimeout)
	defer cancel()
	t, err := h.sess.call(ctx, &montyv1.ClientRequest{Kind: &montyv1.ClientRequest_Dump{Dump: &montyv1.Dump{}}})
	if err != nil {
		return nil, err
	}
	switch k := t.end.GetKind().(type) {
	case *montyv1.WorkerEvent_DumpResult:
		return sealDump(k.DumpResult.GetState()), nil
	case *montyv1.WorkerEvent_Error:
		return nil, pythonError(k.Error.GetException())
	default:
		h.sess.fail(fmt.Errorf("remote: unexpected reply to Dump: %T", k))
		return nil, h.sess.dead
	}
}

var _ monty.ReplResetter = (*replHandle)(nil)

// Reset drops the session's state on the worker and configures a new, empty
// session on the same stream. The old handle is spent; the returned one carries
// the session. If either step fails the session is closed.
func (h *replHandle) Reset(ctx context.Context) (monty.ReplHandle, error) {
	ctx, cancel := context.WithTimeout(ctx, dumpTimeout)
	defer cancel()
	t, err := h.sess.call(ctx, &montyv1.ClientRequest{Kind: &montyv1.ClientRequest_Reset_{Reset_: &montyv1.Reset{}}})
	if err != nil {
		return nil, err
	}
	if _, ok := t.end.GetKind().(*montyv1.WorkerEvent_Ok); !ok {
		h.sess.fail(fmt.Errorf("remote: unexpected reply to Reset: %T", t.end.GetKind()))
		return nil, h.sess.dead
	}
	t, err = h.sess.call(ctx, &montyv1.ClientRequest{Kind: &montyv1.ClientRequest_Configure{
		Configure: proto.Clone(h.sess.configure).(*montyv1.Configure),
	}})
	if err != nil {
		return nil, err
	}
	switch k := t.end.GetKind().(type) {
	case *montyv1.WorkerEvent_Ok:
	case *montyv1.WorkerEvent_SessionRefused:
		h.sess.close()
		return nil, &SessionRefusedError{Reason: k.SessionRefused.GetMessage()}
	default:
		h.sess.fail(fmt.Errorf("remote: unexpected reply to Configure after Reset: %T", k))
		return nil, h.sess.dead
	}
	h.consumed.Store(true)
	return newReplHandle(h.sess, h.scriptName), nil
}

func (h *replHandle) FeedStart(ctx context.Context, code string, opts monty.FeedStartOptions) monty.Step {
	refs, arena, err := encodeInputs(opts.Inputs)
	if err != nil {
		// Nothing was sent, so the handle is still good.
		return monty.Step{Err: err, Retained: true}
	}
	feed := &montyv1.Feed{Code: code, Inputs: refs, Values: arena}
	t, err := h.sess.call(ctx, &montyv1.ClientRequest{Kind: &montyv1.ClientRequest_Feed{Feed: feed}})

	h.consumed.Store(true)
	if err != nil {
		// The session is gone, so there is nothing to hand back.
		return monty.Step{Err: err}
	}
	return newReplHandle(h.sess, h.scriptName).stepFor(ctx, t)
}

// stepFor turns the event that ended a feed into a Step. The session is ready
// for another feed after every outcome except a fatal one, which call has
// already handled.
func (h *replHandle) stepFor(ctx context.Context, t turn) monty.Step {
	switch k := t.end.GetKind().(type) {
	case *montyv1.WorkerEvent_Complete:
		value, err := decodeValue(k.Complete.GetValues(), k.Complete.GetValue())
		if err != nil {
			return t.prints.step(monty.Step{Err: err, Repl: h})
		}
		return t.prints.step(monty.Step{
			Progress: &completedProgress{repl: h},
			Desc: monty.ProgressDesc{
				Kind:       monty.ProgressComplete,
				ScriptName: h.scriptName,
				IsRepl:     true,
				Output:     value,
			},
		})
	case *montyv1.WorkerEvent_Error:
		return t.prints.step(monty.Step{Err: pythonError(k.Error.GetException()), Repl: h})
	case *montyv1.WorkerEvent_TypingError:
		return t.prints.step(monty.Step{Err: &TypingError{Diagnostics: k.TypingError.GetDiagnostics()}, Repl: h})
	case *montyv1.WorkerEvent_FunctionCall:
		return h.pauseOnCall(ctx, k.FunctionCall, t.prints)
	case *montyv1.WorkerEvent_OsCall:
		return h.pauseOnOS(ctx, k.OsCall, t.prints)
	case *montyv1.WorkerEvent_NameLookup:
		return h.pauseOnName(ctx, k.NameLookup, t.prints)
	case *montyv1.WorkerEvent_ResolveFutures:
		return h.pauseOnFutures(k.ResolveFutures, t.prints)
	case *montyv1.WorkerEvent_StreamedCalls:
		// Only sent to a client that asked for them, which this one does not.
		return h.abortFeed(ctx, "a streamed host call", t.prints)
	default:
		h.sess.fail(fmt.Errorf("remote: unexpected event ending a feed: %T", k))
		return t.prints.step(monty.Step{Err: h.sess.dead})
	}
}

// proceed sends the host's answer to a pause and turns the worker's next event
// into a Step.
func (h *replHandle) proceed(ctx context.Context, req *montyv1.ClientRequest) monty.Step {
	t, err := h.sess.call(ctx, req)
	if err != nil {
		return monty.Step{Err: err}
	}
	return h.stepFor(ctx, t)
}

// abort ends a feed that is suspended. The worker raises the exception where
// the script was waiting, the feed ends in an Error event, and the session
// stays usable. It returns what the script printed on the way out.
func (h *replHandle) abort(ctx context.Context, message string) (output, error) {
	exc := &montyv1.RaisedException{ExcType: "RuntimeError", Message: &message}
	t, err := h.sess.call(ctx, &montyv1.ClientRequest{Kind: &montyv1.ClientRequest_AbortFeed{
		AbortFeed: &montyv1.AbortFeed{Exception: exc},
	}})
	if err != nil {
		return output{}, err
	}
	if _, ok := t.end.GetKind().(*montyv1.WorkerEvent_Error); !ok {
		h.sess.fail(fmt.Errorf("remote: unexpected reply to AbortFeed: %T", t.end.GetKind()))
		return output{}, h.sess.dead
	}
	return t.prints, nil
}

// abortFeedWith gives up on a suspended feed because of cause, and reports
// cause to the caller. The session survives.
func (h *replHandle) abortFeedWith(ctx context.Context, cause error, message string, prints output) monty.Step {
	more, err := h.abort(ctx, message)
	if err != nil {
		return prints.step(monty.Step{Err: errors.Join(cause, err)})
	}
	return prints.plus(more).step(monty.Step{Err: cause, Repl: h})
}

// abortFeed gives up on a feed that needs something this backend cannot answer.
func (h *replHandle) abortFeed(ctx context.Context, what string, prints output) monty.Step {
	cause := fmt.Errorf("%w: the script needed %s", ErrUnsupported, what)
	return h.abortFeedWith(ctx, cause, "the remote backend cannot answer "+what+" yet", prints)
}

// completedProgress is the state after a snippet finished. The host layer takes
// the session back from it and closes it, which must leave the session open.
type completedProgress struct {
	repl *replHandle
}

var _ monty.ProgressHandle = (*completedProgress)(nil)

func (p *completedProgress) Close() {}

func (p *completedProgress) Dump() ([]byte, error) {
	return nil, fmt.Errorf("%w: Dump", ErrUnsupported)
}

func (p *completedProgress) TakeRepl() (monty.ReplHandle, error) { return p.repl, nil }

var errCompleted = errors.New("remote: a completed execution cannot be resumed")

func (p *completedProgress) ResumeCall(context.Context, monty.Result) monty.Step {
	return monty.Step{Err: errCompleted}
}

func (p *completedProgress) ResumeLookup(context.Context, monty.Value, bool) monty.Step {
	return monty.Step{Err: errCompleted}
}

func (p *completedProgress) ResumeFutures(context.Context, map[uint32]monty.Result) monty.Step {
	return monty.Step{Err: errCompleted}
}
