package remote_test

import (
	"context"
	"errors"
	"log/slog"
	"math"
	"math/big"
	"strings"
	"sync"
	"testing"
	"time"

	monty "github.com/mdfranz/gomonty"
	"github.com/mdfranz/gomonty/remote"
	"github.com/mdfranz/gomonty/remote/internal/fakeworker"
	montyv1 "github.com/mdfranz/gomonty/remote/internal/montyv1"
)

const token = "test-token"

// serve answers Configure with Ok, then calls onFeed for each Feed, numbering
// them from 1, until the client closes the stream.
func serve(onFeed func(s *fakeworker.Stream, n int, feed *montyv1.Feed)) fakeworker.Handler {
	return func(s *fakeworker.Stream) {
		if s.RecvConfigure() == nil {
			return
		}
		s.Send(fakeworker.Ok())
		for n := 1; ; n++ {
			feed := s.RecvFeed()
			if feed == nil {
				return
			}
			onFeed(s, n, feed)
		}
	}
}

func newRepl(t *testing.T, handler fakeworker.Handler) *monty.Repl {
	t.Helper()
	conn := fakeworker.Start(t, handler)
	backend := remote.NewBackend(remote.NewGRPCTransport(conn, token))
	repl, err := monty.NewRepl(monty.ReplOptions{ScriptName: "remote_test.py", Backend: backend})
	if err != nil {
		t.Fatalf("NewRepl: %v", err)
	}
	return repl
}

func feed(t *testing.T, repl *monty.Repl, code string, opts monty.FeedOptions) (monty.Value, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return repl.FeedRun(ctx, code, opts)
}

func wantInt(t *testing.T, got monty.Value, err error, want int64) {
	t.Helper()
	if err != nil {
		t.Fatalf("FeedRun: %v", err)
	}
	if got.Raw() != want {
		t.Fatalf("result = %#v, want %d", got.Raw(), want)
	}
}

// recorder keeps what the worker saw, for the test goroutine to read.
type recorder struct {
	mu     sync.Mutex
	token  string
	config *montyv1.Configure
	codes  []string
}

func (r *recorder) set(f func(*recorder)) {
	r.mu.Lock()
	defer r.mu.Unlock()
	f(r)
}

func TestSessionIsConfiguredAndReused(t *testing.T) {
	rec := &recorder{}
	handler := func(s *fakeworker.Stream) {
		cfg := s.RecvConfigure()
		if cfg == nil {
			return
		}
		rec.set(func(r *recorder) { r.token, r.config = s.Token(), cfg })
		s.Send(fakeworker.Ok())
		for {
			f := s.RecvFeed()
			if f == nil {
				return
			}
			rec.set(func(r *recorder) { r.codes = append(r.codes, f.GetCode()) })
			s.Send(fakeworker.Printed("hi\n"))
			s.Send(fakeworker.Complete(0, fakeworker.Int(42)))
		}
	}

	conn := fakeworker.Start(t, handler)
	limits := &monty.ResourceLimits{MaxDuration: 2 * time.Second, MaxMemory: 1 << 20, MaxRecursionDepth: 50, MaxSuspensions: 7}
	repl, err := monty.NewRepl(monty.ReplOptions{
		ScriptName: "session.py",
		Limits:     limits,
		Backend:    remote.NewBackend(remote.NewGRPCTransport(conn, token)),
	})
	if err != nil {
		t.Fatal(err)
	}

	var printed string
	opts := monty.FeedOptions{Print: func(_, text string) { printed += text }}
	got, err := feed(t, repl, "first", opts)
	wantInt(t, got, err, 42)
	got, err = feed(t, repl, "second", opts)
	wantInt(t, got, err, 42)

	if printed != "hi\nhi\n" {
		t.Fatalf("printed %q, want the worker's output from both feeds", printed)
	}

	rec.mu.Lock()
	defer rec.mu.Unlock()
	if rec.token != token {
		t.Errorf("worker saw token %q, want %q", rec.token, token)
	}
	cfg := rec.config
	if cfg.GetScriptName() != "session.py" {
		t.Errorf("script name = %q", cfg.GetScriptName())
	}
	if cfg.GetProtocolVersion() != 8 {
		t.Errorf("protocol version = %d, want 8 (a worker rejects 0)", cfg.GetProtocolVersion())
	}
	l := cfg.GetLimits()
	if l.GetMaxFeedDurationMicros() != 2_000_000 || l.GetMaxMemoryBytes() != 1<<20 ||
		l.GetMaxRecursionDepth() != 50 || l.GetMaxSuspensions() != 7 {
		t.Errorf("limits = %v", l)
	}
	if strings.Join(rec.codes, ",") != "first,second" {
		t.Errorf("worker was fed %v", rec.codes)
	}
}

func TestInputsAndResultsRoundTripThroughTheArena(t *testing.T) {
	// The worker echoes the first input back, so what reaches the client has
	// been encoded and decoded.
	repl := newRepl(t, serve(func(s *fakeworker.Stream, _ int, f *montyv1.Feed) {
		s.Send(fakeworker.CompleteWith(f.GetInputs()[0].GetValue(), f.GetValues()))
	}))

	huge := new(big.Int).Lsh(big.NewInt(1), 100)
	text := "héllo"
	offset := int32(-18000)
	zoneName := "EST"
	cases := map[string]monty.Value{
		"none":       monty.None(),
		"ellipsis":   monty.Ellipsis(),
		"true":       monty.Bool(true),
		"zero":       monty.Int(0),
		"negative":   monty.Int(-7),
		"max":        monty.Int(math.MaxInt64),
		"min":        monty.Int(math.MinInt64),
		"big":        monty.BigInt(huge),
		"negbig":     monty.BigInt(new(big.Int).Neg(huge)),
		"float":      monty.Float(1.5),
		"complex":    monty.Complex(complex(1, -2)),
		"string":     monty.String(text),
		"bytes":      monty.Bytes([]byte{0, 1, 255}),
		"list":       monty.List(monty.Int(1), monty.String("two"), monty.List(monty.None())),
		"tuple":      monty.TupleValue(monty.Int(1), monty.Int(2)),
		"set":        monty.SetValue(monty.Set{monty.Int(3)}),
		"frozenset":  monty.FrozenSetValue(monty.FrozenSet{monty.String("a")}),
		"decimal":    monty.DecimalValue("1.20"),
		"path":       monty.PathValue("/tmp/x"),
		"exception":  monty.ExceptionValue(monty.Exception{Type: "ValueError", Arg: &text}),
		"function":   monty.FunctionValue(monty.Function{Name: "tool"}),
		"date":       monty.DateValue(monty.Date{Year: 2026, Month: 11, Day: 12}),
		"time":       monty.TimeValue(monty.Time{Hour: 1, Minute: 2, Second: 3, Microsecond: 4, OffsetSeconds: &offset, Fold: 1}),
		"datetime":   monty.DateTimeValue(monty.DateTime{Year: 2026, Month: 11, Day: 12, Hour: 13, Minute: 14, Second: 15, Microsecond: 16, OffsetSeconds: &offset, TimezoneName: &zoneName}),
		"naive":      monty.DateTimeValue(monty.DateTime{Year: 1999, Month: 1, Day: 2}),
		"timedelta":  monty.TimeDeltaValue(monty.TimeDelta{Days: -1, Seconds: 2, Microseconds: 3}),
		"timezone":   monty.TimeZoneValue(monty.TimeZone{OffsetSeconds: 3600, Name: &zoneName}),
		"namedtuple": monty.NamedTupleValue(monty.NamedTuple{TypeName: "StatResult", FieldNames: []string{"size", "name"}, Values: []monty.Value{monty.Int(7), monty.String("x")}}),
		"module": monty.ModuleValue(monty.Module{Name: "hostmod", ID: "0b9c6a3e-5f2d-4c1a-8e7b-123456789abc", Attrs: monty.Dict{
			{Key: monty.String("answer"), Value: monty.Int(42)},
			{Key: monty.String("names"), Value: monty.List(monty.String("a"))},
		}}),
		"emptylist":  monty.List(),
		"nestedlist": monty.List(monty.List(monty.List(monty.Int(1)))),
		"dict": monty.DictValue(monty.Dict{
			{Key: monty.String("b"), Value: monty.Int(2)},
			{Key: monty.String("a"), Value: monty.List(monty.Int(1))},
			{Key: monty.Int(3), Value: monty.None()},
		}),
	}
	for name, want := range cases {
		t.Run(name, func(t *testing.T) {
			got, err := feed(t, repl, "x", monty.FeedOptions{Inputs: map[string]monty.Value{"x": want}})
			if err != nil {
				t.Fatalf("FeedRun: %v", err)
			}
			if got.Kind() != want.Kind() || got.String() != want.String() {
				t.Fatalf("got %s %s, want %s %s", got.Kind(), got, want.Kind(), want)
			}
		})
	}

	t.Run("negative zero keeps its sign", func(t *testing.T) {
		got, err := feed(t, repl, "x", monty.FeedOptions{Inputs: map[string]monty.Value{"x": monty.Float(math.Copysign(0, -1))}})
		if err != nil {
			t.Fatal(err)
		}
		if f, _ := got.Raw().(float64); !math.Signbit(f) {
			t.Fatalf("-0.0 came back as %v", f)
		}
	})
}

// The worker builds results in post-order and may share a child between
// holders, which the client's own encoder never does.
func TestWorkerResultsMayShareNodes(t *testing.T) {
	repl := newRepl(t, serve(func(s *fakeworker.Stream, _ int, _ *montyv1.Feed) {
		s.Send(fakeworker.Complete(3, fakeworker.Int(1), fakeworker.Int(2), fakeworker.Str("x"), fakeworker.List(0, 1, 0, 2)))
	}))
	got, err := feed(t, repl, "x", monty.FeedOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if want := monty.List(monty.Int(1), monty.Int(2), monty.Int(1), monty.String("x")); got.String() != want.String() {
		t.Fatalf("got %s, want %s", got, want)
	}
}

func TestPythonErrorLeavesTheSessionUsable(t *testing.T) {
	repl := newRepl(t, serve(func(s *fakeworker.Stream, n int, _ *montyv1.Feed) {
		s.Send(fakeworker.Printed("before the error\n"))
		if n == 1 {
			s.Send(fakeworker.Raised("ValueError", "boom"))
			return
		}
		s.Send(fakeworker.Complete(0, fakeworker.Int(1)))
	}))

	var printed string
	_, err := feed(t, repl, "bad", monty.FeedOptions{Print: func(_, text string) { printed += text }})
	var pyErr *remote.PythonError
	if !errors.As(err, &pyErr) || pyErr.Type != "ValueError" || pyErr.Message != "boom" {
		t.Fatalf("err = %v, want a ValueError(boom)", err)
	}
	if pyErr.Error() != "ValueError: boom" {
		t.Fatalf("message = %q", pyErr.Error())
	}
	if printed != "before the error\n" {
		t.Fatalf("printed %q, want the output that preceded the error", printed)
	}

	got, err := feed(t, repl, "good", monty.FeedOptions{})
	wantInt(t, got, err, 1)
}

func TestTypeCheckFailureLeavesTheSessionUsable(t *testing.T) {
	repl := newRepl(t, serve(func(s *fakeworker.Stream, n int, _ *montyv1.Feed) {
		if n == 1 {
			s.Send(fakeworker.TypeCheckFailed("error[invalid-argument-type]"))
			return
		}
		s.Send(fakeworker.Complete(0, fakeworker.Int(2)))
	}))
	_, err := feed(t, repl, "bad", monty.FeedOptions{})
	var typing *remote.TypingError
	if !errors.As(err, &typing) || !strings.Contains(typing.Diagnostics, "invalid-argument-type") {
		t.Fatalf("err = %v, want a TypingError", err)
	}
	got, err := feed(t, repl, "good", monty.FeedOptions{})
	wantInt(t, got, err, 2)
}

func TestRefusedSessionFailsNewRepl(t *testing.T) {
	conn := fakeworker.Start(t, func(s *fakeworker.Stream) {
		if s.RecvConfigure() != nil {
			s.Send(fakeworker.Refused("unknown profile: x"))
		}
	})
	_, err := monty.NewRepl(monty.ReplOptions{Backend: remote.NewBackend(remote.NewGRPCTransport(conn, ""))})
	var refused *remote.SessionRefusedError
	if !errors.As(err, &refused) || refused.Reason != "unknown profile: x" {
		t.Fatalf("err = %v, want a SessionRefusedError", err)
	}
}

func TestFatalErrorEndsTheSession(t *testing.T) {
	repl := newRepl(t, serve(func(s *fakeworker.Stream, _ int, _ *montyv1.Feed) {
		s.Send(fakeworker.Fatal("frame desync"))
	}))
	_, err := feed(t, repl, "x", monty.FeedOptions{})
	if err == nil || !strings.Contains(err.Error(), "frame desync") {
		t.Fatalf("err = %v, want the worker's message", err)
	}
	if _, err := feed(t, repl, "x", monty.FeedOptions{}); !errors.Is(err, monty.ErrClosed) {
		t.Fatalf("a dead session should be closed, got %v", err)
	}
}

// A worker that exits without a FatalError crashed; the client must not wait
// for it.
func TestWorkerCrashIsReported(t *testing.T) {
	repl := newRepl(t, func(s *fakeworker.Stream) {
		if s.RecvConfigure() == nil {
			return
		}
		s.Send(fakeworker.Ok())
		s.RecvFeed()
		// Return without answering, which ends the stream: the worker died.
	})
	_, err := feed(t, repl, "x", monty.FeedOptions{})
	var lost *remote.ConnectionLostError
	if !errors.As(err, &lost) || !strings.Contains(err.Error(), "closed the stream") {
		t.Fatalf("err = %v, want a ConnectionLostError about a closed stream", err)
	}
}

func TestPingIsAnswered(t *testing.T) {
	answered := make(chan bool, 1)
	repl := newRepl(t, serve(func(s *fakeworker.Stream, _ int, _ *montyv1.Feed) {
		s.Send(fakeworker.Ping())
		req := s.Recv()
		answered <- req != nil && req.GetPong() != nil
		s.Send(fakeworker.Complete(0, fakeworker.Int(5)))
	}))
	got, err := feed(t, repl, "x", monty.FeedOptions{})
	wantInt(t, got, err, 5)
	if !<-answered {
		t.Fatal("the client did not answer Ping with Pong")
	}
}

// A relay pings between turns and drops a client that stays quiet, so the
// Pong must come while the client is idle, not only while it awaits a reply.
func TestPingBetweenTurnsIsAnswered(t *testing.T) {
	answered := make(chan bool, 1)
	repl := newRepl(t, serve(func(s *fakeworker.Stream, n int, _ *montyv1.Feed) {
		s.Send(fakeworker.Complete(0, fakeworker.Int(5)))
		if n == 1 {
			s.Send(fakeworker.Ping())
			req := s.Recv()
			answered <- req != nil && req.GetPong() != nil
		}
	}))
	got, err := feed(t, repl, "x", monty.FeedOptions{})
	wantInt(t, got, err, 5)
	select {
	case ok := <-answered:
		if !ok {
			t.Fatal("the worker received something other than Pong")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the client did not answer a Ping sent between turns")
	}
	got, err = feed(t, repl, "y", monty.FeedOptions{})
	wantInt(t, got, err, 5)
}

func TestUnsendableInputIsRejectedBeforeAnythingIsSent(t *testing.T) {
	feeds := 0
	var mu sync.Mutex
	repl := newRepl(t, serve(func(s *fakeworker.Stream, _ int, _ *montyv1.Feed) {
		mu.Lock()
		feeds++
		mu.Unlock()
		s.Send(fakeworker.Complete(0, fakeworker.Int(3)))
	}))

	bad := monty.ClassInstanceValue(monty.ClassInstance{})
	_, err := feed(t, repl, "x", monty.FeedOptions{Inputs: map[string]monty.Value{"when": bad}})
	if err == nil || !strings.Contains(err.Error(), `input "when"`) {
		t.Fatalf("err = %v, want the input named", err)
	}
	got, err := feed(t, repl, "x", monty.FeedOptions{})
	wantInt(t, got, err, 3)

	mu.Lock()
	defer mu.Unlock()
	if feeds != 1 {
		t.Fatalf("the worker saw %d feeds, want 1: the bad input must not be sent", feeds)
	}
}

func TestMalformedResultIsAnErrorNotAPanic(t *testing.T) {
	repl := newRepl(t, serve(func(s *fakeworker.Stream, n int, _ *montyv1.Feed) {
		switch n {
		case 1: // root outside the arena
			s.Send(fakeworker.Complete(5, fakeworker.Int(1)))
		case 2: // a child that does not precede its holder
			s.Send(fakeworker.Complete(0, fakeworker.List(0)))
		default:
			s.Send(fakeworker.Complete(0, fakeworker.Int(4)))
		}
	}))
	for i := 0; i < 2; i++ {
		if _, err := feed(t, repl, "x", monty.FeedOptions{}); err == nil {
			t.Fatalf("malformed result %d was accepted", i+1)
		}
	}
	got, err := feed(t, repl, "x", monty.FeedOptions{})
	wantInt(t, got, err, 4)
}

func TestUnsupportedOperations(t *testing.T) {
	conn := fakeworker.Start(t, func(*fakeworker.Stream) {})
	backend := remote.NewBackend(remote.NewGRPCTransport(conn, ""))

	if _, err := monty.New("1", monty.CompileOptions{Backend: backend}); !errors.Is(err, remote.ErrUnsupported) {
		t.Errorf("a runner: err = %v, want ErrUnsupported", err)
	}
	if _, err := backend.LoadRunner(nil); !errors.Is(err, remote.ErrUnsupported) {
		t.Errorf("LoadRunner: err = %v", err)
	}
	if step := backend.LoadProgress(nil); !errors.Is(step.Err, remote.ErrUnsupported) {
		t.Errorf("LoadProgress: err = %v", step.Err)
	}
}

// Only an idle REPL can be dumped; a snippet that is paused on the host has no
// remote snapshot yet.
func TestSnapshotOfAPausedSnippetIsNotSupportedYet(t *testing.T) {
	repl := newRepl(t, serve(func(s *fakeworker.Stream, _ int, _ *montyv1.Feed) {
		s.Send(fakeworker.CallsHost("later"))
		s.Recv()
	}))
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	progress, err := repl.FeedStart(ctx, "later()", monty.FeedStartOptions{})
	if err != nil {
		t.Fatal(err)
	}
	snapshot, ok := progress.(*monty.Snapshot)
	if !ok {
		t.Fatalf("progress is %T, want a call snapshot", progress)
	}
	if _, err := snapshot.Dump(); !errors.Is(err, remote.ErrUnsupported) {
		t.Fatalf("Dump of a paused snippet: err = %v, want ErrUnsupported", err)
	}
}

func TestDialGRPCRefusesATokenWithoutTLS(t *testing.T) {
	_, err := remote.DialGRPC("localhost:1", remote.GRPCConfig{Insecure: true, Token: "secret"})
	if err == nil || strings.Contains(err.Error(), "secret") {
		t.Fatalf("err = %v, want a refusal that does not echo the token", err)
	}
}

func TestDialGRPCRefusesAnAuthorizationHeaderWithoutTLS(t *testing.T) {
	_, err := remote.DialGRPC("localhost:1", remote.GRPCConfig{
		Insecure: true,
		Metadata: map[string]string{"Authorization": "Bearer secret"},
	})
	if err == nil || strings.Contains(err.Error(), "secret") {
		t.Fatalf("err = %v, want a refusal that does not echo the header", err)
	}
}

// A worker that stops answering must not hang the caller. Cancelling the
// context ends the call and, since one turn cannot be abandoned without losing
// the stream, the session.
func TestCancellingAStalledFeedReturnsAndEndsTheSession(t *testing.T) {
	repl := newRepl(t, func(s *fakeworker.Stream) {
		if s.RecvConfigure() == nil {
			return
		}
		s.Send(fakeworker.Ok())
		if s.RecvFeed() == nil {
			return
		}
		<-s.Context().Done() // never answers
	})

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err := repl.FeedRun(ctx, "stalls", monty.FeedOptions{})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v, want the context's deadline error", err)
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Fatalf("took %v: the call did not return when the context ended", elapsed)
	}

	// The session went with it.
	if _, err := feed(t, repl, "again", monty.FeedOptions{}); !errors.Is(err, monty.ErrClosed) {
		t.Fatalf("after cancelling a feed, err = %v, want the REPL to be closed", err)
	}
}

// Cancelling after a feed has finished must not touch the session.
func TestCancelledContextAfterTheFeedLeavesTheSessionAlone(t *testing.T) {
	repl := newRepl(t, serve(func(s *fakeworker.Stream, _ int, _ *montyv1.Feed) {
		s.Send(fakeworker.Complete(0, fakeworker.Int(6)))
	}))

	ctx, cancel := context.WithCancel(context.Background())
	got, err := repl.FeedRun(ctx, "x", monty.FeedOptions{})
	wantInt(t, got, err, 6)
	cancel() // after the call returned

	time.Sleep(50 * time.Millisecond) // let any stray cancellation land
	got, err = feed(t, repl, "x", monty.FeedOptions{})
	wantInt(t, got, err, 6)
}

// A context that has ended before the call fails without sending anything, and
// the session stays usable.
func TestAlreadyCancelledContextSendsNothing(t *testing.T) {
	feeds := 0
	var mu sync.Mutex
	repl := newRepl(t, serve(func(s *fakeworker.Stream, _ int, _ *montyv1.Feed) {
		mu.Lock()
		feeds++
		mu.Unlock()
		s.Send(fakeworker.Complete(0, fakeworker.Int(8)))
	}))

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := repl.FeedRun(ctx, "x", monty.FeedOptions{}); !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	got, err := feed(t, repl, "x", monty.FeedOptions{})
	wantInt(t, got, err, 8)

	mu.Lock()
	defer mu.Unlock()
	if feeds != 1 {
		t.Fatalf("the worker saw %d feeds, want 1", feeds)
	}
}

func TestOpeningASessionTimesOut(t *testing.T) {
	conn := fakeworker.Start(t, func(s *fakeworker.Stream) {
		s.RecvConfigure()
		<-s.Context().Done() // never replies to Configure
	})
	backend := remote.NewBackend(remote.NewGRPCTransport(conn, ""), remote.WithOpenTimeout(200*time.Millisecond))

	start := time.Now()
	_, err := monty.NewRepl(monty.ReplOptions{Backend: backend})
	if err == nil || !strings.Contains(err.Error(), "timed out") {
		t.Fatalf("err = %v, want a timeout", err)
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Fatalf("took %v: opening did not time out", elapsed)
	}
}

// The open timeout only covers opening. It must not end a session that opened
// in time.
func TestOpenTimeoutDoesNotEndAnOpenSession(t *testing.T) {
	conn := fakeworker.Start(t, serve(func(s *fakeworker.Stream, _ int, _ *montyv1.Feed) {
		s.Send(fakeworker.Complete(0, fakeworker.Int(2)))
	}))
	backend := remote.NewBackend(remote.NewGRPCTransport(conn, ""), remote.WithOpenTimeout(150*time.Millisecond))
	repl, err := monty.NewRepl(monty.ReplOptions{Backend: backend})
	if err != nil {
		t.Fatal(err)
	}

	time.Sleep(400 * time.Millisecond) // well past the open timeout
	got, err := feed(t, repl, "x", monty.FeedOptions{})
	wantInt(t, got, err, 2)
}

func str(s string) *string { return &s }

// A raised exception keeps its traceback, and TracebackString renders it the
// way a local monty.RuntimeError does.
func TestRaisedExceptionKeepsItsTraceback(t *testing.T) {
	repl := newRepl(t, serve(func(s *fakeworker.Stream, _ int, _ *montyv1.Feed) {
		s.Send(fakeworker.RaisedException(&montyv1.RaisedException{
			ExcType: "KeyError",
			Message: str("a"),
			Traceback: []*montyv1.StackFrame{
				{Filename: "<python-input-0>", Start: &montyv1.CodeLoc{Line: 3, Column: 1}, End: &montyv1.CodeLoc{Line: 3, Column: 6}, PreviewLine: str("f({})")},
				{Filename: "<python-input-0>", Start: &montyv1.CodeLoc{Line: 2, Column: 12}, End: &montyv1.CodeLoc{Line: 2, Column: 18}, FrameName: str("f"), PreviewLine: str("    return x['a']")},
				{Filename: "<python-input-0>", Start: &montyv1.CodeLoc{Line: 4, Column: 1}, End: &montyv1.CodeLoc{Line: 4, Column: 9}, PreviewLine: str("raise X"), HideCaret: true, HideFrameName: true},
			},
		}))
	}))
	_, err := feed(t, repl, "f({})", monty.FeedOptions{})
	var pyErr *remote.PythonError
	if !errors.As(err, &pyErr) {
		t.Fatalf("err = %v, want a *remote.PythonError", err)
	}
	if pyErr.Type != "KeyError" || pyErr.Message != "a" || len(pyErr.Frames) != 3 {
		t.Fatalf("got %+v", pyErr)
	}
	f := pyErr.Frames[1]
	if f.Line != 2 || f.Column != 12 || f.EndColumn != 18 || f.FunctionName == nil || *f.FunctionName != "f" || f.SourceLine == nil {
		t.Fatalf("frame 1 = %+v", f)
	}
	if pyErr.Frames[0].FunctionName != nil {
		t.Fatalf("a module-level frame has no function name, got %q", *pyErr.Frames[0].FunctionName)
	}
	want := `Traceback (most recent call last):
  File "<python-input-0>", line 3, in <module>
    f({})
    ~~~~~
  File "<python-input-0>", line 2, in f
    return x['a']
           ~~~~~~
  File "<python-input-0>", line 4
    raise X
KeyError: a`
	if got := pyErr.TracebackString(); got != want {
		t.Fatalf("TracebackString:\n%s\nwant:\n%s", got, want)
	}
}

func TestExceptionWithNoTracebackRendersJustTheException(t *testing.T) {
	repl := newRepl(t, serve(func(s *fakeworker.Stream, _ int, _ *montyv1.Feed) {
		s.Send(fakeworker.Raised("ValueError", "boom"))
	}))
	_, err := feed(t, repl, "x", monty.FeedOptions{})
	var pyErr *remote.PythonError
	if !errors.As(err, &pyErr) {
		t.Fatalf("err = %v", err)
	}
	if got := pyErr.TracebackString(); got != "ValueError: boom" {
		t.Fatalf("TracebackString = %q", got)
	}
}

func TestExceptionDataIsKept(t *testing.T) {
	doc := `{bad`
	repl := newRepl(t, serve(func(s *fakeworker.Stream, n int, _ *montyv1.Feed) {
		switch n {
		case 1:
			s.Send(fakeworker.RaisedException(&montyv1.RaisedException{
				ExcType: "UnicodeDecodeError",
				Message: str("'utf-8' codec can't decode byte 0xff in position 0: invalid start byte"),
				Data: &montyv1.ExcData{Kind: &montyv1.ExcData_Unicode{Unicode: &montyv1.UnicodeErrorData{
					Encoding: "utf-8",
					Object:   &montyv1.UnicodeErrorData_ObjectBytes{ObjectBytes: []byte{0xff}},
					Start:    0, End: 1, Reason: "invalid start byte",
				}}},
			}))
		case 2:
			s.Send(fakeworker.RaisedException(&montyv1.RaisedException{
				ExcType: "json.JSONDecodeError",
				Data: &montyv1.ExcData{Kind: &montyv1.ExcData_Json{Json: &montyv1.JsonErrorData{
					Msg: "Expecting property name enclosed in double quotes", Doc: &doc, Pos: 1, Lineno: 1, Colno: 2,
				}}},
			}))
		default:
			s.Send(fakeworker.Raised("ValueError", "plain"))
		}
	}))
	pyErr := func() *remote.PythonError {
		_, err := feed(t, repl, "x", monty.FeedOptions{})
		var pe *remote.PythonError
		if !errors.As(err, &pe) {
			t.Fatalf("err = %v", err)
		}
		return pe
	}

	u := pyErr().Unicode
	if u == nil || u.Encoding != "utf-8" || u.Start != 0 || u.End != 1 || u.Reason != "invalid start byte" || u.Object.Kind() != "bytes" {
		t.Fatalf("unicode data = %+v", u)
	}
	j := pyErr().JSON
	if j == nil || j.Msg == "" || j.Doc == nil || *j.Doc != doc || j.Pos != 1 || j.Lineno != 1 || j.Colno != 2 {
		t.Fatalf("json data = %+v", j)
	}
	plain := pyErr()
	if plain.Unicode != nil || plain.JSON != nil {
		t.Fatalf("a plain exception grew data: %+v", plain)
	}
}

// Output keeps its stream and its order, so a host can tell stderr from stdout.
func TestPrintStreamsAreKeptApart(t *testing.T) {
	repl := newRepl(t, serve(func(s *fakeworker.Stream, _ int, _ *montyv1.Feed) {
		s.Send(fakeworker.PrintedSegments(fakeworker.Stdout("one\n"), fakeworker.Stderr("warn\n"), fakeworker.Stdout("two\n")))
		s.Send(fakeworker.Printed("three\n"))
		s.Send(fakeworker.Complete(0, fakeworker.Int(1)))
	}))

	type run struct{ stream, text string }
	var got []run
	_, err := feed(t, repl, "x", monty.FeedOptions{Print: func(stream, text string) { got = append(got, run{stream, text}) }})
	if err != nil {
		t.Fatal(err)
	}
	want := []run{{"stdout", "one\n"}, {"stderr", "warn\n"}, {"stdout", "two\n"}, {"stdout", "three\n"}}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got %v, want %v", got, want)
		}
	}
}

// Output printed before a Python error still reaches the callback with its
// stream.
func TestStderrBeforeAnErrorIsDelivered(t *testing.T) {
	repl := newRepl(t, serve(func(s *fakeworker.Stream, _ int, _ *montyv1.Feed) {
		s.Send(fakeworker.PrintedSegments(fakeworker.Stderr("careful\n")))
		s.Send(fakeworker.Raised("ValueError", "boom"))
	}))
	var stderr, stdout string
	_, err := feed(t, repl, "x", monty.FeedOptions{Print: func(stream, text string) {
		if stream == "stderr" {
			stderr += text
		} else {
			stdout += text
		}
	}})
	if err == nil {
		t.Fatal("want an error")
	}
	if stderr != "careful\n" || stdout != "" {
		t.Fatalf("stderr = %q, stdout = %q", stderr, stdout)
	}
}

// Output reaches the print callback while the turn is still running, not when
// it ends: the worker holds the turn open until the host has seen the text.
func TestPrintIsDeliveredWhileTheTurnRuns(t *testing.T) {
	seen := make(chan struct{})
	var once sync.Once
	early := make(chan bool, 1)
	repl := newRepl(t, serve(func(s *fakeworker.Stream, _ int, _ *montyv1.Feed) {
		s.Send(fakeworker.PrintedSegments(fakeworker.Stdout("early\n"), fakeworker.Stderr("warn\n")))
		select {
		case <-seen:
			early <- true
		case <-time.After(3 * time.Second):
			early <- false
		}
		s.Send(fakeworker.Complete(0, fakeworker.Int(1)))
	}))

	var mu sync.Mutex
	var got []string
	_, err := feed(t, repl, "x", monty.FeedOptions{Print: func(stream, text string) {
		mu.Lock()
		got = append(got, stream+":"+text)
		mu.Unlock()
		once.Do(func() { close(seen) })
	}})
	if err != nil {
		t.Fatal(err)
	}
	if !<-early {
		t.Fatal("the host saw no output until the turn ended")
	}
	mu.Lock()
	defer mu.Unlock()
	if len(got) != 2 || got[0] != "stdout:early\n" || got[1] != "stderr:warn\n" {
		t.Fatalf("got %v, want each run exactly once, in order", got)
	}
}

// Output from before and after a host call is reported once each, in order,
// even though the turn is split by the call.
func TestStreamedPrintIsNotRepeatedAcrossAPause(t *testing.T) {
	repl := newRepl(t, serve(func(s *fakeworker.Stream, _ int, _ *montyv1.Feed) {
		s.Send(fakeworker.Printed("before\n"))
		s.Send(fakeworker.CallsHost("ping"))
		if s.RecvResumeCall() == nil {
			return
		}
		s.Send(fakeworker.PrintedSegments(fakeworker.Stderr("after\n")))
		s.Send(fakeworker.Complete(0, fakeworker.Int(1)))
	}))

	var got []string
	_, err := feed(t, repl, "x", monty.FeedOptions{
		Print: func(stream, text string) { got = append(got, stream+":"+text) },
		Functions: map[string]monty.ExternalFunction{
			"ping": func(context.Context, monty.Call) (monty.Result, error) { return monty.Return(monty.None()), nil },
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0] != "stdout:before\n" || got[1] != "stderr:after\n" {
		t.Fatalf("got %v", got)
	}
}

// connect returns a backend whose sessions are served by handler.
func connect(t *testing.T, handler fakeworker.Handler) *remote.Backend {
	t.Helper()
	conn := fakeworker.Start(t, handler)
	return remote.NewBackend(remote.NewGRPCTransport(conn, token))
}

// A dump is the worker's snapshot, and loading it opens a new session that
// starts from it.
func TestReplDumpAndLoadRoundTrip(t *testing.T) {
	var mu sync.Mutex
	streams := 0
	loaded := make(chan *montyv1.Configure, 1)
	backend := connect(t, func(s *fakeworker.Stream) {
		mu.Lock()
		streams++
		n := streams
		mu.Unlock()
		cfg := s.RecvConfigure()
		if cfg == nil {
			return
		}
		if n == 1 {
			if cfg.GetLoad() != nil {
				t.Errorf("a new session asked to load")
			}
			s.Send(fakeworker.Ok())
			if s.RecvDump() == nil {
				return
			}
			s.Send(fakeworker.Dumped([]byte("worker-state")))
			// The session carries on after a dump.
			if feed := s.RecvFeed(); feed != nil {
				s.Send(fakeworker.Complete(0, fakeworker.Int(1)))
			}
			return
		}
		loaded <- cfg
		s.Send(fakeworker.Restored("original.py"))
		if feed := s.RecvFeed(); feed != nil {
			s.Send(fakeworker.Complete(0, fakeworker.Int(42)))
		}
	})

	repl, err := monty.NewRepl(monty.ReplOptions{ScriptName: "original.py", Backend: backend})
	if err != nil {
		t.Fatal(err)
	}
	data, err := repl.Dump()
	if err != nil {
		t.Fatalf("Dump: %v", err)
	}
	got, err := feed(t, repl, "x", monty.FeedOptions{})
	wantInt(t, got, err, 1)

	restored, err := monty.LoadRepl(data, monty.ReplOptions{ScriptName: "original.py", Backend: backend})
	if err != nil {
		t.Fatalf("LoadRepl: %v", err)
	}
	cfg := <-loaded
	if string(cfg.GetLoad().GetState()) != "worker-state" {
		t.Fatalf("the worker was asked to load %q, want the dumped state", cfg.GetLoad().GetState())
	}
	if cfg.GetScriptName() != "original.py" || cfg.GetProtocolVersion() == 0 {
		t.Fatalf("configure = %v", cfg)
	}
	got, err = feed(t, restored, "x", monty.FeedOptions{})
	wantInt(t, got, err, 42)
}

// A worker with no session storage refuses a Dump; the session carries on.
func TestDumpRefusedByTheWorkerLeavesTheSessionUsable(t *testing.T) {
	repl := newRepl(t, func(s *fakeworker.Stream) {
		if s.RecvConfigure() == nil {
			return
		}
		s.Send(fakeworker.Ok())
		if s.RecvDump() == nil {
			return
		}
		s.Send(fakeworker.Raised("RuntimeError", "no session storage"))
		if feed := s.RecvFeed(); feed != nil {
			s.Send(fakeworker.Complete(0, fakeworker.Int(5)))
		}
	})
	_, err := repl.Dump()
	var pyErr *remote.PythonError
	if !errors.As(err, &pyErr) || pyErr.Message != "no session storage" {
		t.Fatalf("Dump err = %v, want the worker's refusal", err)
	}
	got, err := feed(t, repl, "x", monty.FeedOptions{})
	wantInt(t, got, err, 5)
}

func TestLoadRefusedByTheWorker(t *testing.T) {
	backend := connect(t, func(s *fakeworker.Stream) {
		if s.RecvConfigure() == nil {
			return
		}
		s.Send(fakeworker.Raised("RuntimeError", "unknown session"))
	})
	data := sealedDump(t, backend)
	_, err := monty.LoadRepl(data, monty.ReplOptions{Backend: backend})
	var pyErr *remote.PythonError
	if !errors.As(err, &pyErr) || pyErr.Message != "unknown session" {
		t.Fatalf("LoadRepl err = %v, want the worker's refusal", err)
	}
}

// sealedDump is a Dump taken from a throwaway worker, for tests that only need
// valid bytes to load.
func sealedDump(t *testing.T, _ *remote.Backend) []byte {
	t.Helper()
	repl := newRepl(t, func(s *fakeworker.Stream) {
		if s.RecvConfigure() == nil {
			return
		}
		s.Send(fakeworker.Ok())
		if s.RecvDump() != nil {
			s.Send(fakeworker.Dumped([]byte("state")))
		}
	})
	data, err := repl.Dump()
	if err != nil {
		t.Fatal(err)
	}
	return data
}

// A dump from the native library is not a remote dump, and the other way round.
func TestDumpsAreNotInterchangeableBetweenBackends(t *testing.T) {
	native, err := monty.NewRepl(monty.ReplOptions{})
	if err != nil {
		t.Skipf("no native library: %v", err)
	}
	nativeDump, err := native.Dump()
	if err != nil {
		t.Fatal(err)
	}
	backend := connect(t, func(s *fakeworker.Stream) {
		t.Errorf("a native dump reached the worker")
	})
	if _, err := monty.LoadRepl(nativeDump, monty.ReplOptions{Backend: backend}); !errors.Is(err, monty.ErrIncompatibleDump) {
		t.Fatalf("loading a native dump remotely: %v, want ErrIncompatibleDump", err)
	}

	remoteDump := sealedDump(t, nil)
	if _, err := monty.LoadRepl(remoteDump, monty.ReplOptions{}); err == nil {
		t.Fatal("the native library accepted a remote dump")
	}
}

// Reset drops the session's state on the worker and configures it again on the
// same stream.
func TestReplResetReconfiguresTheSession(t *testing.T) {
	var mu sync.Mutex
	streams := 0
	seen := make(chan *montyv1.Configure, 2)
	backend := connect(t, func(s *fakeworker.Stream) {
		mu.Lock()
		streams++
		mu.Unlock()
		cfg := s.RecvConfigure()
		if cfg == nil {
			return
		}
		seen <- cfg
		s.Send(fakeworker.Ok())
		if s.RecvFeed() == nil {
			return
		}
		s.Send(fakeworker.Complete(0, fakeworker.Int(1)))
		if s.RecvReset() == nil {
			return
		}
		s.Send(fakeworker.Ok())
		cfg = s.RecvConfigure()
		if cfg == nil {
			return
		}
		seen <- cfg
		s.Send(fakeworker.Ok())
		if s.RecvFeed() != nil {
			s.Send(fakeworker.Raised("NameError", "name 'x' is not defined"))
		}
	})
	repl, err := monty.NewRepl(monty.ReplOptions{ScriptName: "reset.py", Backend: backend})
	if err != nil {
		t.Fatal(err)
	}
	got, err := feed(t, repl, "x = 1", monty.FeedOptions{})
	wantInt(t, got, err, 1)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := repl.Reset(ctx); err != nil {
		t.Fatalf("Reset: %v", err)
	}
	if _, err := feed(t, repl, "x", monty.FeedOptions{}); err == nil {
		t.Fatal("x survived a reset")
	}

	first, second := <-seen, <-seen
	if second.GetScriptName() != first.GetScriptName() || second.GetLoad() != nil || second.GetFeed() != nil {
		t.Fatalf("the second Configure = %v, want the first without a first request", second)
	}
	mu.Lock()
	defer mu.Unlock()
	if streams != 1 {
		t.Fatalf("a reset opened %d streams, want the same one reused", streams)
	}
}

// A worker that will not accept the new Configure ends the session; the REPL
// then reports itself closed instead of pretending to be empty.
func TestResetRefusedEndsTheSession(t *testing.T) {
	repl := newRepl(t, func(s *fakeworker.Stream) {
		if s.RecvConfigure() == nil {
			return
		}
		s.Send(fakeworker.Ok())
		if s.RecvReset() == nil {
			return
		}
		s.Send(fakeworker.Ok())
		if s.RecvConfigure() == nil {
			return
		}
		s.Send(fakeworker.Refused("no more sessions"))
	})
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	err := repl.Reset(ctx)
	var refused *remote.SessionRefusedError
	if !errors.As(err, &refused) {
		t.Fatalf("Reset err = %v, want SessionRefusedError", err)
	}
	if _, err := feed(t, repl, "1", monty.FeedOptions{}); !errors.Is(err, monty.ErrClosed) {
		t.Fatalf("feeding after a failed reset: %v, want ErrClosed", err)
	}
}

// The options reach the worker's Configure, and an unset option stays unset so
// the worker's own default applies.
func TestConfigureCarriesTheOptions(t *testing.T) {
	seen := make(chan *montyv1.Configure, 2)
	handler := func(s *fakeworker.Stream) {
		cfg := s.RecvConfigure()
		if cfg == nil {
			return
		}
		seen <- cfg
		s.Send(fakeworker.Ok())
		s.Recv()
	}

	conn := fakeworker.Start(t, handler)
	plain := remote.NewBackend(remote.NewGRPCTransport(conn, token))
	if _, err := monty.NewRepl(monty.ReplOptions{ScriptName: "plain.py", Backend: plain}); err != nil {
		t.Fatal(err)
	}
	cfg := <-seen
	if cfg.GetTypeCheck() || cfg.Profile != nil || cfg.PrintFlushIntervalMs != nil ||
		cfg.GetPersistence() != montyv1.Persistence_PERSISTENCE_UNSPECIFIED {
		t.Fatalf("an unconfigured backend set options: %v", cfg)
	}

	configured := remote.NewBackend(remote.NewGRPCTransport(conn, token),
		remote.WithTypeCheck(remote.TypeCheckOptions{Stubs: "def f() -> int: ...", Format: remote.TypeCheckConcise, Color: true}),
		remote.WithProfile("sandbox"),
		remote.WithPersistence(remote.PersistenceStored),
		remote.WithPrintFlushInterval(0),
	)
	if _, err := monty.NewRepl(monty.ReplOptions{ScriptName: "configured.py", Backend: configured}); err != nil {
		t.Fatal(err)
	}
	cfg = <-seen
	if !cfg.GetTypeCheck() || cfg.GetTypeCheckStubs() != "def f() -> int: ..." || !cfg.GetTypeCheckColor() ||
		cfg.GetTypeCheckFormat() != montyv1.TypeCheckFormat_TYPE_CHECK_FORMAT_CONCISE {
		t.Fatalf("type check options = %v", cfg)
	}
	if cfg.GetProfile() != "sandbox" || cfg.GetPersistence() != montyv1.Persistence_PERSISTENCE_STORED {
		t.Fatalf("profile/persistence = %v", cfg)
	}
	if cfg.PrintFlushIntervalMs == nil || *cfg.PrintFlushIntervalMs != 0 {
		t.Fatalf("print flush interval = %v, want an explicit 0 (line buffering)", cfg.PrintFlushIntervalMs)
	}
}

func TestUnknownTypeCheckFormatIsRefusedBeforeConnecting(t *testing.T) {
	backend := remote.NewBackend(remote.NewGRPCTransport(fakeworker.Start(t, func(s *fakeworker.Stream) {
		t.Errorf("a stream was opened with an invalid format")
	}), token), remote.WithTypeCheck(remote.TypeCheckOptions{Format: "fancy"}))
	if _, err := monty.NewRepl(monty.ReplOptions{Backend: backend}); err == nil || !strings.Contains(err.Error(), "fancy") {
		t.Fatalf("err = %v, want the format named", err)
	}
}

// A feed can opt out of the session's type checking.
func TestFeedCanSkipTheTypeCheck(t *testing.T) {
	skips := make(chan bool, 2)
	repl := newRepl(t, serve(func(s *fakeworker.Stream, _ int, f *montyv1.Feed) {
		skips <- f.GetSkipTypeCheck()
		s.Send(fakeworker.Complete(0, fakeworker.Int(1)))
	}))
	if _, err := feed(t, repl, "x", monty.FeedOptions{}); err != nil {
		t.Fatal(err)
	}
	if _, err := feed(t, repl, "x", monty.FeedOptions{SkipTypeCheck: true}); err != nil {
		t.Fatal(err)
	}
	if first, second := <-skips, <-skips; first || !second {
		t.Fatalf("skip flags = %v, %v; want false then true", first, second)
	}
}

// What the worker reports about the session reaches telemetry: its in-sandbox
// times and its session id.
func TestWorkerStatsReachTelemetry(t *testing.T) {
	id := []byte{0xde, 0xad, 0xbe, 0xef}
	repl := newRepl(t, func(s *fakeworker.Stream) {
		if s.RecvConfigure() == nil {
			return
		}
		s.Send(fakeworker.WithSessionID(fakeworker.Ok(), id))
		for n := 1; ; n++ {
			if s.RecvFeed() == nil {
				return
			}
			s.Send(fakeworker.WithStats(fakeworker.Complete(0, fakeworker.Int(1)), uint64(n)*5000, 1500))
		}
	})

	var logs strings.Builder
	opts := monty.FeedOptions{Telemetry: monty.SlogHandler{
		Logger:       slog.New(slog.NewTextHandler(&logs, nil)),
		DurationUnit: monty.DurationMicroseconds,
	}}
	if _, err := feed(t, repl, "x", opts); err != nil {
		t.Fatal(err)
	}
	if _, err := feed(t, repl, "x", opts); err != nil {
		t.Fatal(err)
	}

	var finished []string
	for _, line := range strings.Split(logs.String(), "\n") {
		if strings.Contains(line, "execution finished") {
			finished = append(finished, line)
		}
	}
	if len(finished) != 2 {
		t.Fatalf("want 2 finished lines:\n%s", logs.String())
	}
	for i, want := range []string{"worker_session_duration_us=5000", "worker_session_duration_us=10000"} {
		for _, attr := range []string{"worker_feed_duration_us=1500", want, "worker_session_id=deadbeef"} {
			if !strings.Contains(finished[i], attr) {
				t.Errorf("feed %d lacks %s:\n%s", i+1, attr, finished[i])
			}
		}
	}
}

// A failing turn is reported too: the stats ride on the turn-ending event, not
// only on Complete.
func TestWorkerStatsAreReportedForAFailedFeed(t *testing.T) {
	repl := newRepl(t, serve(func(s *fakeworker.Stream, _ int, _ *montyv1.Feed) {
		s.Send(fakeworker.WithStats(fakeworker.Raised("ValueError", "boom"), 7000, 3000))
	}))
	var logs strings.Builder
	_, err := feed(t, repl, "x", monty.FeedOptions{Telemetry: monty.SlogHandler{
		Logger: slog.New(slog.NewTextHandler(&logs, nil)), DurationUnit: monty.DurationMicroseconds,
	}})
	if err == nil {
		t.Fatal("want an error")
	}
	if !strings.Contains(logs.String(), "execution failed") || !strings.Contains(logs.String(), "worker_feed_duration_us=3000") {
		t.Fatalf("the failed feed carries no worker stats:\n%s", logs.String())
	}
}

// The native library reports nothing, so its executions have no worker fields.
func TestNativeExecutionsHaveNoWorkerStats(t *testing.T) {
	repl, err := monty.NewRepl(monty.ReplOptions{})
	if err != nil {
		t.Skipf("no native library: %v", err)
	}
	var logs strings.Builder
	if _, err := feed(t, repl, "1 + 1", monty.FeedOptions{Telemetry: monty.SlogHandler{Logger: slog.New(slog.NewTextHandler(&logs, nil))}}); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(logs.String(), "worker_") {
		t.Fatalf("a native run reported worker stats:\n%s", logs.String())
	}
}
