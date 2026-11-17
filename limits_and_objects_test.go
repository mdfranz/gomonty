package monty

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestMaxSuspensionsEnforcedRunner(t *testing.T) {
	calls := 0
	runner, err := New(
		"try:\n    for i in range(50):\n        ext()\nexcept BaseException:\n    'caught'",
		CompileOptions{ScriptName: "susp_runner.py"},
	)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer closeTestRunner(runner)

	_, err = runner.Run(context.Background(), RunOptions{
		Limits: &ResourceLimits{MaxSuspensions: 5},
		Functions: map[string]ExternalFunction{
			"ext": func(context.Context, Call) (Result, error) {
				calls++
				return Return(Int(int64(calls))), nil
			},
		},
	})
	var rtErr *RuntimeError
	if !errors.As(err, &rtErr) {
		t.Fatalf("err = %v (%T), want *RuntimeError", err, err)
	}
	if !strings.Contains(rtErr.Error(), "suspension limit 5 exceeded") {
		t.Fatalf("error = %q, want substring %q", rtErr.Error(), "suspension limit 5 exceeded")
	}
	if calls != 5 {
		t.Fatalf("host calls serviced = %d, want 5 before aborting on 6th suspension", calls)
	}
}

func TestMaxSuspensionsDefaultEnforced(t *testing.T) {
	calls := 0
	runner, err := New(
		"for i in range(1005):\n    ext()\n'done'",
		CompileOptions{ScriptName: "susp_default.py"},
	)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer closeTestRunner(runner)

	_, err = runner.Run(context.Background(), RunOptions{
		Functions: map[string]ExternalFunction{
			"ext": func(context.Context, Call) (Result, error) {
				calls++
				return Return(Int(int64(calls))), nil
			},
		},
	})
	var rtErr *RuntimeError
	if !errors.As(err, &rtErr) {
		t.Fatalf("err = %v (%T), want *RuntimeError", err, err)
	}
	if !strings.Contains(rtErr.Error(), "suspension limit 1000 exceeded") {
		t.Fatalf("error = %q, want substring %q", rtErr.Error(), "suspension limit 1000 exceeded")
	}
	if calls != 1000 {
		t.Fatalf("host calls serviced = %d, want 1000", calls)
	}
}

func TestMaxSuspensionsEnforcedAcrossLookupAndFutures(t *testing.T) {
	// Assigning the function reference triggers:
	//   1) NameLookup (`ext_async`),
	//   2) FunctionCall (`f()`),
	//   3) ResolveFutures (`await`).
	code := "async def main():\n    f = ext_async\n    return await f()\nawait main()"
	runner, err := New(code, CompileOptions{ScriptName: "susp_kinds.py"})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer closeTestRunner(runner)

	fns := map[string]ExternalFunction{
		"ext_async": func(context.Context, Call) (Result, error) {
			return Pending(funcWaiter(func(context.Context) Result {
				return Return(Int(7))
			})), nil
		},
	}

	got, err := runner.Run(context.Background(), RunOptions{
		Limits:    &ResourceLimits{MaxSuspensions: 3},
		Functions: fns,
	})
	if err != nil || got.Raw() != int64(7) {
		t.Fatalf("MaxSuspensions=3 got = %v, %v; want 7", got.Raw(), err)
	}

	_, err = runner.Run(context.Background(), RunOptions{
		Limits:    &ResourceLimits{MaxSuspensions: 2},
		Functions: fns,
	})
	var rtErr *RuntimeError
	if !errors.As(err, &rtErr) {
		t.Fatalf("err = %v (%T), want *RuntimeError", err, err)
	}
	if !strings.Contains(rtErr.Error(), "suspension limit 2 exceeded") {
		t.Fatalf("error = %q, want substring %q", rtErr.Error(), "suspension limit 2 exceeded")
	}
}

type funcWaiter func(context.Context) Result

func (f funcWaiter) Wait(ctx context.Context) Result {
	return f(ctx)
}

func TestMaxSuspensionsEnforcedReplAndReset(t *testing.T) {
	ctx := context.Background()
	repl, err := NewRepl(ReplOptions{
		ScriptName: "susp_repl.py",
		Limits:     &ResourceLimits{MaxSuspensions: 3},
	})
	if err != nil {
		t.Fatalf("NewRepl: %v", err)
	}
	defer closeTestRepl(repl)

	calls := 0
	opts := FeedOptions{
		Functions: map[string]ExternalFunction{
			"ext": func(context.Context, Call) (Result, error) {
				calls++
				return Return(Int(int64(calls))), nil
			},
		},
	}

	if _, err := repl.FeedRun(ctx, "x = 42", opts); err != nil {
		t.Fatalf("seed global: %v", err)
	}
	// Two calls consume 2 suspensions from the session budget.
	if got, err := repl.FeedRun(ctx, "ext() + ext()", opts); err != nil || got.Raw() != int64(3) {
		t.Fatalf("first 2 calls = %v, %v; want 3", got.Raw(), err)
	}
	// Next feed asks for 2 more calls: 3rd succeeds, 4th aborts.
	_, err = repl.FeedRun(ctx, "ext() + ext()", opts)
	var rtErr *RuntimeError
	if !errors.As(err, &rtErr) || !strings.Contains(rtErr.Error(), "suspension limit 3 exceeded") {
		t.Fatalf("over-budget FeedRun err = %v, want suspension limit 3 exceeded", err)
	}
	if calls != 3 {
		t.Fatalf("calls = %d, want 3", calls)
	}

	// Prior REPL bindings stay intact and non-suspending feeds still work.
	got, err := repl.FeedRun(ctx, "x", opts)
	if err != nil || got.Raw() != int64(42) {
		t.Fatalf("post-abort x = %v, %v; want 42", got.Raw(), err)
	}

	// Resetting clears the session's suspension count.
	if err := repl.Reset(ctx); err != nil {
		t.Fatalf("Reset: %v", err)
	}
	calls = 0
	if got, err := repl.FeedRun(ctx, "ext() + ext() + ext()", opts); err != nil || got.Raw() != int64(6) {
		t.Fatalf("post-reset 3 calls = %v, %v; want 6", got.Raw(), err)
	}
}

func TestMaxSuspensionsPreservedAcrossSnapshotDumpLoad(t *testing.T) {
	ctx := context.Background()
	runner, err := New(
		"a = ext(1)\nb = ext(2)\nc = ext(3)\nd = ext(4)\na + b + c + d",
		CompileOptions{ScriptName: "dump_susp.py"},
	)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer closeTestRunner(runner)

	p1, err := runner.Start(ctx, StartOptions{Limits: &ResourceLimits{MaxSuspensions: 3}})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	s1 := p1.(*Snapshot)
	p2, err := s1.ResumeReturn(ctx, Int(10))
	if err != nil {
		t.Fatalf("ResumeReturn #1: %v", err)
	}
	s2 := p2.(*Snapshot)

	// Dump after 2 suspensions, reload, then allow #3 and expect abort on #4.
	dumpData, err := s2.Dump()
	closeTestProgress(s2)
	if err != nil {
		t.Fatalf("Snapshot.Dump: %v", err)
	}

	reloaded, err := LoadSnapshot(dumpData)
	if err != nil {
		t.Fatalf("LoadSnapshot: %v", err)
	}
	s2Loaded := reloaded.(*Snapshot)
	p3, err := s2Loaded.ResumeReturn(ctx, Int(20))
	if err != nil {
		t.Fatalf("ResumeReturn #2: %v", err)
	}
	s3 := p3.(*Snapshot)

	p4, err := s3.ResumeReturn(ctx, Int(30))
	if p4 != nil {
		closeTestProgress(p4)
	}
	var rtErr *RuntimeError
	if !errors.As(err, &rtErr) || !strings.Contains(rtErr.Error(), "suspension limit 3 exceeded") {
		t.Fatalf("ResumeReturn #3 err = %v, want suspension limit 3 exceeded", err)
	}
}

func TestProgressAbortUncatchable(t *testing.T) {
	ctx := context.Background()
	msg := "aborted from go"

	t.Run("runner snapshot abort is uncatchable", func(t *testing.T) {
		runner, err := New(
			"try:\n    ext()\nexcept BaseException:\n    'caught'",
			CompileOptions{ScriptName: "abort_runner.py"},
		)
		if err != nil {
			t.Fatalf("New: %v", err)
		}
		defer closeTestRunner(runner)

		p, err := runner.Start(ctx, StartOptions{})
		if err != nil {
			t.Fatalf("Start: %v", err)
		}
		snap := p.(*Snapshot)
		err = snap.Abort(ctx, Exception{Type: "RuntimeError", Arg: &msg})
		if err == nil || !strings.Contains(err.Error(), msg) {
			t.Fatalf("Abort err = %v, want %q", err, msg)
		}
		if err := snap.Abort(ctx, Exception{Type: "RuntimeError", Arg: &msg}); !errors.Is(err, ErrClosed) {
			t.Fatalf("second Abort err = %v, want ErrClosed", err)
		}
	})

	t.Run("repl name lookup and future abort preserve repl owner", func(t *testing.T) {
		repl, err := NewRepl(ReplOptions{ScriptName: "abort_repl.py"})
		if err != nil {
			t.Fatalf("NewRepl: %v", err)
		}
		defer closeTestRepl(repl)

		if _, err := repl.FeedRun(ctx, "keep = 99", FeedOptions{}); err != nil {
			t.Fatalf("setup: %v", err)
		}

		pLookup, err := repl.FeedStart(ctx, "try:\n    missing_name\nexcept BaseException:\n    'caught'", FeedStartOptions{})
		if err != nil {
			t.Fatalf("FeedStart lookup: %v", err)
		}
		lookup := pLookup.(*NameLookupSnapshot)
		if err := lookup.Abort(ctx, Exception{Type: "RuntimeError", Arg: &msg}); err == nil || !strings.Contains(err.Error(), msg) {
			t.Fatalf("lookup.Abort err = %v, want %q", err, msg)
		}

		got, err := repl.FeedRun(ctx, "keep", FeedOptions{})
		if err != nil || got.Raw() != int64(99) {
			t.Fatalf("post-lookup-abort keep = %v, %v; want 99", got.Raw(), err)
		}

		pCall, err := repl.FeedStart(ctx, "try:\n    await ext()\nexcept BaseException:\n    'caught'", FeedStartOptions{})
		if err != nil {
			t.Fatalf("FeedStart async: %v", err)
		}
		pFuture, err := pCall.(*Snapshot).ResumePending(ctx)
		if err != nil {
			t.Fatalf("ResumePending: %v", err)
		}
		if err := pFuture.(*FutureSnapshot).Abort(ctx, Exception{Type: "RuntimeError", Arg: &msg}); err == nil || !strings.Contains(err.Error(), msg) {
			t.Fatalf("future.Abort err = %v, want %q", err, msg)
		}

		got, err = repl.FeedRun(ctx, "keep", FeedOptions{})
		if err != nil || got.Raw() != int64(99) {
			t.Fatalf("post-future-abort keep = %v, %v; want 99", got.Raw(), err)
		}
	})
}

func TestHostObjectMethodAndAttrDoNotFallThroughToGlobalFunctions(t *testing.T) {
	ctx := context.Background()
	hostObj := ClassInstanceValue(ClassInstance{
		ClassType: ClassType{
			Name:        "Doc",
			ID:          "12345678-9abc-4def-8123-456789abcdef",
			HostDefined: true,
		},
		InstanceID: "fedcba98-7654-4321-8fed-cba987654321",
	})

	globalCalls := 0
	fns := map[string]ExternalFunction{
		"save": func(context.Context, Call) (Result, error) {
			globalCalls++
			return Return(String("global-save")), nil
		},
	}

	t.Run("runner blocks host method call and attr lookup while preserving global save", func(t *testing.T) {
		runner, err := New(
			`[hasattr(obj, "save"), save()]`,
			CompileOptions{ScriptName: "hostobj_attr.py", Inputs: []string{"obj"}},
		)
		if err != nil {
			t.Fatalf("New: %v", err)
		}
		defer closeTestRunner(runner)

		globalCalls = 0
		res, err := runner.Run(ctx, RunOptions{
			Inputs:    map[string]Value{"obj": hostObj},
			Functions: fns,
		})
		if err != nil {
			t.Fatalf("Run: %v", err)
		}
		items, ok := res.Raw().([]Value)
		if !ok || len(items) != 2 {
			t.Fatalf("res = %#v, want 2-element list", res.Raw())
		}
		if items[0].Raw() != false {
			t.Fatalf("hasattr(obj, \"save\") = %#v, want false (must not fall back to global function)", items[0].Raw())
		}
		if items[1].Raw() != "global-save" || globalCalls != 1 {
			t.Fatalf("save() = %#v (globalCalls=%d), want global-save (1)", items[1].Raw(), globalCalls)
		}
	})

	t.Run("host method call raises NotImplementedError without invoking global save", func(t *testing.T) {
		runner, err := New(
			"obj.save()",
			CompileOptions{ScriptName: "hostobj_method.py", Inputs: []string{"obj"}},
		)
		if err != nil {
			t.Fatalf("New: %v", err)
		}
		defer closeTestRunner(runner)

		globalCalls = 0
		_, err = runner.Run(ctx, RunOptions{
			Inputs:    map[string]Value{"obj": hostObj},
			Functions: fns,
		})
		var rtErr *RuntimeError
		if !errors.As(err, &rtErr) || !strings.Contains(rtErr.Error(), "NotImplementedError") {
			t.Fatalf("err = %v, want NotImplementedError", err)
		}
		if globalCalls != 0 {
			t.Fatalf("global save invoked %d times on obj.save(), want 0", globalCalls)
		}
	})

	t.Run("repl host attribute access raises AttributeError without invoking global save", func(t *testing.T) {
		repl, err := NewRepl(ReplOptions{ScriptName: "hostobj_repl.py"})
		if err != nil {
			t.Fatalf("NewRepl: %v", err)
		}
		defer closeTestRepl(repl)

		if _, err := repl.FeedRun(ctx, "bound = obj", FeedOptions{Inputs: map[string]Value{"obj": hostObj}}); err != nil {
			t.Fatalf("setup: %v", err)
		}
		_, err = repl.FeedRun(ctx, "bound.save", FeedOptions{Functions: fns})
		var rtErr *RuntimeError
		if !errors.As(err, &rtErr) || !strings.Contains(rtErr.Error(), "AttributeError") {
			t.Fatalf("bound.save err = %v, want AttributeError", err)
		}
	})
}
