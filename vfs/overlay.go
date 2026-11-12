package vfs

import (
	"bytes"
	"errors"
	"fmt"
	stdfs "io/fs"
	"path"
	"sort"
	"strings"
	"sync"
	"syscall"
	"unicode/utf8"

	"github.com/mdfranz/gomonty"
)

// OverlayFS reads from a lower filesystem and stages all mutations in memory.
// The lower tree must remain stable while using the overlay and inspecting its
// diff. Use the upper filesystem exclusively through this overlay. Symlink
// traversal and mutation are rejected so changes always name concrete paths.
// Operations through a single OverlayFS are safe for concurrent use.
type OverlayFS struct {
	mu        sync.Mutex
	lower     FileSystem
	upper     *MemoryFS
	whiteouts map[string]bool
}

var _ FileSystem = (*OverlayFS)(nil)

// Overlay constructs a copy-on-write filesystem. Nil layers are replaced with
// empty MemoryFS instances. The layers must be distinct. No writes, deletes or
// renames are sent to lower, including when lower is a read-only DirFS.
// It panics if the same MemoryFS is supplied as both layers.
func Overlay(lower FileSystem, upper *MemoryFS) *OverlayFS {
	if lower == nil {
		lower = NewMemoryFS()
	}
	if upper == nil {
		upper = NewMemoryFS()
	}
	if memory, ok := lower.(*MemoryFS); ok && memory == upper {
		panic("vfs: overlay layers must be distinct")
	}
	return &OverlayFS{lower: lower, upper: upper, whiteouts: make(map[string]bool)}
}

// ChangeKind identifies a net change relative to the lower tree.
type ChangeKind string

const (
	ChangeCreated  ChangeKind = "created"
	ChangeModified ChangeKind = "modified"
	ChangeDeleted  ChangeKind = "deleted"
)

// Change describes one path in a diff. Data is a copy of the current contents
// for created/modified regular files; it is nil for directories and deletions.
// IsDir describes the new entry, or the old entry for a deletion. Metadata-only
// changes are omitted. Renames have no separate kind: the old and new paths
// report their net created, modified, or deleted state.
type Change struct {
	Path  string
	Kind  ChangeKind
	IsDir bool
	Data  []byte
}

func overlayError(op, p string, err error) error { return &stdfs.PathError{Op: op, Path: p, Err: err} }
func (o *OverlayFS) masked(p string) bool {
	for {
		if o.whiteouts[p] {
			return true
		}
		if p == "/" {
			return false
		}
		p = path.Dir(p)
	}
}
func (o *OverlayFS) upperEntry(p string) *entry {
	o.upper.mu.RLock()
	defer o.upper.mu.RUnlock()
	if e := o.upper.entries[p]; e != nil {
		copy := *e
		return &copy
	}
	return nil
}
func (o *OverlayFS) raw(p string) (*entry, error) {
	if e := o.upperEntry(p); e != nil {
		if e.kind == entryKindSymlink {
			return nil, overlayError("overlay", p, fmt.Errorf("symlinks are not supported by overlays: %w", stdfs.ErrPermission))
		}
		return e, nil
	}
	if o.masked(p) {
		return nil, overlayError("stat", p, stdfs.ErrNotExist)
	}
	link, err := o.lower.IsSymlink(p)
	if err != nil {
		return nil, err
	}
	if link {
		return nil, overlayError("overlay", p, fmt.Errorf("symlinks are not supported by overlays: %w", stdfs.ErrPermission))
	}
	stat, err := o.lower.Stat(p)
	if err != nil {
		return nil, err
	}
	kind := entryKindFile
	if stat.Mode&0o170_000 == 0o040_000 {
		kind = entryKindDirectory
	}
	return &entry{kind: kind, stat: stat}, nil
}
func (o *OverlayFS) node(p string) (*entry, error) {
	current := "/"
	e, err := o.raw(current)
	if err != nil || p == "/" {
		return e, err
	}
	for _, part := range strings.Split(strings.TrimPrefix(p, "/"), "/") {
		if e.kind != entryKindDirectory {
			return nil, overlayError("stat", current, syscall.ENOTDIR)
		}
		current = path.Join(current, part)
		e, err = o.raw(current)
		if err != nil {
			return nil, err
		}
	}
	return e, nil
}

func (o *OverlayFS) read(p string) ([]byte, error) {
	e, err := o.node(p)
	if err != nil {
		return nil, err
	}
	if e.kind == entryKindDirectory {
		return nil, overlayError("read", p, syscall.EISDIR)
	}
	if upper := o.upperEntry(p); upper != nil {
		return bytes.Clone(upper.data), nil
	}
	return o.lower.ReadBytes(p)
}

// Exists reports whether a visible path exists.
func (o *OverlayFS) Exists(p string) (bool, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	_, err := o.node(normalizePath(p))
	if errors.Is(err, stdfs.ErrNotExist) || errors.Is(err, syscall.ENOTDIR) {
		return false, nil
	}
	return err == nil, err
}

// IsFile reports whether a visible path is a regular file.
func (o *OverlayFS) IsFile(p string) (bool, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	e, err := o.node(normalizePath(p))
	if errors.Is(err, stdfs.ErrNotExist) || errors.Is(err, syscall.ENOTDIR) {
		return false, nil
	}
	return err == nil && e.kind == entryKindFile, err
}

// IsDir reports whether a visible path is a directory.
func (o *OverlayFS) IsDir(p string) (bool, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	e, err := o.node(normalizePath(p))
	if errors.Is(err, stdfs.ErrNotExist) || errors.Is(err, syscall.ENOTDIR) {
		return false, nil
	}
	return err == nil && e.kind == entryKindDirectory, err
}

// IsSymlink rejects symlink paths and returns false for supported entries.
func (o *OverlayFS) IsSymlink(p string) (bool, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	_, err := o.node(normalizePath(p))
	if errors.Is(err, stdfs.ErrNotExist) || errors.Is(err, syscall.ENOTDIR) {
		return false, nil
	}
	return false, err
}

// ReadBytes reads the current merged file contents.
func (o *OverlayFS) ReadBytes(p string) ([]byte, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.read(normalizePath(p))
}

// ReadText reads the current file as UTF-8 text.
func (o *OverlayFS) ReadText(p string) (string, error) {
	data, err := o.ReadBytes(p)
	if err != nil {
		return "", err
	}
	if !utf8.Valid(data) {
		return "", overlayError("read_text", p, syscall.EILSEQ)
	}
	return string(data), nil
}

// WriteText stages a UTF-8 text file in the upper layer.
func (o *OverlayFS) WriteText(p, text string) (int, error) { return o.WriteBytes(p, []byte(text)) }

// WriteBytes stages a copy of data in the upper layer.
func (o *OverlayFS) WriteBytes(p string, data []byte) (int, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	p = normalizePath(p)
	parent, err := o.node(path.Dir(p))
	if err != nil {
		return 0, err
	}
	if parent.kind != entryKindDirectory {
		return 0, overlayError("write", p, syscall.ENOTDIR)
	}
	e, err := o.node(p)
	if err != nil && !errors.Is(err, stdfs.ErrNotExist) {
		return 0, err
	}
	if err == nil && e.kind == entryKindDirectory {
		return 0, overlayError("write", p, syscall.EISDIR)
	}
	o.upper.AddBytes(p, data)
	return len(data), nil
}

// Mkdir creates a directory in the upper layer.
func (o *OverlayFS) Mkdir(p string, parents, existOK bool) error {
	o.mu.Lock()
	defer o.mu.Unlock()
	p = normalizePath(p)
	if e, err := o.node(p); err == nil {
		if e.kind == entryKindDirectory && existOK {
			return nil
		}
		return overlayError("mkdir", p, stdfs.ErrExist)
	} else if !errors.Is(err, stdfs.ErrNotExist) {
		return err
	}
	current := path.Dir(p)
	for {
		e, err := o.node(current)
		if err == nil {
			if e.kind != entryKindDirectory {
				return overlayError("mkdir", current, syscall.ENOTDIR)
			}
			break
		}
		if !parents || !errors.Is(err, stdfs.ErrNotExist) {
			return err
		}
		if current == "/" {
			return err
		}
		current = path.Dir(current)
	}
	return o.upper.Mkdir(p, true, true)
}
func (o *OverlayFS) remove(p string) {
	o.upper.mu.Lock()
	delete(o.upper.entries, p)
	o.upper.mu.Unlock()
	o.whiteouts[p] = true
}

// Unlink hides a file without changing the lower layer.
func (o *OverlayFS) Unlink(p string) error {
	o.mu.Lock()
	defer o.mu.Unlock()
	p = normalizePath(p)
	e, err := o.node(p)
	if err != nil {
		return err
	}
	if e.kind == entryKindDirectory {
		return overlayError("unlink", p, syscall.EISDIR)
	}
	o.remove(p)
	return nil
}

// Rmdir hides an empty merged directory.
func (o *OverlayFS) Rmdir(p string) error {
	o.mu.Lock()
	defer o.mu.Unlock()
	p = normalizePath(p)
	if p == "/" {
		return overlayError("rmdir", p, stdfs.ErrPermission)
	}
	entries, err := o.iterdir(p)
	if err != nil {
		return err
	}
	if len(entries) > 0 {
		return overlayError("rmdir", p, syscall.ENOTEMPTY)
	}
	o.remove(p)
	return nil
}
func (o *OverlayFS) iterdir(p string) ([]string, error) {
	e, err := o.node(p)
	if err != nil {
		return nil, err
	}
	if e.kind != entryKindDirectory {
		return nil, overlayError("iterdir", p, syscall.ENOTDIR)
	}
	children := map[string]bool{}
	if !o.masked(p) {
		isDir, err := o.lower.IsDir(p)
		if err != nil {
			return nil, err
		}
		if isDir {
			names, err := o.lower.Iterdir(p)
			if err != nil {
				return nil, err
			}
			for _, name := range names {
				if !o.masked(name) {
					children[name] = true
				}
			}
		}
	}
	o.upper.mu.RLock()
	for name := range o.upper.entries {
		if name != p && path.Dir(name) == p {
			children[name] = true
		}
	}
	o.upper.mu.RUnlock()
	out := make([]string, 0, len(children))
	for name := range children {
		out = append(out, name)
	}
	sort.Strings(out)
	return out, nil
}

// Iterdir lists sorted direct children from both layers.
func (o *OverlayFS) Iterdir(p string) ([]string, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.iterdir(normalizePath(p))
}

// Stat reports metadata for the visible entry.
func (o *OverlayFS) Stat(p string) (monty.StatResult, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	e, err := o.node(normalizePath(p))
	if err != nil {
		return monty.StatResult{}, err
	}
	return e.stat, nil
}

// Resolve normalizes a path, rejecting traversal through symlinks.
func (o *OverlayFS) Resolve(p string) (string, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	p = normalizePath(p)
	_, err := o.node(p)
	if errors.Is(err, stdfs.ErrNotExist) {
		return p, nil
	}
	return p, err
}

// Absolute normalizes a sandbox path.
func (o *OverlayFS) Absolute(p string) (string, error) { return normalizePath(p), nil }

func (o *OverlayFS) copyTree(p string, entries map[string]*entry) error {
	e, err := o.node(p)
	if err != nil {
		return err
	}
	if e.kind == entryKindFile {
		e.data, err = o.read(p)
		if err != nil {
			return err
		}
	}
	entries[p] = e
	if e.kind == entryKindDirectory {
		children, err := o.iterdir(p)
		if err != nil {
			return err
		}
		for _, child := range children {
			if err := o.copyTree(child, entries); err != nil {
				return err
			}
		}
	}
	return nil
}

// Rename stages a move of the complete merged subtree. All reads and validation
// finish before upper or whiteouts change, so a failed move leaves the view intact.
func (o *OverlayFS) Rename(oldPath, newPath string) error {
	o.mu.Lock()
	defer o.mu.Unlock()
	oldPath = normalizePath(oldPath)
	newPath = normalizePath(newPath)
	if oldPath == "/" || newPath == "/" {
		return overlayError("rename", oldPath, stdfs.ErrPermission)
	}
	if strings.HasPrefix(newPath, oldPath+"/") {
		return overlayError("rename", newPath, syscall.EINVAL)
	}
	if _, err := o.node(oldPath); err != nil {
		return err
	}
	if _, err := o.node(newPath); err == nil {
		return overlayError("rename", newPath, stdfs.ErrExist)
	} else if !errors.Is(err, stdfs.ErrNotExist) {
		return err
	}
	parent, err := o.node(path.Dir(newPath))
	if err != nil {
		return err
	}
	if parent.kind != entryKindDirectory {
		return overlayError("rename", newPath, syscall.ENOTDIR)
	}
	staged := map[string]*entry{}
	if err := o.copyTree(oldPath, staged); err != nil {
		return err
	}
	// Materialize lower-only parents after the move has been fully validated.
	o.upper.mu.Lock()
	defer o.upper.mu.Unlock()
	o.upper.ensureParents(newPath)
	for name := range o.upper.entries {
		if name == oldPath || strings.HasPrefix(name, oldPath+"/") {
			delete(o.upper.entries, name)
		}
	}
	for name, e := range staged {
		o.upper.entries[newPath+strings.TrimPrefix(name, oldPath)] = e
	}
	o.whiteouts[oldPath] = true
	return nil
}

// Diff returns the sorted net changes from lower. Writing original contents back,
// or creating then deleting a path, removes it from the diff. Returned contents
// are independent copies. No operation applies this diff to the lower filesystem.
func (o *OverlayFS) Diff() ([]Change, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	candidates := map[string]bool{}
	o.upper.mu.RLock()
	for p := range o.upper.entries {
		if p != "/" {
			candidates[p] = true
		}
	}
	o.upper.mu.RUnlock()
	var collect func(string) error
	collect = func(p string) error {
		candidates[p] = true
		isDir, err := o.lower.IsDir(p)
		if err != nil {
			return err
		}
		if isDir {
			children, err := o.lower.Iterdir(p)
			if err != nil {
				return err
			}
			for _, child := range children {
				if err := collect(child); err != nil {
					return err
				}
			}
		}
		return nil
	}
	for p := range o.whiteouts {
		if err := collect(p); err != nil {
			return nil, err
		}
	}
	names := make([]string, 0, len(candidates))
	for p := range candidates {
		names = append(names, p)
	}
	sort.Strings(names)
	changes := make([]Change, 0)
	for _, p := range names {
		before, err := o.lower.Stat(p)
		beforeExists := err == nil
		if err != nil && !errors.Is(err, stdfs.ErrNotExist) && !errors.Is(err, syscall.ENOTDIR) {
			return nil, err
		}
		after, err := o.node(p)
		afterExists := err == nil
		if err != nil && !errors.Is(err, stdfs.ErrNotExist) && !errors.Is(err, syscall.ENOTDIR) {
			return nil, err
		}
		if !beforeExists && !afterExists {
			continue
		}
		change := Change{Path: p}
		if !afterExists {
			change.Kind = ChangeDeleted
			change.IsDir = before.Mode&0o170_000 == 0o040_000
			changes = append(changes, change)
			continue
		}
		change.IsDir = after.kind == entryKindDirectory
		if !change.IsDir {
			change.Data, err = o.read(p)
			if err != nil {
				return nil, err
			}
		}
		if !beforeExists {
			change.Kind = ChangeCreated
		} else {
			beforeDir := before.Mode&0o170_000 == 0o040_000
			if beforeDir && change.IsDir {
				continue
			}
			if !beforeDir && !change.IsDir {
				data, err := o.lower.ReadBytes(p)
				if err != nil {
					return nil, err
				}
				if bytes.Equal(data, change.Data) {
					continue
				}
			}
			change.Kind = ChangeModified
		}
		changes = append(changes, change)
	}
	return changes, nil
}
