package remote

import (
	"context"
	"errors"
	"fmt"
	"runtime"
	"sync/atomic"

	monty "github.com/mdfranz/gomonty"
	montyv1 "github.com/mdfranz/gomonty/remote/internal/montyv1"
)

// Backend runs REPL sessions on a remote Monty worker. Pass it as
// monty.ReplOptions.Backend:
//
//	repl, err := monty.NewRepl(monty.ReplOptions{Backend: remote.NewBackend(transport)})
//
// Each REPL is one session on its own stream. Runners, dumps and snapshots are
// not supported yet, and a script that calls a host function, reads a name from
// the host or makes an OS call is stopped with ErrUnsupported; those arrive
// with the work tracked in #86 and #87.
type Backend struct {
	transport Transport
}

var _ monty.Backend = (*Backend)(nil)

// NewBackend returns a Backend that opens its sessions over t.
func NewBackend(t Transport) *Backend { return &Backend{transport: t} }

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
	ctx, cancel := context.WithCancel(context.Background())
	stream, err := b.transport.Open(ctx)
	if err != nil {
		cancel()
		return nil, err
	}
	sess := &session{stream: stream, cancel: cancel}

	configure := &montyv1.Configure{
		ScriptName:      opts.ScriptName,
		Limits:          limitsToProto(opts.Limits),
		MontyVersion:    "gomonty",
		ProtocolVersion: protocolVersion,
	}
	t, err := sess.call(&montyv1.ClientRequest{Kind: &montyv1.ClientRequest_Configure{Configure: configure}})
	if err != nil {
		sess.close()
		return nil, err
	}
	switch k := t.end.GetKind().(type) {
	case *montyv1.WorkerEvent_Ok:
		return newReplHandle(sess, opts.ScriptName), nil
	case *montyv1.WorkerEvent_SessionRefused:
		sess.close()
		return nil, &SessionRefusedError{Reason: k.SessionRefused.GetMessage()}
	default:
		sess.close()
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

func (h *replHandle) Dump() ([]byte, error) {
	return nil, fmt.Errorf("%w: Dump", ErrUnsupported)
}

func (h *replHandle) FeedStart(code string, opts monty.FeedStartOptions) monty.Step {
	refs, arena, err := encodeInputs(opts.Inputs)
	if err != nil {
		// Nothing was sent, so the handle is still good.
		return monty.Step{Err: err, Retained: true}
	}
	feed := &montyv1.Feed{Code: code, Inputs: refs, Values: arena}
	t, err := h.sess.call(&montyv1.ClientRequest{Kind: &montyv1.ClientRequest_Feed{Feed: feed}})

	h.consumed.Store(true)
	if err != nil {
		// The session is gone, so there is nothing to hand back.
		return monty.Step{Err: err}
	}
	return newReplHandle(h.sess, h.scriptName).stepFor(t)
}

// stepFor turns the event that ended a feed into a Step. The session is ready
// for another feed after every outcome except a fatal one, which call has
// already handled.
func (h *replHandle) stepFor(t turn) monty.Step {
	switch k := t.end.GetKind().(type) {
	case *montyv1.WorkerEvent_Complete:
		value, err := decodeValue(k.Complete.GetValues(), k.Complete.GetValue())
		if err != nil {
			return monty.Step{Err: err, Repl: h, Prints: t.prints}
		}
		return monty.Step{
			Progress: &completedProgress{repl: h},
			Prints:   t.prints,
			Desc: monty.ProgressDesc{
				Kind:       monty.ProgressComplete,
				ScriptName: h.scriptName,
				IsRepl:     true,
				Output:     value,
			},
		}
	case *montyv1.WorkerEvent_Error:
		return monty.Step{Err: pythonError(k.Error.GetException()), Repl: h, Prints: t.prints}
	case *montyv1.WorkerEvent_TypingError:
		return monty.Step{Err: &TypingError{Diagnostics: k.TypingError.GetDiagnostics()}, Repl: h, Prints: t.prints}
	case *montyv1.WorkerEvent_FunctionCall:
		return h.abortFeed("a host function call", t.prints)
	case *montyv1.WorkerEvent_OsCall:
		return h.abortFeed("an OS call", t.prints)
	case *montyv1.WorkerEvent_NameLookup:
		return h.abortFeed("a name from the host", t.prints)
	case *montyv1.WorkerEvent_ResolveFutures:
		return h.abortFeed("a future from the host", t.prints)
	case *montyv1.WorkerEvent_StreamedCalls:
		return h.abortFeed("a streamed host call", t.prints)
	default:
		h.sess.fail(fmt.Errorf("remote: unexpected event ending a feed: %T", k))
		return monty.Step{Err: h.sess.dead, Prints: t.prints}
	}
}

// abortFeed ends a feed that is suspended on something this backend cannot
// answer. The worker raises the exception where the script was waiting, the
// feed ends in an Error event, and the session stays usable.
func (h *replHandle) abortFeed(what, prints string) monty.Step {
	cause := fmt.Errorf("%w: the script needed %s", ErrUnsupported, what)
	message := "the remote backend cannot answer " + what + " yet"
	abort := &montyv1.AbortFeed{Exception: &montyv1.RaisedException{ExcType: "RuntimeError", Message: &message}}
	t, err := h.sess.call(&montyv1.ClientRequest{Kind: &montyv1.ClientRequest_AbortFeed{AbortFeed: abort}})
	if err != nil {
		return monty.Step{Err: errors.Join(cause, err), Prints: prints}
	}
	if _, ok := t.end.GetKind().(*montyv1.WorkerEvent_Error); !ok {
		h.sess.fail(fmt.Errorf("remote: unexpected reply to AbortFeed: %T", t.end.GetKind()))
		return monty.Step{Err: errors.Join(cause, h.sess.dead), Prints: prints}
	}
	return monty.Step{Err: cause, Repl: h, Prints: prints + t.prints}
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

func (p *completedProgress) ResumeCall(monty.Result) monty.Step {
	return monty.Step{Err: errCompleted}
}

func (p *completedProgress) ResumeLookup(monty.Value, bool) monty.Step {
	return monty.Step{Err: errCompleted}
}

func (p *completedProgress) ResumeFutures(map[uint32]monty.Result) monty.Step {
	return monty.Step{Err: errCompleted}
}
