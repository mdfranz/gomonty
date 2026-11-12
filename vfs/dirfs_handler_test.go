package vfs

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/mdfranz/gomonty"
)

// runPython runs a script against a DirFS-backed handler and returns the
// script's final expression as a string.
func runPython(t *testing.T, f *dirFixture, code string) string {
	t.Helper()
	runner, err := monty.New(code, monty.CompileOptions{ScriptName: "dirfs.py"})
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	value, err := runner.Run(context.Background(), monty.RunOptions{
		OS: Handler(f.fs, MapEnvironment{}),
	})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	text, ok := value.Raw().(string)
	if !ok {
		t.Fatalf("script returned %#v, want a string", value.Raw())
	}
	return text
}

func TestDirFSThroughPython(t *testing.T) {
	f := newDirFixture(t)
	f.write(t, "notes/todo.txt", "write tests")

	got := runPython(t, f, `
from pathlib import Path

todo = Path('/notes/todo.txt')
Path('/notes/done.txt').write_text(todo.read_text().upper())
sorted(p.name for p in Path('/notes').iterdir())[0] + ':' + Path('/notes/done.txt').read_text()
`)
	if got != "done.txt:WRITE TESTS" {
		t.Fatalf("script result = %q", got)
	}
	hostData, err := os.ReadFile(filepath.Join(f.root, "notes", "done.txt"))
	if err != nil || string(hostData) != "WRITE TESTS" {
		t.Fatalf("host file = %q, %v", hostData, err)
	}
}

// A symlink that leaves the mount reaches sandbox code as PermissionError,
// and a missing file as FileNotFoundError.
func TestDirFSErrorsReachPythonAsExceptions(t *testing.T) {
	f := newDirFixture(t)
	f.symlink(t, filepath.Join(f.outside, "secret.txt"), "leak.txt")
	f.symlink(t, f.outside, "leakdir")

	got := runPython(t, f, `
from pathlib import Path

def outcome(action):
    try:
        action()
    except Exception as exc:
        return type(exc).__name__
    return 'no error'

':'.join([
    outcome(lambda: Path('/leak.txt').read_text()),
    outcome(lambda: Path('/leakdir/secret.txt').read_text()),
    outcome(lambda: Path('/leakdir/new.txt').write_text('x')),
    outcome(lambda: Path('/missing.txt').read_text()),
])
`)
	want := "PermissionError:PermissionError:PermissionError:FileNotFoundError"
	if got != want {
		t.Fatalf("exceptions = %q, want %q", got, want)
	}
	f.requireOutsideUntouched(t)
}
