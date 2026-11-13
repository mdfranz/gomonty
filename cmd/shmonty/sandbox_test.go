package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	monty "github.com/mdfranz/gomonty"
)

const sandboxChecks = `
from pathlib import Path
import os
import hostmod
assert os.getenv("SHMONTY_ENV") == "sandbox"
assert os.environ["HOME"] == "/sandbox"
assert hostmod.name == "shmonty" and hostmod.root == "/sandbox"
assert "Welcome" in Path("/sandbox/readme.txt").read_text()
assert Path("/sandbox/readme.txt").stat().st_size == len(Path("/sandbox/readme.txt").read_text())
Path("/sandbox/test.txt").write_text("saved")
assert Path("/sandbox/test.txt").read_text() == "saved"
assert len(list(Path("/sandbox").rglob("*.txt"))) == 3
assert len(list(os.walk("/sandbox"))) == 2
assert host_calc("add", 2, 3) == 5
assert host_calc(b=4, op="mul", a=3) == 12
assert host_calc("sub", 9, b=2) == 7
assert host_calc("div", 9, 2) == 4.5
assert host_echo(value=[1, "two"]) == {"value": [1, "two"], "source": "shmonty"}
try:
    host_fail(msg="demo failure")
except ValueError as error:
    assert str(error) == "demo failure"
else:
    assert False, "host_fail must raise"
import asyncio
async def gather_sleeps():
    return await asyncio.gather(host_sleep(0.05, "slow"), host_sleep(0.01, "fast"), host_sleep(0))
assert await gather_sleeps() == ["slow", "fast", None]
`

func TestSandboxScript(t *testing.T) {
	runner, err := monty.New(sandboxChecks, monty.CompileOptions{})
	if err != nil {
		t.Fatal(err)
	}
	s := newSandbox()
	if _, err := runner.Run(context.Background(), monty.RunOptions{
		OS: s.os, Modules: s.modules, Functions: hostFunctions(),
	}); err != nil {
		t.Fatal(err)
	}
	// A separate script invocation gets a clean filesystem.
	fresh := newSandbox()
	check, err := monty.New(`from pathlib import Path; assert not Path("/sandbox/test.txt").exists()`, monty.CompileOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := check.Run(context.Background(), monty.RunOptions{OS: fresh.os}); err != nil {
		t.Fatal(err)
	}
}

func TestSandboxReplPersistsAndRewinds(t *testing.T) {
	repl, err := monty.NewRepl(monty.ReplOptions{})
	if err != nil {
		t.Fatal(err)
	}
	m := newModel(repl, nil, &logBuffer{})
	if e := m.runCode("checks", sandboxChecks); e.isErr {
		t.Fatal(e.result)
	}
	if e := m.runCode("read", `assert Path("/sandbox/test.txt").read_text() == "saved"`); e.isErr {
		t.Fatal(e.result)
	}
	script := filepath.Join(t.TempDir(), "execute.py")
	if err := os.WriteFile(script, []byte(`assert Path("/sandbox/test.txt").read_text() == "saved"`), 0600); err != nil {
		t.Fatal(err)
	}
	if e := m.executeFile("/execute "+script, script); e.isErr {
		t.Fatal(e.result)
	}
	// Rewind replays earlier feeds into fresh Python and filesystem state.
	m.history = []string{`from pathlib import Path`, `Path("/sandbox/removed.txt").write_text("removed")`}
	for _, code := range m.history {
		if e := m.runCode(code, code); e.isErr {
			t.Fatal(e.result)
		}
	}
	m = m.rewind()
	if e := m.runCode("rewound", `assert not Path("/sandbox/removed.txt").exists()`); e.isErr {
		t.Fatal(e.result)
	}
}

func TestHostCallbackErrorsAreCatchable(t *testing.T) {
	repl, err := monty.NewRepl(monty.ReplOptions{})
	if err != nil {
		t.Fatal(err)
	}
	m := newModel(repl, nil, &logBuffer{})
	cases := []struct{ code, kind string }{
		{`host_calc("add", 1)`, "TypeError"},
		{`host_calc("add", 1, 2, a=3)`, "TypeError"},
		{`host_calc(op="add", a=1, b=2, extra=3)`, "TypeError"},
		{`host_calc("add", 1, 2, 3)`, "TypeError"},
		{`host_calc(1, 2, 3)`, "TypeError"},
		{`host_calc("add", "x", 2)`, "TypeError"},
		{`host_calc("unknown", 1, 2)`, "ValueError"},
		{`host_calc("div", 1, 0)`, "ZeroDivisionError"},
		{`host_echo()`, "TypeError"},
		{`host_fail(1)`, "TypeError"},
		{`host_sleep()`, "TypeError"},
		{`host_sleep("x")`, "TypeError"},
		{`host_sleep(-1)`, "ValueError"},
		{`host_sleep(31)`, "ValueError"},
	}
	for _, tc := range cases {
		t.Run(tc.code, func(t *testing.T) {
			code := "try:\n    " + tc.code + "\nexcept " + tc.kind + ":\n    pass\nelse:\n    assert False, 'expected exception'"
			if e := m.runCode(tc.code, code); e.isErr {
				t.Fatal(e.result)
			}
		})
	}
}
