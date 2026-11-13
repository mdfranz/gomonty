package remote_test

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	monty "github.com/mdfranz/gomonty"
	"github.com/mdfranz/gomonty/remote"
)

// These tests drive a real Monty worker instead of the in-process fake. They
// skip unless MONTY_REMOTE_ENDPOINT is set, so CI is unaffected:
//
//	MONTY_REMOTE_ENDPOINT   host:port of a worker serving monty.v1.MontySession
//	MONTY_REMOTE_INSECURE   "1" to dial without TLS (local relay; no token)
//	MONTY_REMOTE_TOKEN      bearer token, sent only over TLS
//
// A local worker is scripts/grpc_relay.py from pydantic/monty in front of
// `monty subprocess`.
func realRepl(t *testing.T) *monty.Repl {
	t.Helper()
	endpoint := os.Getenv("MONTY_REMOTE_ENDPOINT")
	if endpoint == "" {
		t.Skip("MONTY_REMOTE_ENDPOINT is not set")
	}
	transport, err := remote.DialGRPC(endpoint, remote.GRPCConfig{
		Insecure: os.Getenv("MONTY_REMOTE_INSECURE") == "1",
		Token:    os.Getenv("MONTY_REMOTE_TOKEN"),
	})
	if err != nil {
		t.Fatalf("DialGRPC: %v", err)
	}
	t.Cleanup(func() { transport.Close() })
	repl, err := monty.NewRepl(monty.ReplOptions{ScriptName: "real_worker_test.py", Backend: remote.NewBackend(transport)})
	if err != nil {
		t.Fatalf("NewRepl: %v", err)
	}
	return repl
}

func TestRealWorkerStatePersistsAcrossFeeds(t *testing.T) {
	repl := realRepl(t)
	if _, err := feed(t, repl, "x = 40", monty.FeedOptions{}); err != nil {
		t.Fatal(err)
	}
	got, err := feed(t, repl, "x + 2", monty.FeedOptions{})
	wantInt(t, got, err, 42)
}

func TestRealWorkerPythonErrorLeavesSessionUsable(t *testing.T) {
	repl := realRepl(t)
	if _, err := feed(t, repl, "1 / 0", monty.FeedOptions{}); err == nil {
		t.Fatal("1 / 0 succeeded")
	}
	got, err := feed(t, repl, "1 + 1", monty.FeedOptions{})
	wantInt(t, got, err, 2)
}

func TestRealWorkerHostFunction(t *testing.T) {
	repl := realRepl(t)
	got, err := feed(t, repl, "add(40, 2)", monty.FeedOptions{
		Functions: map[string]monty.ExternalFunction{
			"add": func(_ context.Context, call monty.Call) (monty.Result, error) {
				return monty.Return(monty.Int(call.Args[0].Raw().(int64) + call.Args[1].Raw().(int64))), nil
			},
		},
	})
	wantInt(t, got, err, 42)
}

func TestRealWorkerHostFunctionErrorReachesScript(t *testing.T) {
	repl := realRepl(t)
	got, err := feed(t, repl, "try:\n    boom()\nexcept ValueError:\n    r = 7\nr", monty.FeedOptions{
		Functions: map[string]monty.ExternalFunction{
			"boom": func(context.Context, monty.Call) (monty.Result, error) {
				return monty.Raise(monty.Exception{Type: "ValueError"}), nil
			},
		},
	})
	wantInt(t, got, err, 7)
}

func TestRealWorkerOSCall(t *testing.T) {
	repl := realRepl(t)
	var seen monty.OSCall
	got, err := feed(t, repl, "import os\nos.getenv('GREETING', 'none')", monty.FeedOptions{
		OS: func(_ context.Context, call monty.OSCall) (monty.Result, error) {
			seen = call
			return monty.Return(monty.String("hello")), nil
		},
	})
	if err != nil {
		t.Fatalf("FeedRun: %v (OS call seen: %+v)", err, seen)
	}
	if got.Raw() != "hello" {
		t.Fatalf("result = %v (OS call seen: %+v), want hello", got, seen)
	}
}

func TestRealWorkerSleep(t *testing.T) {
	repl := realRepl(t)
	start := time.Now()
	if _, err := feed(t, repl, "import time\ntime.sleep(0.2)", monty.FeedOptions{
		OS: func(context.Context, monty.OSCall) (monty.Result, error) { return monty.Return(monty.None()), nil },
	}); err != nil {
		t.Fatalf("FeedRun: %v", err)
	}
	if d := time.Since(start); d < 150*time.Millisecond {
		t.Fatalf("sleep took %v, want about 200ms", d)
	}
}

func TestRealWorkerCancellationEndsTheSession(t *testing.T) {
	repl := realRepl(t)
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err := repl.FeedRun(ctx, "while True:\n    pass", monty.FeedOptions{})
	// A worker without a feed time limit lets the client's deadline fire. A
	// worker that enforces its own limit (such as a hosted one) ends the feed
	// first and reports a Python TimeoutError. Either way the feed must stop.
	if !errors.Is(err, context.DeadlineExceeded) && !(err != nil && strings.Contains(err.Error(), "TimeoutError")) {
		t.Fatalf("err = %v, want the context's deadline error or a worker TimeoutError", err)
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Fatalf("took %v to return after cancel", elapsed)
	}
	if errors.Is(err, context.DeadlineExceeded) {
		// Cancelling from the client tears the session down. A worker-side
		// TimeoutError is an ordinary Python error and leaves it usable.
		if _, err := feed(t, repl, "1", monty.FeedOptions{}); !errors.Is(err, monty.ErrClosed) {
			t.Fatalf("after cancel, err = %v, want ErrClosed", err)
		}
	}
}

func TestRealWorkerNameLookup(t *testing.T) {
	repl := realRepl(t)
	got, err := feed(t, repl, "try:\n    missing\nexcept NameError:\n    r = 1\nelse:\n    r = 0\ntool is not None and r", monty.FeedOptions{
		Functions: map[string]monty.ExternalFunction{
			"tool": func(context.Context, monty.Call) (monty.Result, error) { return monty.Return(monty.None()), nil },
		},
	})
	wantInt(t, got, err, 1)
}

func TestRealWorkerFutures(t *testing.T) {
	repl := realRepl(t)
	got, err := feed(t, repl, "import asyncio\nasync def main():\n    a, b = await asyncio.gather(later(1), later(2))\n    return a + b\nawait main()", monty.FeedOptions{
		Functions: map[string]monty.ExternalFunction{
			"later": func(_ context.Context, call monty.Call) (monty.Result, error) {
				return monty.Pending(readyWaiter{monty.Return(call.Args[0])}), nil
			},
		},
	})
	wantInt(t, got, err, 3)
}

func TestRealWorkerLargeValueRoundTrip(t *testing.T) {
	repl := realRepl(t)
	got, err := feed(t, repl, "len(echo(list(range(50000))))", monty.FeedOptions{
		Functions: map[string]monty.ExternalFunction{
			"echo": func(_ context.Context, call monty.Call) (monty.Result, error) { return monty.Return(call.Args[0]), nil },
		},
	})
	wantInt(t, got, err, 50000)
}
