package remote

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"

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

	closeOnce sync.Once
	// dead is set once the stream can no longer be used.
	dead error
}

// turn is a request's outcome: the output printed during it and the event that
// ended it.
type turn struct {
	prints string
	end    *montyv1.WorkerEvent
}

// call sends one request and reads until the turn ends. Print events are
// gathered, and Ping is answered with Pong. If the worker reports a fatal
// error, or the stream ends without one, the session is dead afterwards.
func (s *session) call(req *montyv1.ClientRequest) (turn, error) {
	if s.dead != nil {
		return turn{}, s.dead
	}
	if err := s.stream.Send(req); err != nil {
		return turn{}, s.fail(fmt.Errorf("remote: send: %w", err))
	}
	var prints strings.Builder
	for {
		event, err := s.stream.Recv()
		if err != nil {
			if errors.Is(err, io.EOF) {
				// A worker that exits without a FatalError crashed.
				err = errors.New("the worker closed the stream without finishing the turn")
			}
			return turn{}, s.fail(fmt.Errorf("remote: receive: %w", err))
		}
		switch kind := event.GetKind().(type) {
		case *montyv1.WorkerEvent_Print:
			for _, segment := range kind.Print.GetSegments() {
				prints.WriteString(segment.GetText())
			}
		case *montyv1.WorkerEvent_Ping:
			pong := &montyv1.ClientRequest{Kind: &montyv1.ClientRequest_Pong{Pong: &montyv1.Pong{}}}
			if err := s.stream.Send(pong); err != nil {
				return turn{}, s.fail(fmt.Errorf("remote: answer ping: %w", err))
			}
		case *montyv1.WorkerEvent_FatalError:
			return turn{}, s.fail(fmt.Errorf("remote: the worker failed: %s", kind.FatalError.GetMessage()))
		default:
			return turn{prints: prints.String(), end: event}, nil
		}
	}
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
		_ = s.stream.CloseSend()
		s.cancel()
	})
}
