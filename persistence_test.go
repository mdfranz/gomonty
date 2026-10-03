package monty

import (
	"context"
	"testing"
)

// These tests guard the serialized forms (Runner.Dump, Snapshot.Dump,
// NameLookupSnapshot.Dump, FutureSnapshot.Dump, REPL snapshots). The FFI wraps
// upstream Monty's internal types and serializes them with postcard, so an
// upstream serde change can silently break reloading persisted state; a
// dump -> load -> resume cycle per progress kind catches that.

func mustComplete(t *testing.T, progress Progress, want int64) {
	t.Helper()
	complete, ok := progress.(*Complete)
	if !ok {
		closeTestProgress(progress)
		t.Fatalf("progress is %T, want *Complete", progress)
	}
	got, ok := complete.Output.Raw().(int64)
	if !ok || got != want {
		t.Fatalf("output = %#v, want int64 %d", complete.Output.Raw(), want)
	}
}

func TestRunnerDumpLoadRoundTrip(t *testing.T) {
	runner, err := New("x * 2 + 1", CompileOptions{ScriptName: "dump.py", Inputs: []string{"x"}})
	if err != nil {
		t.Fatal(err)
	}
	defer closeTestRunner(runner)

	data, err := runner.Dump()
	if err != nil {
		t.Fatal(err)
	}
	loaded, err := LoadRunner(data)
	if err != nil {
		t.Fatalf("LoadRunner: %v", err)
	}
	defer closeTestRunner(loaded)

	for name, r := range map[string]*Runner{"original": runner, "loaded": loaded} {
		got, err := r.Run(context.Background(), RunOptions{Inputs: map[string]Value{"x": Int(21)}})
		if err != nil {
			t.Fatalf("%s Run: %v", name, err)
		}
		if got.Raw() != int64(43) {
			t.Fatalf("%s result = %#v, want 43", name, got.Raw())
		}
	}
}

func TestCallSnapshotDumpLoadResume(t *testing.T) {
	runner, err := New("a = ext(1, 2)\na + 10", CompileOptions{ScriptName: "call.py"})
	if err != nil {
		t.Fatal(err)
	}
	defer closeTestRunner(runner)

	progress, err := runner.Start(context.Background(), StartOptions{})
	if err != nil {
		t.Fatal(err)
	}
	snapshot, ok := progress.(*Snapshot)
	if !ok {
		closeTestProgress(progress)
		t.Fatalf("progress is %T, want *Snapshot", progress)
	}
	data, err := snapshot.Dump()
	if err != nil {
		closeTestProgress(progress)
		t.Fatal(err)
	}
	closeTestProgress(progress)

	reloaded, err := LoadSnapshot(data)
	if err != nil {
		t.Fatalf("LoadSnapshot: %v", err)
	}
	call, ok := reloaded.(*Snapshot)
	if !ok {
		closeTestProgress(reloaded)
		t.Fatalf("reloaded progress is %T, want *Snapshot", reloaded)
	}
	if call.FunctionName != "ext" || len(call.Args) != 2 {
		closeTestProgress(reloaded)
		t.Fatalf("reloaded call = %q with %d args, want ext with 2", call.FunctionName, len(call.Args))
	}
	done, err := call.ResumeReturn(context.Background(), Int(5))
	if err != nil {
		t.Fatalf("ResumeReturn: %v", err)
	}
	mustComplete(t, done, 15)
}

func TestNameLookupSnapshotDumpLoadResume(t *testing.T) {
	runner, err := New("target + 1", CompileOptions{ScriptName: "lookup.py"})
	if err != nil {
		t.Fatal(err)
	}
	defer closeTestRunner(runner)

	progress, err := runner.Start(context.Background(), StartOptions{})
	if err != nil {
		t.Fatal(err)
	}
	lookup, ok := progress.(*NameLookupSnapshot)
	if !ok {
		closeTestProgress(progress)
		t.Fatalf("progress is %T, want *NameLookupSnapshot", progress)
	}
	data, err := lookup.Dump()
	closeTestProgress(progress)
	if err != nil {
		t.Fatal(err)
	}

	reloaded, err := LoadSnapshot(data)
	if err != nil {
		t.Fatalf("LoadSnapshot: %v", err)
	}
	reloadedLookup, ok := reloaded.(*NameLookupSnapshot)
	if !ok {
		closeTestProgress(reloaded)
		t.Fatalf("reloaded progress is %T, want *NameLookupSnapshot", reloaded)
	}
	if reloadedLookup.VariableName != "target" {
		closeTestProgress(reloaded)
		t.Fatalf("VariableName = %q, want target", reloadedLookup.VariableName)
	}
	done, err := reloadedLookup.ResumeValue(context.Background(), Int(41))
	if err != nil {
		t.Fatalf("ResumeValue: %v", err)
	}
	mustComplete(t, done, 42)
}

func TestFutureSnapshotDumpLoadResume(t *testing.T) {
	runner, err := New("await ext()", CompileOptions{ScriptName: "future.py"})
	if err != nil {
		t.Fatal(err)
	}
	defer closeTestRunner(runner)

	progress, err := runner.Start(context.Background(), StartOptions{})
	if err != nil {
		t.Fatal(err)
	}
	call, ok := progress.(*Snapshot)
	if !ok {
		closeTestProgress(progress)
		t.Fatalf("progress is %T, want *Snapshot", progress)
	}
	callID := call.CallID
	pending, err := call.ResumePending(context.Background())
	if err != nil {
		t.Fatalf("ResumePending: %v", err)
	}
	future, ok := pending.(*FutureSnapshot)
	if !ok {
		closeTestProgress(pending)
		t.Fatalf("pending progress is %T, want *FutureSnapshot", pending)
	}
	data, err := future.Dump()
	closeTestProgress(pending)
	if err != nil {
		t.Fatal(err)
	}

	reloaded, err := LoadSnapshot(data)
	if err != nil {
		t.Fatalf("LoadSnapshot: %v", err)
	}
	reloadedFuture, ok := reloaded.(*FutureSnapshot)
	if !ok {
		closeTestProgress(reloaded)
		t.Fatalf("reloaded progress is %T, want *FutureSnapshot", reloaded)
	}
	ids := reloadedFuture.PendingCallIDs()
	if len(ids) != 1 || ids[0] != callID {
		closeTestProgress(reloaded)
		t.Fatalf("PendingCallIDs = %v, want [%d]", ids, callID)
	}
	done, err := reloadedFuture.ResumeResults(context.Background(), map[uint32]Result{callID: Return(Int(7))})
	if err != nil {
		t.Fatalf("ResumeResults: %v", err)
	}
	mustComplete(t, done, 7)
}

func TestReplSnapshotDumpLoadResume(t *testing.T) {
	repl, err := NewRepl(ReplOptions{ScriptName: "repl.py"})
	if err != nil {
		t.Fatal(err)
	}
	defer closeTestRepl(repl)

	progress, err := repl.FeedStart(context.Background(), "ext() + 1", FeedStartOptions{})
	if err != nil {
		t.Fatal(err)
	}
	call, ok := progress.(*Snapshot)
	if !ok {
		closeTestProgress(progress)
		t.Fatalf("progress is %T, want *Snapshot", progress)
	}
	data, err := call.Dump()
	closeTestProgress(progress)
	if err != nil {
		t.Fatal(err)
	}

	reloaded, owner, err := LoadReplSnapshot(data)
	if err != nil {
		t.Fatalf("LoadReplSnapshot: %v", err)
	}
	defer closeTestRepl(owner)
	reloadedCall, ok := reloaded.(*Snapshot)
	if !ok {
		closeTestProgress(reloaded)
		t.Fatalf("reloaded progress is %T, want *Snapshot", reloaded)
	}
	done, err := reloadedCall.ResumeReturn(context.Background(), Int(1))
	if err != nil {
		t.Fatalf("ResumeReturn: %v", err)
	}
	mustComplete(t, done, 2)
}
