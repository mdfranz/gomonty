package vfs_test

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	monty "github.com/mdfranz/gomonty"
	"github.com/mdfranz/gomonty/vfs"
)

// DirFS mounts a real directory as "/" inside the sandbox. Code can read and
// write files there, and cannot reach anything outside it.
func ExampleNewDirFS() {
	dir, err := os.MkdirTemp("", "dirfs-example")
	if err != nil {
		panic(err)
	}
	defer os.RemoveAll(dir)
	if err := os.WriteFile(filepath.Join(dir, "notes.txt"), []byte("hello from disk"), 0o644); err != nil {
		panic(err)
	}

	mounted, err := vfs.NewDirFS(dir)
	if err != nil {
		panic(err)
	}
	defer mounted.Close()

	runner, err := monty.New(`
from pathlib import Path

text = Path("/notes.txt").read_text()
Path("/shout.txt").write_text(text.upper())
Path("/shout.txt").read_text()
`, monty.CompileOptions{ScriptName: "example.py"})
	if err != nil {
		panic(err)
	}

	value, err := runner.Run(context.Background(), monty.RunOptions{
		OS: vfs.Handler(mounted, vfs.MapEnvironment{}),
	})
	if err != nil {
		panic(err)
	}
	fmt.Println(value.Raw())

	written, err := os.ReadFile(filepath.Join(dir, "shout.txt"))
	if err != nil {
		panic(err)
	}
	fmt.Println(string(written))
	// Output:
	// HELLO FROM DISK
	// HELLO FROM DISK
}
