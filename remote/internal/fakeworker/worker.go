// Package fakeworker is an in-process Monty worker for tests. It serves the
// monty.v1.MontySession gRPC service over an in-memory connection, and what it
// does is decided by a handler written in the test, so a test can script any
// sequence of events, including the failures a real worker rarely produces.
package fakeworker

import (
	"context"
	"errors"
	"io"
	"net"
	"testing"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/test/bufconn"

	montyv1 "github.com/mdfranz/gomonty/remote/internal/montyv1"
)

// Handler scripts one session. It runs on the server's goroutine; returning
// ends the stream.
type Handler func(*Stream)

// Stream is the worker's end of one session.
type Stream struct {
	t   testing.TB
	srv montyv1.MontySession_SessionServer
}

// Start serves handler and returns a client connection to it. The connection
// and server are closed when the test ends.
func Start(t testing.TB, handler Handler) *grpc.ClientConn {
	t.Helper()
	listener := bufconn.Listen(1 << 20)
	server := grpc.NewServer()
	montyv1.RegisterMontySessionServer(server, &service{t: t, handler: handler})
	go func() { _ = server.Serve(listener) }()

	conn, err := grpc.NewClient("passthrough:///fakeworker",
		grpc.WithContextDialer(func(context.Context, string) (net.Conn, error) { return listener.Dial() }),
		grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatalf("fakeworker: dial: %v", err)
	}
	t.Cleanup(func() {
		_ = conn.Close()
		server.Stop()
		_ = listener.Close()
	})
	return conn
}

type service struct {
	montyv1.UnimplementedMontySessionServer
	t       testing.TB
	handler Handler
}

func (s *service) Session(srv montyv1.MontySession_SessionServer) error {
	// A worker sends its response headers when it admits the stream; a client
	// waits for them before sending anything.
	if err := srv.SendHeader(metadata.MD{}); err != nil {
		return err
	}
	s.handler(&Stream{t: s.t, srv: srv})
	return nil
}

// Context is the session's context; it ends when the client goes away.
func (s *Stream) Context() context.Context { return s.srv.Context() }

// Token returns the bearer token the client sent, or "" if there was none.
func (s *Stream) Token() string {
	md, _ := metadata.FromIncomingContext(s.srv.Context())
	for _, v := range md.Get("authorization") {
		if len(v) > len("Bearer ") && v[:len("Bearer ")] == "Bearer " {
			return v[len("Bearer "):]
		}
	}
	return ""
}

// Send writes one event.
func (s *Stream) Send(event *montyv1.WorkerEvent) {
	s.t.Helper()
	if err := s.srv.Send(event); err != nil && s.srv.Context().Err() == nil {
		s.t.Errorf("fakeworker: send: %v", err)
	}
}

// Recv reads the next request. It returns nil when the client has closed the
// stream or gone away.
func (s *Stream) Recv() *montyv1.ClientRequest {
	s.t.Helper()
	req, err := s.srv.Recv()
	if err != nil {
		if !errors.Is(err, io.EOF) && s.srv.Context().Err() == nil {
			s.t.Errorf("fakeworker: recv: %v", err)
		}
		return nil
	}
	return req
}

// RecvConfigure reads the session's first request, which must be Configure.
func (s *Stream) RecvConfigure() *montyv1.Configure {
	s.t.Helper()
	req := s.Recv()
	if req == nil {
		return nil
	}
	cfg := req.GetConfigure()
	if cfg == nil {
		s.t.Errorf("fakeworker: first request was %T, want Configure", req.GetKind())
	}
	return cfg
}

// RecvFeed reads the next request, which must be Feed. It returns nil when the
// client has closed the stream.
func (s *Stream) RecvFeed() *montyv1.Feed {
	s.t.Helper()
	req := s.Recv()
	if req == nil {
		return nil
	}
	feed := req.GetFeed()
	if feed == nil {
		s.t.Errorf("fakeworker: request was %T, want Feed", req.GetKind())
	}
	return feed
}

// RecvDump reads the next request, which must be Dump.
func (s *Stream) RecvDump() *montyv1.Dump {
	s.t.Helper()
	req := s.Recv()
	if req == nil {
		return nil
	}
	dump := req.GetDump()
	if dump == nil {
		s.t.Errorf("fakeworker: request was %T, want Dump", req.GetKind())
	}
	return dump
}

// RecvReset reads the next request, which must be Reset.
func (s *Stream) RecvReset() *montyv1.Reset {
	s.t.Helper()
	req := s.Recv()
	if req == nil {
		return nil
	}
	reset := req.GetReset_()
	if reset == nil {
		s.t.Errorf("fakeworker: request was %T, want Reset", req.GetKind())
	}
	return reset
}

// Event constructors.

// WithStats sets the execution times a worker reports on a turn-ending event,
// in microseconds.
func WithStats(event *montyv1.WorkerEvent, totalMicros, feedMicros uint64) *montyv1.WorkerEvent {
	event.TotalExecutionMicros = totalMicros
	event.FeedExecutionMicros = feedMicros
	return event
}

// WithSessionID sets the session id a relay reports on its first reply.
func WithSessionID(event *montyv1.WorkerEvent, id []byte) *montyv1.WorkerEvent {
	event.SessionId = id
	return event
}

// Dumped answers a Dump with the given opaque state.
func Dumped(state []byte) *montyv1.WorkerEvent {
	return &montyv1.WorkerEvent{Kind: &montyv1.WorkerEvent_DumpResult{DumpResult: &montyv1.DumpResult{State: state}}}
}

// Restored answers a Configure that carried a Load, naming the script the
// dump came from.
func Restored(scriptName string) *montyv1.WorkerEvent {
	return &montyv1.WorkerEvent{
		Kind:               &montyv1.WorkerEvent_Ok{Ok: &montyv1.Ok{}},
		RestoredScriptName: &scriptName,
	}
}

// Ok acknowledges a Configure.
func Ok() *montyv1.WorkerEvent {
	return &montyv1.WorkerEvent{Kind: &montyv1.WorkerEvent_Ok{Ok: &montyv1.Ok{}}}
}

// Printed is a Print event with one stdout segment.
func Printed(text string) *montyv1.WorkerEvent {
	return &montyv1.WorkerEvent{Kind: &montyv1.WorkerEvent_Print{Print: &montyv1.Print{
		Segments: []*montyv1.PrintSegment{{Stream: montyv1.PrintStream_PRINT_STREAM_STDOUT, Text: text}},
	}}}
}

// PrintedSegments is a Print event with the given segments, in order.
func PrintedSegments(segments ...*montyv1.PrintSegment) *montyv1.WorkerEvent {
	return &montyv1.WorkerEvent{Kind: &montyv1.WorkerEvent_Print{Print: &montyv1.Print{Segments: segments}}}
}

// Stdout and Stderr build one print segment on that stream.
func Stdout(text string) *montyv1.PrintSegment {
	return &montyv1.PrintSegment{Stream: montyv1.PrintStream_PRINT_STREAM_STDOUT, Text: text}
}

func Stderr(text string) *montyv1.PrintSegment {
	return &montyv1.PrintSegment{Stream: montyv1.PrintStream_PRINT_STREAM_STDERR, Text: text}
}

// RaisedException ends a turn with a fully specified exception, traceback and
// data included.
func RaisedException(exc *montyv1.RaisedException) *montyv1.WorkerEvent {
	return &montyv1.WorkerEvent{Kind: &montyv1.WorkerEvent_Error{Error: &montyv1.Error{Exception: exc}}}
}

// Complete ends a turn with the node at root in the arena built from nodes.
func Complete(root uint32, nodes ...*montyv1.MontyNode) *montyv1.WorkerEvent {
	return &montyv1.WorkerEvent{Kind: &montyv1.WorkerEvent_Complete{Complete: &montyv1.Complete{
		Value:  root,
		Values: &montyv1.Arena{NodeCount: uint32(len(nodes)), Nodes: nodes},
	}}}
}

// CompleteWith ends a turn with a result taken from an existing arena.
func CompleteWith(root uint32, arena *montyv1.Arena) *montyv1.WorkerEvent {
	return &montyv1.WorkerEvent{Kind: &montyv1.WorkerEvent_Complete{Complete: &montyv1.Complete{Value: root, Values: arena}}}
}

// Raised ends a turn with a Python exception.
func Raised(excType, message string) *montyv1.WorkerEvent {
	return &montyv1.WorkerEvent{Kind: &montyv1.WorkerEvent_Error{Error: &montyv1.Error{
		Exception: &montyv1.RaisedException{ExcType: excType, Message: &message},
	}}}
}

// TypeCheckFailed ends a turn with a type-check rejection.
func TypeCheckFailed(diagnostics string) *montyv1.WorkerEvent {
	return &montyv1.WorkerEvent{Kind: &montyv1.WorkerEvent_TypingError{TypingError: &montyv1.TypingError{Diagnostics: diagnostics}}}
}

// Refused answers a Configure with SessionRefused.
func Refused(message string) *montyv1.WorkerEvent {
	return &montyv1.WorkerEvent{Kind: &montyv1.WorkerEvent_SessionRefused{SessionRefused: &montyv1.SessionRefused{Message: message}}}
}

// Fatal is an unrecoverable worker error.
func Fatal(message string) *montyv1.WorkerEvent {
	return &montyv1.WorkerEvent{Kind: &montyv1.WorkerEvent_FatalError{FatalError: &montyv1.FatalError{Message: message}}}
}

// Ping is a liveness probe the client must answer with Pong.
func Ping() *montyv1.WorkerEvent {
	return &montyv1.WorkerEvent{Kind: &montyv1.WorkerEvent_Ping{Ping: &montyv1.Ping{}}}
}

// CallsHost suspends the feed on a host function call with no arguments.
func CallsHost(name string) *montyv1.WorkerEvent {
	return FunctionCall(name, 1, nil, nil)
}

// FunctionCall suspends the feed on a host function call. args and kwargs
// index into nodes, which are the call's arena.
func FunctionCall(name string, callID uint32, args []uint32, kwargs []*montyv1.NodePair, nodes ...*montyv1.MontyNode) *montyv1.WorkerEvent {
	return &montyv1.WorkerEvent{Kind: &montyv1.WorkerEvent_FunctionCall{FunctionCall: &montyv1.FunctionCall{
		FunctionName: name,
		Args:         args,
		Kwargs:       kwargs,
		CallId:       callID,
		Values:       &montyv1.Arena{NodeCount: uint32(len(nodes)), Nodes: nodes},
	}}}
}

// MethodCall is a call on a host object, which the receiver's id names.
func MethodCall(name string, callID uint32) *montyv1.WorkerEvent {
	return &montyv1.WorkerEvent{Kind: &montyv1.WorkerEvent_FunctionCall{FunctionCall: &montyv1.FunctionCall{
		FunctionName: name,
		CallId:       callID,
		ObjectId:     &montyv1.Uuid{Data: make([]byte, 16)},
		Values:       &montyv1.Arena{},
	}}}
}

// OS suspends the feed on an OS call. call.Values may carry the arena that
// argument indexes (such as os.getenv's default) point into.
func OS(callID uint32, call *montyv1.OsCall) *montyv1.WorkerEvent {
	call.CallId = callID
	if call.Values == nil {
		call.Values = &montyv1.Arena{}
	}
	return &montyv1.WorkerEvent{Kind: &montyv1.WorkerEvent_OsCall{OsCall: call}}
}

// NameLookup suspends the feed on a name the host should supply.
func NameLookup(name string) *montyv1.WorkerEvent {
	return &montyv1.WorkerEvent{Kind: &montyv1.WorkerEvent_NameLookup{NameLookup: &montyv1.NameLookup{Name: name}}}
}

// ResolveFutures suspends the feed until the host resolves these calls.
func ResolveFutures(ids ...uint32) *montyv1.WorkerEvent {
	return &montyv1.WorkerEvent{Kind: &montyv1.WorkerEvent_ResolveFutures{ResolveFutures: &montyv1.ResolveFutures{PendingCallIds: ids}}}
}

// Request readers. Each fails the test, and returns nil, if the client sent
// something else.

// RecvResumeCall reads the client's answer to a call.
func (s *Stream) RecvResumeCall() *montyv1.ResumeCall {
	s.t.Helper()
	req := s.Recv()
	if req == nil {
		return nil
	}
	r := req.GetResumeCall()
	if r == nil {
		s.t.Errorf("fakeworker: request was %T, want ResumeCall", req.GetKind())
	}
	return r
}

// RecvResumeNameLookup reads the client's answer to a name lookup.
func (s *Stream) RecvResumeNameLookup() *montyv1.ResumeNameLookup {
	s.t.Helper()
	req := s.Recv()
	if req == nil {
		return nil
	}
	r := req.GetResumeNameLookup()
	if r == nil {
		s.t.Errorf("fakeworker: request was %T, want ResumeNameLookup", req.GetKind())
	}
	return r
}

// RecvResumeFutures reads the client's answer to a wait on futures.
func (s *Stream) RecvResumeFutures() *montyv1.ResumeFutures {
	s.t.Helper()
	req := s.Recv()
	if req == nil {
		return nil
	}
	r := req.GetResumeFutures()
	if r == nil {
		s.t.Errorf("fakeworker: request was %T, want ResumeFutures", req.GetKind())
	}
	return r
}

// RecvAbortFeed reads the client giving up on the feed.
func (s *Stream) RecvAbortFeed() *montyv1.AbortFeed {
	s.t.Helper()
	req := s.Recv()
	if req == nil {
		return nil
	}
	a := req.GetAbortFeed()
	if a == nil {
		s.t.Errorf("fakeworker: request was %T, want AbortFeed", req.GetKind())
	}
	return a
}

// Node constructors.

// Int is an int node.
func Int(n int64) *montyv1.MontyNode {
	return &montyv1.MontyNode{Kind: &montyv1.MontyNode_Int{Int: n}}
}

// Str is a str node.
func Str(s string) *montyv1.MontyNode {
	return &montyv1.MontyNode{Kind: &montyv1.MontyNode_Str{Str: s}}
}

// List is a list node over earlier nodes.
func List(items ...uint32) *montyv1.MontyNode {
	return &montyv1.MontyNode{Kind: &montyv1.MontyNode_List{List: &montyv1.Indexes{Items: items}}}
}
