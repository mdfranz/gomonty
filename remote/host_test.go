package remote_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	monty "github.com/mdfranz/gomonty"
	"github.com/mdfranz/gomonty/remote"
	"github.com/mdfranz/gomonty/remote/internal/fakeworker"
	montyv1 "github.com/mdfranz/gomonty/remote/internal/montyv1"
	"github.com/mdfranz/gomonty/vfs"
)

// echoOutcome ends a turn with whatever the client answered a pause with: a
// returned value comes back as the snippet's result, and an error as the
// exception it names.
func echoOutcome(s *fakeworker.Stream, answer *montyv1.ResumeCall) {
	switch k := answer.GetResult().GetKind().(type) {
	case *montyv1.ExtFunctionResult_ReturnValue:
		s.Send(fakeworker.CompleteWith(k.ReturnValue, answer.GetValues()))
	case *montyv1.ExtFunctionResult_Error:
		s.Send(fakeworker.Raised(k.Error.GetExcType(), k.Error.GetMessage()))
	case *montyv1.ExtFunctionResult_NotFound:
		s.Send(fakeworker.Raised("ModuleNotFoundError", k.NotFound))
	case *montyv1.ExtFunctionResult_NotHandled:
		s.Send(fakeworker.Raised("PermissionError", "not handled"))
	default:
		s.Send(fakeworker.Raised("AssertionError", fmt.Sprintf("unexpected answer %T", k)))
	}
}

func TestHostFunctionIsCalledAndAnswered(t *testing.T) {
	callIDs := make(chan uint32, 1)
	repl := newRepl(t, serve(func(s *fakeworker.Stream, _ int, _ *montyv1.Feed) {
		s.Send(fakeworker.FunctionCall("add", 7,
			[]uint32{0, 1},
			[]*montyv1.NodePair{{Key: 2, Value: 3}},
			fakeworker.Int(40), fakeworker.Int(2), fakeworker.Str("scale"), fakeworker.Int(2)))
		answer := s.RecvResumeCall()
		if answer == nil {
			return
		}
		callIDs <- answer.GetCallId()
		echoOutcome(s, answer)
	}))

	var seen monty.Call
	got, err := feed(t, repl, "add(40, 2, scale=2)", monty.FeedOptions{
		Functions: map[string]monty.ExternalFunction{
			"add": func(_ context.Context, call monty.Call) (monty.Result, error) {
				seen = call
				a, b := call.Args[0].Raw().(int64), call.Args[1].Raw().(int64)
				return monty.Return(monty.Int((a + b) * call.Kwargs[0].Value.Raw().(int64))), nil
			},
		},
	})
	wantInt(t, got, err, 84)

	if seen.FunctionName != "add" || seen.CallID != 7 || len(seen.Args) != 2 || len(seen.Kwargs) != 1 {
		t.Fatalf("host function saw %+v", seen)
	}
	if key, _ := seen.Kwargs[0].Key.Raw().(string); key != "scale" {
		t.Fatalf("keyword argument = %q, want scale", key)
	}
	if id := <-callIDs; id != 7 {
		t.Fatalf("the answer carried call id %d, want 7", id)
	}
}

func TestHostFunctionErrorReachesTheScript(t *testing.T) {
	repl := newRepl(t, serve(func(s *fakeworker.Stream, n int, _ *montyv1.Feed) {
		if n == 2 {
			s.Send(fakeworker.Complete(0, fakeworker.Int(1)))
			return
		}
		s.Send(fakeworker.CallsHost("boom"))
		if answer := s.RecvResumeCall(); answer != nil {
			echoOutcome(s, answer)
		}
	}))

	msg := "bad input"
	_, err := feed(t, repl, "boom()", monty.FeedOptions{
		Functions: map[string]monty.ExternalFunction{
			"boom": func(context.Context, monty.Call) (monty.Result, error) {
				return monty.Raise(monty.Exception{Type: "ValueError", Arg: &msg}), nil
			},
		},
	})
	var pyErr *remote.PythonError
	if !errors.As(err, &pyErr) || pyErr.Type != "ValueError" || pyErr.Message != "bad input" {
		t.Fatalf("err = %v, want the host's ValueError", err)
	}
	got, err := feed(t, repl, "ok", monty.FeedOptions{})
	wantInt(t, got, err, 1)
}

// A function the host never registered is answered, not left hanging.
func TestUnregisteredHostFunctionIsAnswered(t *testing.T) {
	repl := newRepl(t, serve(func(s *fakeworker.Stream, _ int, _ *montyv1.Feed) {
		s.Send(fakeworker.CallsHost("missing"))
		if answer := s.RecvResumeCall(); answer != nil {
			echoOutcome(s, answer)
		}
	}))
	_, err := feed(t, repl, "missing()", monty.FeedOptions{})
	var pyErr *remote.PythonError
	if !errors.As(err, &pyErr) {
		t.Fatalf("err = %v, want the error the host layer answered with", err)
	}
}

// OS calls reach the host's OSHandler, so a vfs filesystem works unchanged
// over the wire.
func TestOSCallsReachTheOSHandler(t *testing.T) {
	text := "dflt"
	defaultArena := &montyv1.Arena{NodeCount: 1, Nodes: []*montyv1.MontyNode{fakeworker.Str(text)}}
	call := func(c *montyv1.OsCall) *montyv1.OsCall { return c }

	cases := []struct {
		name     string
		call     *montyv1.OsCall
		want     monty.Value // compared by kind and text when set
		wantKind string      // otherwise just the kind
		wantErr  string      // the exception type the script should see
	}{
		{name: "exists", call: call(&montyv1.OsCall{Call: &montyv1.OsCall_Exists{Exists: "/notes.txt"}}), want: monty.Bool(true)},
		{name: "does not exist", call: call(&montyv1.OsCall{Call: &montyv1.OsCall_Exists{Exists: "/nope"}}), want: monty.Bool(false)},
		{name: "read text", call: call(&montyv1.OsCall{Call: &montyv1.OsCall_ReadText{ReadText: "/notes.txt"}}), want: monty.String("hello")},
		{name: "read bytes", call: call(&montyv1.OsCall{Call: &montyv1.OsCall_ReadBytes{ReadBytes: "/notes.txt"}}), want: monty.Bytes([]byte("hello"))},
		{name: "write text", call: call(&montyv1.OsCall{Call: &montyv1.OsCall_WriteText{WriteText: &montyv1.OsCall_TextWrite{Path: "/out.txt", Data: "data"}}}), wantKind: "int"},
		{name: "read what was written", call: call(&montyv1.OsCall{Call: &montyv1.OsCall_ReadText{ReadText: "/out.txt"}}), want: monty.String("data")},
		{name: "write bytes", call: call(&montyv1.OsCall{Call: &montyv1.OsCall_WriteBytes{WriteBytes: &montyv1.OsCall_BytesWrite{Path: "/b.bin", Data: []byte{1, 2}}}}), wantKind: "int"},
		{name: "mkdir with keywords", call: call(&montyv1.OsCall{Call: &montyv1.OsCall_Mkdir_{Mkdir: &montyv1.OsCall_Mkdir{Path: "/d", Parents: true, ExistOk: true}}}), want: monty.None()},
		{name: "is dir", call: call(&montyv1.OsCall{Call: &montyv1.OsCall_IsDir{IsDir: "/d"}}), want: monty.Bool(true)},
		{name: "rename", call: call(&montyv1.OsCall{Call: &montyv1.OsCall_Rename_{Rename: &montyv1.OsCall_Rename{Src: "/out.txt", Dst: "/moved.txt"}}}), want: monty.None()},
		{name: "renamed file exists", call: call(&montyv1.OsCall{Call: &montyv1.OsCall_Exists{Exists: "/moved.txt"}}), want: monty.Bool(true)},
		{name: "missing file", call: call(&montyv1.OsCall{Call: &montyv1.OsCall_ReadText{ReadText: "/nope"}}), wantErr: "FileNotFoundError"},
		{name: "getenv", call: call(&montyv1.OsCall{Call: &montyv1.OsCall_Getenv_{Getenv: &montyv1.OsCall_Getenv{Key: "HOME"}}, Values: defaultArena}), want: monty.String("/sandbox")},
		{name: "getenv default", call: call(&montyv1.OsCall{Call: &montyv1.OsCall_Getenv_{Getenv: &montyv1.OsCall_Getenv{Key: "MISSING", Default: 0}}, Values: defaultArena}), want: monty.String("dflt")},
		{name: "environ", call: call(&montyv1.OsCall{Call: &montyv1.OsCall_GetEnviron{GetEnviron: &montyv1.Unit{}}}), wantKind: "dict"},
		{name: "unknown import", call: call(&montyv1.OsCall{Call: &montyv1.OsCall_Import_{Import: &montyv1.OsCall_Import{Module: "nothing_here", SearchPath: []string{"/"}}}}), wantErr: "ModuleNotFoundError"},
		{name: "append is not handled", call: call(&montyv1.OsCall{Call: &montyv1.OsCall_AppendText{AppendText: &montyv1.OsCall_TextWrite{Path: "/notes.txt", Data: "x"}}}), wantErr: "PermissionError"},
	}

	repl := newRepl(t, serve(func(s *fakeworker.Stream, n int, _ *montyv1.Feed) {
		s.Send(fakeworker.OS(uint32(n), cases[n-1].call))
		if answer := s.RecvResumeCall(); answer != nil {
			echoOutcome(s, answer)
		}
	}))

	fs := vfs.NewMemoryFS()
	fs.AddText("/notes.txt", "hello")
	opts := monty.FeedOptions{OS: vfs.Handler(fs, vfs.MapEnvironment{"HOME": "/sandbox"})}

	for _, tc := range cases {
		got, err := feed(t, repl, "ignored", opts)
		switch {
		case tc.wantErr != "":
			var pyErr *remote.PythonError
			if !errors.As(err, &pyErr) || pyErr.Type != tc.wantErr {
				t.Errorf("%s: err = %v, want %s", tc.name, err, tc.wantErr)
			}
		case err != nil:
			t.Errorf("%s: %v", tc.name, err)
		case tc.want.Kind() != "" && (tc.wantKind == ""):
			if got.Kind() != tc.want.Kind() || got.String() != tc.want.String() {
				t.Errorf("%s: got %s %s, want %s %s", tc.name, got.Kind(), got, tc.want.Kind(), tc.want)
			}
		default:
			if string(got.Kind()) != tc.wantKind {
				t.Errorf("%s: got kind %s, want %s", tc.name, got.Kind(), tc.wantKind)
			}
		}
	}
}

// A blocking sleep is waited out by the backend, and never reaches the OS handler.
func TestSleepIsServedByTheBackend(t *testing.T) {
	waited := make(chan time.Duration, 1)
	repl := newRepl(t, serve(func(s *fakeworker.Stream, _ int, _ *montyv1.Feed) {
		start := time.Now()
		s.Send(fakeworker.OS(1, &montyv1.OsCall{Call: &montyv1.OsCall_SystemSleep{SystemSleep: &montyv1.OsCall_Sleep{Seconds: 0.2}}}))
		answer := s.RecvResumeCall()
		if answer == nil {
			return
		}
		waited <- time.Since(start)
		echoOutcome(s, answer)
	}))

	handlerCalled := false
	got, err := feed(t, repl, "time.sleep(0.2)", monty.FeedOptions{
		OS: func(context.Context, monty.OSCall) (monty.Result, error) {
			handlerCalled = true
			return monty.Return(monty.None()), nil
		},
	})
	if err != nil {
		t.Fatalf("FeedRun: %v", err)
	}
	if got.Kind() != "none" {
		t.Fatalf("result = %v, want None (a sleep returns None)", got)
	}
	if d := <-waited; d < 150*time.Millisecond {
		t.Fatalf("the sleep answer came after %v, want about 200ms", d)
	}
	if handlerCalled {
		t.Fatal("a blocking sleep was passed to the OS handler")
	}
}

func TestSleepEndsWithTheContext(t *testing.T) {
	repl := newRepl(t, serve(func(s *fakeworker.Stream, _ int, _ *montyv1.Feed) {
		s.Send(fakeworker.OS(1, &montyv1.OsCall{Call: &montyv1.OsCall_SystemSleep{SystemSleep: &montyv1.OsCall_Sleep{Seconds: 30}}}))
		s.Recv() // the client goes away instead of answering
	}))

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err := repl.FeedRun(ctx, "time.sleep(30)", monty.FeedOptions{})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v, want the context's deadline error", err)
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Fatalf("took %v: the sleep did not end with the context", elapsed)
	}
}

func TestNameLookups(t *testing.T) {
	type seen struct {
		foundIsFunction bool
		functionName    string
		missingWasUndef bool
	}
	result := make(chan seen, 1)
	repl := newRepl(t, serve(func(s *fakeworker.Stream, _ int, _ *montyv1.Feed) {
		var out seen
		s.Send(fakeworker.NameLookup("tool"))
		if found := s.RecvResumeNameLookup(); found != nil {
			if idx, ok := found.GetKind().(*montyv1.ResumeNameLookup_Value); ok {
				if fn := found.GetValues().GetNodes()[idx.Value].GetFunction(); fn != nil {
					out.foundIsFunction, out.functionName = true, fn.GetName()
				}
			}
		}
		s.Send(fakeworker.NameLookup("missing"))
		if missing := s.RecvResumeNameLookup(); missing != nil {
			_, out.missingWasUndef = missing.GetKind().(*montyv1.ResumeNameLookup_Undefined)
		}
		result <- out
		s.Send(fakeworker.Complete(0, fakeworker.Int(5)))
	}))

	got, err := feed(t, repl, "tool; missing", monty.FeedOptions{
		Functions: map[string]monty.ExternalFunction{
			"tool": func(context.Context, monty.Call) (monty.Result, error) { return monty.Return(monty.None()), nil },
		},
	})
	wantInt(t, got, err, 5)
	out := <-result
	if !out.foundIsFunction || out.functionName != "tool" {
		t.Errorf("a registered name should resolve to its host function, got %+v", out)
	}
	if !out.missingWasUndef {
		t.Error("an unknown name should be answered as undefined")
	}
}

type readyWaiter struct{ result monty.Result }

func (w readyWaiter) Wait(context.Context) monty.Result { return w.result }

// A host function that answers with a future is resolved when the worker waits
// on it.
func TestFuturesAreResolved(t *testing.T) {
	repl := newRepl(t, serve(func(s *fakeworker.Stream, _ int, _ *montyv1.Feed) {
		s.Send(fakeworker.CallsHost("later"))
		first := s.RecvResumeCall()
		if first == nil {
			return
		}
		if first.GetResult().GetFuture() != 1 {
			t.Errorf("a pending answer should name call 1 as the future, got %v", first.GetResult())
		}
		s.Send(fakeworker.ResolveFutures(1))
		resolved := s.RecvResumeFutures()
		if resolved == nil || len(resolved.GetResults()) != 1 {
			t.Errorf("want one resolved future, got %v", resolved)
			return
		}
		one := resolved.GetResults()[0]
		if one.GetCallId() != 1 {
			t.Errorf("resolved call %d, want 1", one.GetCallId())
		}
		s.Send(fakeworker.CompleteWith(one.GetResult().GetReturnValue(), resolved.GetValues()))
	}))

	got, err := feed(t, repl, "await later()", monty.FeedOptions{
		Functions: map[string]monty.ExternalFunction{
			"later": func(context.Context, monty.Call) (monty.Result, error) {
				return monty.Pending(readyWaiter{monty.Return(monty.Int(99))}), nil
			},
		},
	})
	wantInt(t, got, err, 99)
}

// A host function that fails with a Go error is reported to the script as a
// Python exception; the snippet is not abandoned.
func TestHostFunctionGoErrorBecomesAPythonException(t *testing.T) {
	answers := make(chan *montyv1.ResumeCall, 1)
	repl := newRepl(t, serve(func(s *fakeworker.Stream, n int, _ *montyv1.Feed) {
		if n == 2 {
			s.Send(fakeworker.Complete(0, fakeworker.Int(4)))
			return
		}
		s.Send(fakeworker.CallsHost("fails"))
		if answer := s.RecvResumeCall(); answer != nil {
			answers <- answer
			echoOutcome(s, answer)
		}
	}))

	_, err := feed(t, repl, "fails()", monty.FeedOptions{
		Functions: map[string]monty.ExternalFunction{
			"fails": func(context.Context, monty.Call) (monty.Result, error) {
				return monty.Result{}, errors.New("host failure")
			},
		},
	})
	var pyErr *remote.PythonError
	if !errors.As(err, &pyErr) || !strings.Contains(pyErr.Message, "host failure") {
		t.Fatalf("err = %v, want a Python exception carrying the host's message", err)
	}
	if answer := <-answers; answer.GetResult().GetError() == nil {
		t.Fatalf("the worker should have been answered with an error, got %v", answer.GetResult())
	}
	got, err := feed(t, repl, "next", monty.FeedOptions{})
	wantInt(t, got, err, 4)
}

// blockingWaiter never finishes: it returns only when the caller gives up.
type blockingWaiter struct{}

func (blockingWaiter) Wait(ctx context.Context) monty.Result {
	<-ctx.Done()
	return monty.Raise(monty.Exception{Type: "CancelledError"})
}

// When the caller gives up while the script is waiting on a future, the host
// layer abandons the snippet. That must end the feed on the worker, with an
// AbortFeed, and leave the session ready for the next snippet.
func TestAbandonedSnippetLeavesTheSessionUsable(t *testing.T) {
	sawAbort := make(chan bool, 1)
	repl := newRepl(t, serve(func(s *fakeworker.Stream, n int, _ *montyv1.Feed) {
		if n == 2 {
			s.Send(fakeworker.Complete(0, fakeworker.Int(4)))
			return
		}
		s.Send(fakeworker.CallsHost("later"))
		if s.RecvResumeCall() == nil {
			return
		}
		s.Send(fakeworker.ResolveFutures(1))
		req := s.Recv()
		if req == nil {
			return
		}
		sawAbort <- req.GetAbortFeed() != nil
		s.Send(fakeworker.Raised("RuntimeError", "abandoned"))
	}))

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	_, err := repl.FeedRun(ctx, "await later()", monty.FeedOptions{
		Functions: map[string]monty.ExternalFunction{
			"later": func(context.Context, monty.Call) (monty.Result, error) {
				return monty.Pending(blockingWaiter{}), nil
			},
		},
	})
	if err == nil {
		t.Fatal("FeedRun succeeded although the caller gave up")
	}
	if !<-sawAbort {
		t.Fatal("the worker was not told to abort the feed")
	}

	// The session is intact: another snippet runs on it.
	got, err := feed(t, repl, "next", monty.FeedOptions{})
	wantInt(t, got, err, 4)
}

// A call on a host object cannot be routed yet. The feed is aborted, so the
// session survives, and the caller gets an error naming the cause.
func TestMethodCallsOnHostObjectsAreAborted(t *testing.T) {
	repl := newRepl(t, serve(func(s *fakeworker.Stream, n int, _ *montyv1.Feed) {
		if n == 2 {
			s.Send(fakeworker.Complete(0, fakeworker.Int(9)))
			return
		}
		s.Send(fakeworker.MethodCall("run", 1))
		if s.RecvAbortFeed() != nil {
			s.Send(fakeworker.Raised("RuntimeError", "aborted"))
		}
	}))
	_, err := feed(t, repl, "obj.run()", monty.FeedOptions{})
	if !errors.Is(err, remote.ErrUnsupported) || !strings.Contains(err.Error(), "host object") {
		t.Fatalf("err = %v, want ErrUnsupported naming the host object", err)
	}
	got, err := feed(t, repl, "1", monty.FeedOptions{})
	wantInt(t, got, err, 9)
}

// An undefined name arrives as a name lookup and then raises NameError on
// resume. The error must be the Python exception alone, with no "handle is
// closed" error joined on by the dispatcher's cleanup.
func TestUndefinedNameErrorIsNotJoinedWithClosedHandle(t *testing.T) {
	repl := newRepl(t, serve(func(s *fakeworker.Stream, _ int, _ *montyv1.Feed) {
		s.Send(fakeworker.NameLookup("foo"))
		s.RecvResumeNameLookup()
		s.Send(fakeworker.Raised("NameError", "name 'foo' is not defined"))
	}))
	_, err := feed(t, repl, "print(foo)", monty.FeedOptions{})
	if err == nil || err.Error() != "NameError: name 'foo' is not defined" {
		t.Fatalf("err = %q, want only the NameError", err)
	}
	if errors.Is(err, monty.ErrClosed) {
		t.Fatalf("err wraps ErrClosed: %v", err)
	}
}

// A host module and a stat result travel to the worker as arena nodes: the
// import handler's Module and vfs's Path.stat NamedTuple.
func TestModuleAndStatResultReachTheWorker(t *testing.T) {
	calls := []*montyv1.OsCall{
		{Call: &montyv1.OsCall_Import_{Import: &montyv1.OsCall_Import{Module: "hostmod", SearchPath: []string{"/"}}}},
		{Call: &montyv1.OsCall_Stat{Stat: "/readme.txt"}},
	}
	repl := newRepl(t, serve(func(s *fakeworker.Stream, n int, _ *montyv1.Feed) {
		s.Send(fakeworker.OS(uint32(n), calls[n-1]))
		if answer := s.RecvResumeCall(); answer != nil {
			echoOutcome(s, answer)
		}
	}))

	fs := vfs.NewMemoryFS()
	fs.AddText("/readme.txt", "hello")
	stat := vfs.Handler(fs, vfs.MapEnvironment{})
	opts := monty.FeedOptions{OS: func(ctx context.Context, call monty.OSCall) (monty.Result, error) {
		if call.Function == monty.OSImport {
			return monty.Return(monty.ModuleValue(monty.Module{Name: "hostmod", Attrs: monty.Dict{
				{Key: monty.String("answer"), Value: monty.Int(42)},
			}})), nil
		}
		return stat(ctx, call)
	}}

	got, err := feed(t, repl, "import hostmod", opts)
	if err != nil {
		t.Fatalf("import: %v", err)
	}
	mod, ok := got.Module()
	if !ok || mod.Name != "hostmod" || len(mod.Attrs) != 1 || mod.ID == "" {
		t.Fatalf("import returned %s %s", got.Kind(), got)
	}

	got, err = feed(t, repl, "Path('/readme.txt').stat()", opts)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	nt, ok := got.Raw().(monty.NamedTuple)
	if !ok || len(nt.FieldNames) != len(nt.Values) || len(nt.Values) == 0 {
		t.Fatalf("stat returned %s %s", got.Kind(), got)
	}
}
