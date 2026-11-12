// Package vfs adapts Go-defined filesystems and environments into Monty OS
// handlers.
//
// MemoryFS keeps files in Go-owned memory. DirFS mounts a real directory with
// optional read-only, byte-limit, and path policies. Overlay layers in-memory
// edits over a stable lower filesystem and exposes a net diff for review.
package vfs
