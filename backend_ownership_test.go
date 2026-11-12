package monty

import (
	"context"
	"errors"
	"testing"
)

// These tests pin down who owns which handle when something goes wrong. They
// reuse the fakes from backend_test.go.

// scriptedLoads is a fake backend whose snapshot loads return scripted steps,
// in order.
type scriptedLoads struct {
	*fakeBackend
	loads []step
}

func (s *scriptedLoads) loadProgress([]byte) step {
	if len(s.loads) == 0 {
		return step{err: errors.New("no scripted load")}
	}
	next := s.loads[0]
	s.loads = s.loads[1:]
	return next
}

// retainingRepl fails its first snippets the way a backend does when it cannot
// encode the options: nothing was sent, so the session was not consumed.
type retainingRepl struct {
	*fakeRepl
	failures int
}

func (r *retainingRepl) FeedStart(code string, opts FeedStartOptions) step {
	if r.failures > 0 {
		r.failures--
		return step{err: errors.New("cannot encode the inputs"), retained: true}
	}
	return r.fakeRepl.FeedStart(code, opts)
}

type retainingBackend struct {
	*fakeBackend
	failures int
}

func (b *retainingBackend) newRepl(ReplOptions) (replHandle, error) {
	return &retainingRepl{fakeRepl: &fakeRepl{b: b.fakeBackend}, failures: b.failures}, nil
}

func pausedCall() progressDesc {
	return progressDesc{
		kind:         progressFunctionCall,
		scriptName:   "fake.py",
		isRepl:       true,
		functionName: "double",
		args:         []Value{Int(1)},
		callID:       1,
	}
}

// With the native library: an input that cannot be encoded fails the snippet
// before anything runs, and the REPL must stay usable. It used to be left
// permanently "already in use".
func TestReplSurvivesInputsItCannotEncode(t *testing.T) {
	repl, err := NewRepl(ReplOptions{})
	if err != nil {
		t.Fatal(err)
	}
	defer closeTestRepl(repl)

	bad := Value{kind: ValueKind("not-a-real-kind")}
	_, err = repl.FeedRun(context.Background(), "1", FeedOptions{Inputs: map[string]Value{"bad": bad}})
	if err == nil {
		t.Fatal("an unsupported value was accepted as an input")
	}
	if errors.Is(err, ErrConcurrentUse) || errors.Is(err, ErrClosed) {
		t.Fatalf("the failure was reported as a handle problem: %v", err)
	}

	got, err := repl.FeedRun(context.Background(), "1 + 1", FeedOptions{})
	if err != nil {
		t.Fatalf("REPL unusable after a rejected input: %v", err)
	}
	if got.Raw() != int64(2) {
		t.Fatalf("1 + 1 = %v", got.Raw())
	}
}

// The same rule at the host layer, over a backend that reports it.
func TestReplKeepsSessionWhenTheBackendRetainsIt(t *testing.T) {
	fake := &fakeBackend{}
	repl, err := NewRepl(ReplOptions{backend: &retainingBackend{fakeBackend: fake, failures: 2}})
	if err != nil {
		t.Fatal(err)
	}

	for i := 0; i < 2; i++ {
		if _, err := repl.FeedRun(context.Background(), "ignored", FeedOptions{}); err == nil {
			t.Fatalf("attempt %d: expected the backend's encode error", i+1)
		}
	}
	// Nothing consumed the session, so nothing should have closed it.
	if closes := fake.closes.Load(); closes != 0 {
		t.Fatalf("session closed %d times after retained failures", closes)
	}

	got, err := repl.FeedRun(context.Background(), "ignored", FeedOptions{
		Inputs:    map[string]Value{"x": Int(4)},
		Functions: map[string]ExternalFunction{"double": doubleFunction},
	})
	if err != nil {
		t.Fatalf("REPL unusable after retained failures: %v", err)
	}
	if got.Raw() != int64(8) {
		t.Fatalf("result = %v, want 8", got.Raw())
	}
}

func TestLoadSnapshotClosesTheHandleOnFailure(t *testing.T) {
	fake := &fakeBackend{}
	leaky := &fakeProgress{b: fake}
	b := &scriptedLoads{fakeBackend: fake, loads: []step{{progress: leaky, err: errors.New("bad payload")}}}

	if _, err := loadSnapshot(b, addDumpHeader([]byte("snapshot"))); err == nil {
		t.Fatal("expected an error")
	}
	if closes := fake.closes.Load(); closes != 1 {
		t.Fatalf("handle closed %d times, want 1", closes)
	}

	// A backend that reports success without a handle must produce an error,
	// not a panic.
	b = &scriptedLoads{fakeBackend: fake, loads: []step{{}}}
	if _, err := loadSnapshot(b, addDumpHeader([]byte("snapshot"))); err == nil {
		t.Fatal("expected an error for a missing handle")
	}
}

// A REPL snapshot is loaded twice: once to resume from, once to recover the
// session that owns it. The recovery copy must be closed, and a failure part
// way through must not leak the first load.
func TestLoadReplSnapshotClosesTheRecoveryCopy(t *testing.T) {
	data := addDumpHeader([]byte("snapshot"))

	t.Run("success", func(t *testing.T) {
		fake := &fakeBackend{}
		b := &scriptedLoads{fakeBackend: fake, loads: []step{
			{progress: &fakeProgress{b: fake}, desc: pausedCall()},
			{progress: &fakeProgress{b: fake}, desc: pausedCall()},
		}}
		progress, owner, err := loadReplSnapshot(b, data)
		if err != nil {
			t.Fatal(err)
		}
		if _, ok := progress.(*Snapshot); !ok {
			t.Fatalf("progress is %T, want *Snapshot", progress)
		}
		if owner == nil || owner.state.handle == nil {
			t.Fatal("the owning session was not restored")
		}
		if closes := fake.closes.Load(); closes != 1 {
			t.Fatalf("closed %d handles, want only the recovery copy", closes)
		}
	})

	t.Run("second load fails", func(t *testing.T) {
		fake := &fakeBackend{}
		b := &scriptedLoads{fakeBackend: fake, loads: []step{
			{progress: &fakeProgress{b: fake}, desc: pausedCall()},
			{err: errors.New("second load failed")},
		}}
		if _, _, err := loadReplSnapshot(b, data); err == nil {
			t.Fatal("expected an error")
		}
		if closes := fake.closes.Load(); closes != 1 {
			t.Fatalf("closed %d handles, want the first load released", closes)
		}
	})
}
