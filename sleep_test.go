package monty

import (
	"context"
	"errors"
	"testing"
	"time"
)

func sleepCode(t *testing.T, code string, opts RunOptions) (Value, time.Duration, error) {
	t.Helper()
	runner, err := New(code, CompileOptions{ScriptName: "sleep.py"})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer closeTestRunner(runner)
	start := time.Now()
	got, err := runner.Run(context.Background(), opts)
	return got, time.Since(start), err
}

func TestSystemSleepWaitsWithoutOSHandler(t *testing.T) {
	got, elapsed, err := sleepCode(t, "import time\ntime.sleep(0.2)\n'slept'", RunOptions{})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if got.Raw() != "slept" || elapsed < 200*time.Millisecond {
		t.Fatalf("result = %v after %v, want 'slept' after >= 200ms", got, elapsed)
	}
}

func TestSystemSleepNeverReachesOSHandler(t *testing.T) {
	os := func(_ context.Context, call OSCall) (Result, error) {
		t.Errorf("sleep reached the OS handler: %s", call.Function)
		return Return(None()), nil
	}
	code := "import time, asyncio\ntime.sleep(0.05)\nasync def f():\n    await asyncio.sleep(0.05)\n    return 'done'\nawait f()"
	got, elapsed, err := sleepCode(t, code, RunOptions{OS: os})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if got.Raw() != "done" || elapsed < 100*time.Millisecond {
		t.Fatalf("result = %v after %v", got, elapsed)
	}
}

func TestAsyncSleepLetsOtherTasksRun(t *testing.T) {
	code := `import asyncio
async def nap(n):
    await asyncio.sleep(0.3)
    return n
await asyncio.gather(nap(1), nap(2), nap(3))`
	got, elapsed, err := sleepCode(t, code, RunOptions{})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if elapsed < 300*time.Millisecond || elapsed > 700*time.Millisecond {
		t.Fatalf("gathered sleeps took %v, want about 300ms (concurrent)", elapsed)
	}
	if got.String() == "" {
		t.Fatal("empty result")
	}
}

func TestSystemSleepRespectsCancellation(t *testing.T) {
	for name, code := range map[string]string{
		"sync":  "import time\ntime.sleep(30)",
		"async": "import asyncio\nawait asyncio.sleep(30)",
	} {
		t.Run(name, func(t *testing.T) {
			runner, err := New(code, CompileOptions{ScriptName: "sleep.py"})
			if err != nil {
				t.Fatal(err)
			}
			defer closeTestRunner(runner)
			ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
			defer cancel()
			start := time.Now()
			_, err = runner.Run(ctx, RunOptions{})
			if !errors.Is(err, context.DeadlineExceeded) {
				t.Fatalf("err = %v, want deadline exceeded", err)
			}
			if time.Since(start) > 5*time.Second {
				t.Fatalf("cancellation took %v", time.Since(start))
			}
		})
	}
}

func TestSystemSleepDoesNotCountAgainstMaxDuration(t *testing.T) {
	got, _, err := sleepCode(t, "import time\ntime.sleep(0.4)\n'ok'", RunOptions{
		Limits: &ResourceLimits{MaxDuration: 100 * time.Millisecond},
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if got.Raw() != "ok" {
		t.Fatalf("result = %v", got)
	}
}
