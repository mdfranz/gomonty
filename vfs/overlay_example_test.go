package vfs_test

import (
	"fmt"

	"github.com/mdfranz/gomonty/vfs"
)

func ExampleOverlay() {
	lower := vfs.NewMemoryFS()
	lower.AddText("/notes.txt", "original")
	preview := vfs.Overlay(lower, vfs.NewMemoryFS())
	if _, err := preview.WriteText("/notes.txt", "proposed"); err != nil {
		panic(err)
	}
	if _, err := preview.WriteText("/new.txt", "new"); err != nil {
		panic(err)
	}
	changes, err := preview.Diff()
	if err != nil {
		panic(err)
	}
	for _, change := range changes {
		fmt.Printf("%s %s: %s\n", change.Kind, change.Path, change.Data)
	}
	original, err := lower.ReadText("/notes.txt")
	if err != nil {
		panic(err)
	}
	fmt.Println("lower:", original)
	// Output:
	// created /new.txt: new
	// modified /notes.txt: proposed
	// lower: original
}
