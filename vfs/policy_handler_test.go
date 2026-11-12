package vfs

import (
	"context"
	"testing"

	"github.com/mdfranz/gomonty"
)

func TestDirFSOptionsMapToPythonExceptions(t *testing.T) {
	for _, tc := range []struct {
		name    string
		options DirFSOptions
		want    string
	}{
		{"readonly", DirFSOptions{ReadOnly: true}, "PermissionError"},
		{"file limit", DirFSOptions{MaxFileBytes: 2}, "OSError"},
		{"total limit", DirFSOptions{MaxTotalBytes: 2}, "OSError"},
		{"denied", DirFSOptions{Deny: []string{"file"}}, "PermissionError"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d := mountOptions(t, t.TempDir(), tc.options)
			result, err := Handler(d, nil)(context.Background(), monty.OSCall{Function: monty.OSPathWriteText, Args: []monty.Value{monty.PathValue("/file"), monty.String("long")}})
			if err != nil {
				t.Fatal(err)
			}
			exception, ok := result.Raised()
			if !ok || exception.Type != tc.want {
				t.Fatalf("exception=%+v,%v;want %s", exception, ok, tc.want)
			}
		})
	}
}

func TestOverlayThroughPython(t *testing.T) {
	o, lower := overlayFixture(t, true)
	runner, err := monty.New(`
from pathlib import Path
Path('/top').write_text('changed')
Path('/dir/one').unlink()
Path('/dir/new').write_text('new')
Path('/dir').rename('/moved')
Path('/top').read_text() + ':' + ','.join(sorted(p.name for p in Path('/moved').iterdir()))
`, monty.CompileOptions{ScriptName: "overlay.py"})
	if err != nil {
		t.Fatal(err)
	}
	result, err := runner.Run(context.Background(), monty.RunOptions{OS: Handler(o, nil)})
	if err != nil {
		t.Fatal(err)
	}
	if got := result.Raw(); got != "changed:new,two" {
		t.Fatalf("result=%v", got)
	}
	requireText(t, lower, "/top", "original")
	requireText(t, lower, "/dir/one", "one")
	diff, err := o.Diff()
	if err != nil || len(diff) == 0 {
		t.Fatalf("diff=%+v,%v", diff, err)
	}
}
