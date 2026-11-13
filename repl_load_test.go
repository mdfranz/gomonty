package monty

import (
	"context"
	"errors"
	"testing"
)

func TestLoadReplRestoresGlobalsAndDefinitions(t *testing.T) {
	ctx := context.Background()
	repl, err := NewRepl(ReplOptions{ScriptName: "dump.py"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repl.FeedRun(ctx, "x = 41\ndef inc(n):\n    return n + 1", FeedOptions{}); err != nil {
		t.Fatal(err)
	}
	data, err := repl.Dump()
	if err != nil {
		t.Fatal(err)
	}

	restored, err := LoadRepl(data, ReplOptions{ScriptName: "dump.py"})
	if err != nil {
		t.Fatalf("LoadRepl: %v", err)
	}
	got, err := restored.FeedRun(ctx, "inc(x)", FeedOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if got.Raw() != int64(42) {
		t.Fatalf("inc(x) = %v, want 42", got)
	}
	// The original is unaffected, and the restored one can be fed again.
	if _, err := repl.FeedRun(ctx, "x = 0", FeedOptions{}); err != nil {
		t.Fatal(err)
	}
	got, err = restored.FeedRun(ctx, "x", FeedOptions{})
	if err != nil || got.Raw() != int64(41) {
		t.Fatalf("x = %v, %v; want 41", got, err)
	}
}

func TestLoadReplRejectsBadData(t *testing.T) {
	if _, err := LoadRepl([]byte("not a dump"), ReplOptions{}); !errors.Is(err, ErrIncompatibleDump) {
		t.Fatalf("err = %v, want ErrIncompatibleDump", err)
	}
	if _, err := LoadRepl(addDumpHeader([]byte("garbage")), ReplOptions{}); err == nil {
		t.Fatal("a garbled dump was accepted")
	}
	// A failed load leaves the library usable.
	repl, err := NewRepl(ReplOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if got, err := repl.FeedRun(context.Background(), "1 + 1", FeedOptions{}); err != nil || got.Raw() != int64(2) {
		t.Fatalf("1 + 1 = %v, %v", got, err)
	}
}

// A backend without ReplLoader reports that, rather than guessing.
func TestLoadReplNeedsABackendThatCanLoad(t *testing.T) {
	_, err := LoadRepl(addDumpHeader([]byte("x")), ReplOptions{Backend: &fakeBackend{}})
	if !errors.Is(err, ErrLoadUnsupported) {
		t.Fatalf("err = %v, want ErrLoadUnsupported", err)
	}
}

func TestReplResetForgetsEverything(t *testing.T) {
	ctx := context.Background()
	repl, err := NewRepl(ReplOptions{ScriptName: "reset.py"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repl.FeedRun(ctx, "x = 1", FeedOptions{}); err != nil {
		t.Fatal(err)
	}
	if err := repl.Reset(ctx); err != nil {
		t.Fatalf("Reset: %v", err)
	}
	if _, err := repl.FeedRun(ctx, "x", FeedOptions{}); err == nil {
		t.Fatal("x survived a reset")
	}
	if got, err := repl.FeedRun(ctx, "2 + 2", FeedOptions{}); err != nil || got.Raw() != int64(4) {
		t.Fatalf("2 + 2 = %v, %v", got, err)
	}
}

type resettingRepl struct {
	fakeRepl
	resets *int
}

func (r *resettingRepl) Reset(context.Context) (ReplHandle, error) {
	*r.resets++
	return &resettingRepl{fakeRepl: r.fakeRepl, resets: r.resets}, nil
}

type resettingBackend struct {
	fakeBackend
	resets int
	opens  int
}

func (b *resettingBackend) NewRepl(ReplOptions) (ReplHandle, error) {
	b.opens++
	return &resettingRepl{fakeRepl: fakeRepl{b: &b.fakeBackend}, resets: &b.resets}, nil
}

// A handle that can reset itself is reset in place, not reopened.
func TestReplResetUsesTheHandlesOwnReset(t *testing.T) {
	b := &resettingBackend{}
	repl, err := NewRepl(ReplOptions{Backend: b})
	if err != nil {
		t.Fatal(err)
	}
	if err := repl.Reset(context.Background()); err != nil {
		t.Fatal(err)
	}
	if b.resets != 1 || b.opens != 1 {
		t.Fatalf("resets = %d, opens = %d; want 1 reset and no second open", b.resets, b.opens)
	}
	if err := repl.Reset(context.Background()); err != nil || b.resets != 2 {
		t.Fatalf("second reset: %v, resets = %d", err, b.resets)
	}
}

func TestReplResetWhileRunningIsRefused(t *testing.T) {
	repl, err := NewRepl(ReplOptions{Backend: &fakeBackend{}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repl.state.takeForExecution(); err != nil {
		t.Fatal(err)
	}
	if err := repl.Reset(context.Background()); !errors.Is(err, ErrConcurrentUse) {
		t.Fatalf("err = %v, want ErrConcurrentUse", err)
	}
}
