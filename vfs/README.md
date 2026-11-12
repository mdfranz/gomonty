# Filesystem policies and edit previews

`Handler(fs, env)` accepts `MemoryFS`, `DirFS`, or `OverlayFS` without changes
to the sandbox's Python code.

## Restricting a disk mount

```go
mounted, err := vfs.NewDirFS(projectDir, vfs.DirFSOptions{
    MaxFileBytes:  1 << 20,
    MaxTotalBytes: 10 << 20,
    Allow: []string{".", "src", "notes/*.txt"},
    Deny:  []string{"src/private", "*.key"},
})
if err != nil {
    return err
}
defer mounted.Close()
```

`NewDirFS(dir)` still creates an unrestricted mount. Add `ReadOnly: true` to
reject all mutation methods, including directory changes and renames, with a
Python `PermissionError`. Size violations become `OSError` with the limit name.
Negative limits and invalid glob patterns are rejected when mounting.

`MaxFileBytes` caps the replacement contents of each write. `MaxTotalBytes`
counts the logical byte sizes of all regular file entries already in the mount,
including files hidden by a path policy. Each hard-link entry counts separately;
symlinks are not followed when measuring the tree. Zero means unlimited. Removing
or shrinking files frees space. Reads are not capped. A tree that already exceeds
the cap can still be read and cleaned up; writes must bring the total under it.

Limits apply per `DirFS` instance. Mutations through that instance are serialized
so concurrent writes cannot overspend its cap. Each write checks the current
size of the tree. This does not reserve space against independent mounts or
concurrent host writers. A total-byte cap costs a tree walk per write.

Writes stage a temporary file in the destination directory and rename it into
place after the complete write succeeds. Rejected writes and staging failures
leave existing contents intact and remove staging files. Replacement preserves
permission bits but creates a new file identity: existing hard links continue
pointing to the old contents. Writes through symlinks update the resolved target.
Temporary disk usage while staging can exceed the logical byte cap; the cap is
not a disk-space quota. This is atomic publication, not a crash-durability guarantee.

`Allow` and `Deny` use Go's `path.Match` syntax on normalized root-relative POSIX
paths. `*` does not cross `/`; `**` has no special recursive meaning. A match on
a directory covers its descendants. `.` matches only root, so permitting root
listings does not permit every file. An empty `Allow` permits all paths; `Deny`
always wins. Direct access to denied paths raises `PermissionError`, and directory
listings omit denied children. Both symlink aliases and resolved targets must be
permitted. Renaming a directory checks its descendants at their old and new paths.
Policies assume host processes do not concurrently rewrite the tree; `os.Root`
continues enforcing mount confinement for filesystem operations.

## Previewing edits in memory

```go
lower, err := vfs.NewDirFS(projectDir, vfs.DirFSOptions{ReadOnly: true})
if err != nil {
    return err
}
defer lower.Close()
preview := vfs.Overlay(lower, vfs.NewMemoryFS())
// Pass vfs.Handler(preview, env) as RunOptions.OS or FeedOptions.OS.
changes, err := preview.Diff()
```

Reads use the memory layer when present, otherwise the lower filesystem. Writes,
deletes, directory changes, and renames affect only memory. A deletion leaves a
whiteout that prevents the lower entry from reappearing. Directory listings merge
both layers. Renaming a directory copies the current merged subtree into memory
and hides its old location; failed reads or validation leave the view unchanged.
Recreating a deleted directory does not resurrect its lower children.

`Diff()` returns changes sorted by absolute sandbox path. Each `Change` contains
`Kind` (`ChangeCreated`, `ChangeModified`, or `ChangeDeleted`), `Path`, `IsDir`,
and copied `Data` for created or modified regular files. Binary data is preserved.
Directories and deletions have nil `Data`. Renames report the net changes at
their old and new paths, including descendant paths; there is no separate rename
kind. The diff describes the net current view:
restoring original file contents or creating then deleting a file produces no
change. Permission and timestamp changes are not part of the diff.

Use distinct layers, keep the lower tree stable during editing and diffing, and
access upper only through the overlay. Nil layers create empty `MemoryFS`
instances. Overlay methods are safe for concurrent use through a single instance.
To keep the diff model simple, operations through symlink paths are rejected with
`PermissionError`; normal `DirFS` mounts still support confined symlinks.

There is no automatic apply operation. Present the diff for review and decide in
the host application how and when to apply approved changes. Closing the lower
`DirFS` remains the caller's responsibility.
