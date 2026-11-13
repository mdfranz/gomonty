package remote

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"

	monty "github.com/mdfranz/gomonty"
	montyv1 "github.com/mdfranz/gomonty/remote/internal/montyv1"
)

// protocolVersion is the wire schema version this client speaks. A worker
// rejects a session that declares nothing, or a version outside its range.
// 8 is PROTOCOL_VERSION in monty-proto at the pinned upstream commit (see
// proto/UPSTREAM); the worker there serves versions 7 to 8.
const protocolVersion = 8

// session is one REPL session on one stream. It does not synchronize
// concurrent turns: the handles that wrap it are used by one goroutine at a
// time, which Runner, Repl and the progress types enforce.
type session struct {
	stream Stream
	cancel context.CancelFunc

	// A reader goroutine owns stream.Recv so that Ping is answered between
	// turns too: the relay drops a client that goes quiet. It forwards every
	// other event, then the stream's error, on events. sendMu keeps its Pong
	// from interleaving with a request being sent.
	sendMu sync.Mutex
	events chan recvResult
	done   chan struct{}

	// id is the worker's session id, hex encoded, or "" if it sent none.
	id string

	// configure is what opened the session, kept so Reset can open it again
	// on the same stream. Its first request is cleared.
	configure *montyv1.Configure

	closeOnce sync.Once
	// dead is set once the stream can no longer be used.
	dead error
}

type recvResult struct {
	event *montyv1.WorkerEvent
	err   error
}

// newSession wraps stream and starts reading it.
func newSession(stream Stream, cancel context.CancelFunc) *session {
	s := &session{stream: stream, cancel: cancel, events: make(chan recvResult), done: make(chan struct{})}
	go s.readLoop()
	return s
}

func (s *session) readLoop() {
	for {
		event, err := s.stream.Recv()
		if err == nil {
			if _, ok := event.GetKind().(*montyv1.WorkerEvent_Ping); ok {
				pong := &montyv1.ClientRequest{Kind: &montyv1.ClientRequest_Pong{Pong: &montyv1.Pong{}}}
				s.sendMu.Lock()
				_ = s.stream.Send(pong) // a dead stream fails the next Recv too
				s.sendMu.Unlock()
				continue
			}
		}
		select {
		case s.events <- recvResult{event, err}:
		case <-s.done:
			return
		}
		if err != nil {
			return
		}
	}
}

// recv returns the next event or the stream's error.
func (s *session) recv() (*montyv1.WorkerEvent, error) {
	select {
	case r := <-s.events:
		return r.event, r.err
	case <-s.done:
		return nil, errors.New("remote: the session was closed")
	}
}

// send writes one request, serialized with the reader's Pong.
func (s *session) send(req *montyv1.ClientRequest) error {
	s.sendMu.Lock()
	defer s.sendMu.Unlock()
	return s.stream.Send(req)
}

// turn is a request's outcome: the output printed during it and the event that
// ended it.
type turn struct {
	prints output
	end    *montyv1.WorkerEvent
}

// output is what the sandbox printed, as runs on one stream each, in order.
// delivered says the runs already went to the host's print sink as they
// arrived, so the host layer must not report them again.
type output struct {
	segments  []monty.PrintSegment
	delivered bool
}

// plus returns o followed by more.
func (o output) plus(more output) output {
	return output{
		segments:  append(append([]monty.PrintSegment(nil), o.segments...), more.segments...),
		delivered: o.delivered || more.delivered,
	}
}

// text is every run joined, whatever its stream.
func (o output) text() string {
	var b strings.Builder
	for _, segment := range o.segments {
		b.WriteString(segment.Text)
	}
	return b.String()
}

// step attaches the output to a Step.
func (o output) step(s monty.Step) monty.Step {
	if len(o.segments) > 0 {
		s.Prints = o.text()
		s.PrintSegments = o.segments
		s.PrintsDelivered = o.delivered
	}
	return s
}

func streamName(s montyv1.PrintStream) string {
	if s == montyv1.PrintStream_PRINT_STREAM_STDERR {
		return "stderr"
	}
	return "stdout"
}

// call sends one request and reads until the turn ends. Print events go to the
// context's print sink as they arrive, if it has one, and are also gathered
// for the turn's result. Ping is answered with Pong. If the worker reports a fatal
// error, or the stream ends without one, the session is dead afterwards.
//
// If ctx ends first, the call returns ctx's error and the session is dead
// afterwards: a blocked send or receive can only be stopped by ending the
// stream, and a session cannot carry on after one of its turns is abandoned.
// A context that has already ended fails before anything is sent, and leaves
// the session usable.
func (s *session) call(ctx context.Context, req *montyv1.ClientRequest) (turn, error) {
	if s.dead != nil {
		return turn{}, s.dead
	}
	if err := ctx.Err(); err != nil {
		return turn{}, err
	}
	stop := context.AfterFunc(ctx, s.cancel)
	defer stop()

	if err := s.send(req); err != nil {
		return turn{}, s.fail(callError(ctx, "send", s.sendCause(err)))
	}
	var prints output
	sink := monty.PrintSinkFromContext(ctx)
	for {
		event, err := s.recv()
		if err != nil {
			if errors.Is(err, io.EOF) && ctx.Err() == nil {
				// A worker that exits without a FatalError crashed.
				err = errors.New("the worker closed the stream without finishing the turn")
			}
			return turn{}, s.fail(callError(ctx, "receive", err))
		}
		switch kind := event.GetKind().(type) {
		case *montyv1.WorkerEvent_Print:
			for _, segment := range kind.Print.GetSegments() {
				text := segment.GetText()
				if text == "" {
					continue
				}
				run := monty.PrintSegment{Stream: streamName(segment.GetStream()), Text: text}
				prints.segments = append(prints.segments, run)
				if sink != nil {
					// Hand the run on now: a turn can last a long time.
					sink(run.Stream, run.Text)
					prints.delivered = true
				}
			}
		case *montyv1.WorkerEvent_FatalError:
			return turn{}, s.fail(fmt.Errorf("remote: the worker failed: %s", kind.FatalError.GetMessage()))
		default:
			s.reportStats(ctx, event)
			return turn{prints: prints, end: event}, nil
		}
	}
}

// reportStats gives the caller's stats sink, if it has one, what the worker
// says about the session at the end of a turn.
func (s *session) reportStats(ctx context.Context, event *montyv1.WorkerEvent) {
	sink := monty.WorkerStatsSinkFromContext(ctx)
	if sink == nil {
		return
	}
	sink(monty.WorkerStats{
		SessionExecution: time.Duration(event.GetTotalExecutionMicros()) * time.Microsecond,
		FeedExecution:    time.Duration(event.GetFeedExecutionMicros()) * time.Microsecond,
		SessionID:        s.id,
	})
}

// sendCause explains a failed send. gRPC reports a stream the worker already
// ended as a bare io.EOF and keeps the real status (or a final FatalError
// event) for Recv, so read it to say why the worker hung up.
func (s *session) sendCause(err error) error {
	if !errors.Is(err, io.EOF) {
		return err
	}
	for {
		event, rerr := s.recv()
		if rerr != nil {
			if errors.Is(rerr, io.EOF) {
				return errors.New("the worker closed the stream before the request was sent")
			}
			return rerr
		}
		if fatal, ok := event.GetKind().(*montyv1.WorkerEvent_FatalError); ok {
			return fmt.Errorf("the worker failed: %s", fatal.FatalError.GetMessage())
		}
	}
}

// callError reports a failed send or receive. If the caller's context ended,
// that is the cause, whatever error the cancelled stream returned.
func callError(ctx context.Context, op string, err error) error {
	if ctxErr := ctx.Err(); ctxErr != nil {
		return ctxErr
	}
	return &ConnectionLostError{Op: op, Err: err}
}

func (s *session) fail(err error) error {
	if s.dead == nil {
		s.dead = err
	}
	s.close()
	return s.dead
}

// close ends the session. It is safe to call more than once.
func (s *session) close() {
	s.closeOnce.Do(func() {
		close(s.done)
		_ = s.stream.CloseSend()
		s.cancel()
	})
}
