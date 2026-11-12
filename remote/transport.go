package remote

import (
	"context"

	montyv1 "github.com/mdfranz/gomonty/remote/internal/montyv1"
)

// Stream is one REPL session's message stream to a worker. A *grpc stream
// from the generated client satisfies it as is.
//
// The protocol is a strict alternation: the client sends one request, and the
// worker answers with zero or more Print events followed by one turn-ending
// event. Ping events arrive between turns and are answered with Pong.
type Stream interface {
	Send(*montyv1.ClientRequest) error
	Recv() (*montyv1.WorkerEvent, error)
	CloseSend() error
}

// Transport opens streams. The session lives as long as the context passed to
// Open, so cancelling it ends the session.
//
// gRPC is the only transport today (see GRPCTransport). The rest of the
// package depends only on this interface, so another transport, such as a
// WebSocket relay, needs no change outside this file's siblings.
type Transport interface {
	Open(ctx context.Context) (Stream, error)
}
