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
	"github.com/mdfranz/gomonty/vfs"
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

// Dates are built by the worker and also sent to it, so both directions of the
// date, time and timedelta nodes are exercised.
func TestRealWorkerDateTimeValues(t *testing.T) {
	repl := realRepl(t)
	got, err := feed(t, repl, "import datetime\ndatetime.date(2026, 11, 12)", monty.FeedOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if d, ok := got.Date(); !ok || d != (monty.Date{Year: 2026, Month: 11, Day: 12}) {
		t.Fatalf("date = %s %s", got.Kind(), got)
	}

	got, err = feed(t, repl, "datetime.timedelta(days=1, seconds=2, microseconds=3)", monty.FeedOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if d, ok := got.TimeDelta(); !ok || d != (monty.TimeDelta{Days: 1, Seconds: 2, Microseconds: 3}) {
		t.Fatalf("timedelta = %s %s", got.Kind(), got)
	}

	got, err = feed(t, repl, "datetime.datetime(2026, 1, 2, 3, 4, 5, 6, tzinfo=datetime.timezone.utc)", monty.FeedOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if d, ok := got.DateTime(); !ok || d.Year != 2026 || d.Microsecond != 6 || d.OffsetSeconds == nil || *d.OffsetSeconds != 0 {
		t.Fatalf("datetime = %s %s", got.Kind(), got)
	}

	// A date sent as an input comes back out of Python arithmetic.
	got, err = feed(t, repl, "(when + datetime.timedelta(days=1)).day", monty.FeedOptions{
		Inputs: map[string]monty.Value{"when": monty.DateValue(monty.Date{Year: 2026, Month: 11, Day: 12})},
	})
	wantInt(t, got, err, 13)
}

func TestRealWorkerHostModule(t *testing.T) {
	repl := realRepl(t)
	got, err := feed(t, repl, "import hostmod\nhostmod.answer", monty.FeedOptions{
		OS: func(_ context.Context, call monty.OSCall) (monty.Result, error) {
			if call.Function != monty.OSImport {
				return monty.Result{}, errors.New("unexpected OS call " + string(call.Function))
			}
			return monty.Return(monty.ModuleValue(monty.Module{Name: "hostmod", Attrs: monty.Dict{
				{Key: monty.String("answer"), Value: monty.Int(42)},
			}})), nil
		},
	})
	wantInt(t, got, err, 42)
}

func TestRealWorkerPathStat(t *testing.T) {
	repl := realRepl(t)
	fs := vfs.NewMemoryFS()
	fs.AddText("/readme.txt", "hello")
	got, err := feed(t, repl, "from pathlib import Path\nPath('/readme.txt').stat().st_size", monty.FeedOptions{
		OS: vfs.Handler(fs, vfs.MapEnvironment{}),
	})
	wantInt(t, got, err, 5)
}
