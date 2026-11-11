package vfs

import (
	"context"
	"testing"

	monty "github.com/mdfranz/gomonty"
)

func TestMemoryFSReadWriteRename(t *testing.T) {
	fileSystem := NewMemoryFS()
	fileSystem.AddText("/config.txt", "alpha")

	text, err := fileSystem.ReadText("/config.txt")
	if err != nil {
		t.Fatalf("ReadText: %v", err)
	}
	if text != "alpha" {
		t.Fatalf("unexpected text %q", text)
	}

	if err := fileSystem.Mkdir("/data", false, false); err != nil {
		t.Fatalf("Mkdir: %v", err)
	}
	if _, err := fileSystem.WriteText("/data/output.txt", "beta"); err != nil {
		t.Fatalf("WriteText: %v", err)
	}
	if err := fileSystem.Rename("/data/output.txt", "/data/result.txt"); err != nil {
		t.Fatalf("Rename: %v", err)
	}

	entries, err := fileSystem.Iterdir("/data")
	if err != nil {
		t.Fatalf("Iterdir: %v", err)
	}
	if len(entries) != 1 || entries[0] != "/data/result.txt" {
		t.Fatalf("unexpected entries: %#v", entries)
	}
}

func TestHandlerEnvironmentAndErrorMapping(t *testing.T) {
	fileSystem := NewMemoryFS()
	fileSystem.AddText("/note.txt", "hello")

	handler := Handler(fileSystem, MapEnvironment{
		"HOME": "/sandbox",
	})

	getenvResult, err := handler(context.Background(), monty.OSCall{
		Function: monty.OSGetenv,
		Args: []monty.Value{
			monty.String("HOME"),
		},
	})
	if err != nil {
		t.Fatalf("getenv handler: %v", err)
	}
	if getenvResult.Value().Raw().(string) != "/sandbox" {
		t.Fatalf("unexpected getenv value: %#v", getenvResult.Value())
	}

	statResult, err := handler(context.Background(), monty.OSCall{
		Function: monty.OSPathStat,
		Args: []monty.Value{
			monty.PathValue(monty.Path("/note.txt")),
		},
	})
	if err != nil {
		t.Fatalf("stat handler: %v", err)
	}
	if _, ok := statResult.Value().StatResult(); !ok {
		t.Fatalf("expected stat result, got %#v", statResult.Value())
	}

	missingResult, err := handler(context.Background(), monty.OSCall{
		Function: monty.OSPathReadText,
		Args: []monty.Value{
			monty.PathValue(monty.Path("/missing.txt")),
		},
	})
	if err != nil {
		t.Fatalf("missing read handler: %v", err)
	}
	exception, ok := missingResult.Raised()
	if !ok {
		t.Fatalf("expected exception result, got %#v", missingResult)
	}
	if exception.Type != "FileNotFoundError" {
		t.Fatalf("unexpected exception type %q", exception.Type)
	}
}

func TestScanListsSubtree(t *testing.T) {
	fs := NewMemoryFS()
	if err := fs.Mkdir("/data/sub", true, false); err != nil {
		t.Fatal(err)
	}
	if _, err := fs.WriteText("/data/a.txt", "a"); err != nil {
		t.Fatal(err)
	}
	if _, err := fs.WriteText("/data/sub/b.txt", "b"); err != nil {
		t.Fatal(err)
	}
	handler := Handler(fs, nil)
	run := func(code string) monty.Value {
		t.Helper()
		runner, err := monty.New(code, monty.CompileOptions{ScriptName: "scan.py"})
		if err != nil {
			t.Fatalf("New: %v", err)
		}
		got, err := runner.Run(context.Background(), monty.RunOptions{OS: handler})
		if err != nil {
			t.Fatalf("Run: %v", err)
		}
		return got
	}

	got := run(`
import os
sorted(entry.name for entry in os.scandir('/data'))
`)
	if got.String() != "['a.txt', 'sub']" {
		t.Fatalf("scandir = %s", got)
	}

	got = run(`
import os
sorted((root, sorted(files)) for root, _dirs, files in os.walk('/data'))
`)
	if got.String() != "[('/data', ['a.txt']), ('/data/sub', ['b.txt'])]" {
		t.Fatalf("walk = %s", got)
	}

	got = run(`
from pathlib import Path
sorted(str(p) for p in Path('/data').rglob('*.txt'))
`)
	if got.String() != "['/data/a.txt', '/data/sub/b.txt']" {
		t.Fatalf("rglob = %s", got)
	}
}

func runImportScript(t *testing.T, fileSystem *MemoryFS, code string) (monty.Value, error) {
	t.Helper()
	runner, err := monty.New(code, monty.CompileOptions{ScriptName: "import.py"})
	if err != nil {
		t.Fatal(err)
	}
	return runner.Run(context.Background(), monty.RunOptions{OS: Handler(fileSystem, MapEnvironment{})})
}

func TestHandlerImportsPythonFiles(t *testing.T) {
	fileSystem := NewMemoryFS()
	fileSystem.AddText("/helper.py", "ANSWER = 40 + 2\n")
	fileSystem.AddText("/pkg/__init__.py", "NAME = 'pkg'\n")
	fileSystem.AddText("/pkg/sub.py", "VALUE = 7\n")

	got, err := runImportScript(t, fileSystem, `
import helper
import pkg
import pkg.sub
(helper.ANSWER, pkg.NAME, pkg.sub.VALUE)
`)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if got.String() != "(42, 'pkg', 7)" {
		t.Fatalf("result = %s", got.String())
	}
}

func TestHandlerImportMissingModule(t *testing.T) {
	fileSystem := NewMemoryFS()
	fileSystem.AddText("/pkg/__init__.py", "")

	got, err := runImportScript(t, fileSystem, `
results = []
try:
    import nothing_here
except ModuleNotFoundError as e:
    results.append(type(e).__name__)
try:
    from pkg import missing
except ImportError as e:
    results.append(type(e).__name__)
results
`)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if got.String() != "['ModuleNotFoundError', 'ImportError']" {
		t.Fatalf("result = %s", got.String())
	}
}

func TestHandlerTypeHintsDefaultsToEmptyDict(t *testing.T) {
	result, err := Handler(NewMemoryFS(), MapEnvironment{})(context.Background(), monty.OSCall{
		Function: monty.OSGetTypeHints,
		Args:     []monty.Value{monty.String("function"), monty.String("f")},
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := result.Value(); got.Kind() != "dict" {
		t.Fatalf("result = %v", got)
	}
}
