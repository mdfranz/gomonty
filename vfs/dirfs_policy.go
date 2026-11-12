package vfs

import (
	"bytes"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	stdfs "io/fs"
	"math"
	"os"
	"path"
	"strings"
	"syscall"
)

// DirFSOptions restricts one DirFS instance. Zero size limits mean unlimited.
// Existing regular files count toward MaxTotalBytes; symlinks are not followed
// when measuring the tree. Limits do not coordinate separate mounts or host writers.
type DirFSOptions struct {
	ReadOnly      bool
	MaxFileBytes  int64
	MaxTotalBytes int64
	// Allow and Deny contain root-relative path.Match patterns (no leading slash).
	// An empty Allow permits all paths; Deny takes precedence. A matched directory
	// includes its descendants, except ".", which matches only the mount root.
	// To list root and the src tree, use Allow: []string{".", "src"}.
	Allow []string
	Deny  []string
}

func validateDirFSOptions(opts DirFSOptions) error {
	if opts.MaxFileBytes < 0 || opts.MaxTotalBytes < 0 {
		return fmt.Errorf("vfs: byte limits must be nonnegative")
	}
	for _, patterns := range [][]string{opts.Allow, opts.Deny} {
		for _, pattern := range patterns {
			if strings.HasPrefix(pattern, "/") || pattern == ".." || strings.HasPrefix(pattern, "../") {
				return fmt.Errorf("vfs: pattern must be relative to root: %q", pattern)
			}
			if _, err := path.Match(pattern, ""); err != nil {
				return fmt.Errorf("vfs: invalid pattern %q: %w", pattern, err)
			}
		}
	}
	return nil
}

func matchesPath(patterns []string, p string) bool {
	rel := strings.TrimPrefix(normalizePath(p), "/")
	if rel == "" {
		rel = "."
	}
	for {
		for _, pattern := range patterns {
			if ok, _ := path.Match(pattern, rel); ok {
				return true
			}
		}
		if rel == "." {
			return false
		}
		rel = path.Dir(rel)
		// A root match permits root itself, not every descendant.
		if rel == "." {
			return false
		}
	}
}

func (d *DirFS) permitted(p string) bool {
	return (len(d.options.Allow) == 0 || matchesPath(d.options.Allow, p)) && !matchesPath(d.options.Deny, p)
}

func (d *DirFS) checkAccess(op, p string, write, followFinal bool) error {
	if write && d.options.ReadOnly {
		return &stdfs.PathError{Op: op, Path: p, Err: fmt.Errorf("read-only filesystem: %w", stdfs.ErrPermission)}
	}
	if len(d.options.Allow) == 0 && len(d.options.Deny) == 0 {
		return nil
	}
	if !d.permitted(p) {
		return &stdfs.PathError{Op: op, Path: p, Err: fmt.Errorf("path policy denies access: %w", stdfs.ErrPermission)}
	}
	resolved, err := d.canonical(p, followFinal)
	if err != nil {
		return d.wrap(op, p, err)
	}
	if !d.permitted(resolved) {
		return &stdfs.PathError{Op: op, Path: p, Err: fmt.Errorf("path policy denies symlink target: %w", stdfs.ErrPermission)}
	}
	return nil
}

// canonical follows every symlink component using confined root operations.
// The final component may be absent (creation) or left unresolved (unlink/rename).
func (d *DirFS) canonical(p string, followFinal bool) (string, error) {
	current := normalizePath(p)
	for links := 0; links <= maxSymlinkDepth; links++ {
		parts := strings.Split(strings.TrimPrefix(current, "/"), "/")
		prefix := "/"
		changed := false
		for i, part := range parts {
			prefix = path.Join(prefix, part)
			if i == len(parts)-1 && !followFinal {
				break
			}
			info, err := d.root.Lstat(d.rel(prefix))
			if errors.Is(err, stdfs.ErrNotExist) {
				return current, nil
			}
			if err != nil {
				return "", err
			}
			if info.Mode()&stdfs.ModeSymlink == 0 {
				continue
			}
			target, err := d.root.Readlink(d.rel(prefix))
			if err != nil {
				return "", err
			}
			next, ok := joinWithin(path.Dir(prefix), target)
			if !ok {
				return "", errOutsideRoot
			}
			if i+1 < len(parts) {
				next = path.Join(next, strings.Join(parts[i+1:], "/"))
			}
			current = next
			changed = true
			break
		}
		if !changed {
			return current, nil
		}
	}
	return "", syscall.ELOOP
}

func (d *DirFS) checkSize(p string, size, oldSize int64) error {
	if d.options.MaxFileBytes > 0 && size > d.options.MaxFileBytes {
		return &stdfs.PathError{Op: "write", Path: p, Err: fmt.Errorf("MaxFileBytes limit (%d) exceeded", d.options.MaxFileBytes)}
	}
	if d.options.MaxTotalBytes <= 0 {
		return nil
	}
	var total int64
	err := stdfs.WalkDir(d.root.FS(), ".", func(_ string, e stdfs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if e.Type().IsRegular() {
			info, err := e.Info()
			if err != nil {
				return err
			}
			if info.Size() > math.MaxInt64-total {
				return fmt.Errorf("MaxTotalBytes accounting overflow")
			}
			total += info.Size()
		}
		return nil
	})
	if err != nil {
		return d.wrap("write", p, err)
	}
	if total-oldSize > d.options.MaxTotalBytes-size {
		return &stdfs.PathError{Op: "write", Path: p, Err: fmt.Errorf("MaxTotalBytes limit (%d) exceeded", d.options.MaxTotalBytes)}
	}
	return nil
}

// atomicWrite keeps a partial staging file invisible at the destination. The
// reader abstraction also permits testing a failure after some bytes were written.
func (d *DirFS) atomicWrite(rel string, data io.Reader, mode stdfs.FileMode) error {
	name := path.Join(path.Dir(rel), ".gomonty-"+rand.Text())
	file, err := d.root.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode)
	if err != nil {
		return err
	}
	defer d.root.Remove(name)
	_, writeErr := io.Copy(file, data)
	closeErr := file.Close()
	if writeErr != nil {
		return writeErr
	}
	if closeErr != nil {
		return closeErr
	}
	return d.root.Rename(name, rel)
}

func (d *DirFS) writeLimited(p string, data []byte) (int, error) {
	if err := d.checkAccess("write", p, true, true); err != nil {
		return 0, err
	}
	target, err := d.canonical(p, true)
	if err != nil {
		return 0, d.wrap("write", p, err)
	}
	if target == "/" {
		return 0, &stdfs.PathError{Op: "write", Path: p, Err: syscall.EISDIR}
	}
	rel := d.rel(target)
	info, err := d.root.Stat(rel)
	var oldSize int64
	mode := stdfs.FileMode(0o644)
	if err == nil {
		if info.IsDir() {
			return 0, &stdfs.PathError{Op: "write", Path: p, Err: syscall.EISDIR}
		}
		if !info.Mode().IsRegular() {
			return 0, &stdfs.PathError{Op: "write", Path: p, Err: syscall.EINVAL}
		}
		oldSize = info.Size()
		mode = info.Mode().Perm()
	} else if !errors.Is(err, stdfs.ErrNotExist) {
		return 0, d.wrap("write", p, err)
	}
	if err := d.checkSize(p, int64(len(data)), oldSize); err != nil {
		return 0, err
	}
	if err := d.atomicWrite(rel, bytes.NewReader(data), mode); err != nil {
		return 0, d.wrap("write", p, err)
	}
	return len(data), nil
}

// checkRenameTree prevents a directory rename from bypassing policies on its
// descendants by moving them under a different allowed prefix.
func (d *DirFS) checkRenameTree(oldPath, newPath string) error {
	if len(d.options.Allow) == 0 && len(d.options.Deny) == 0 {
		return nil
	}
	source, err := d.canonical(oldPath, false)
	if err != nil {
		return d.wrap("rename", oldPath, err)
	}
	rel := strings.TrimPrefix(source, "/")
	return stdfs.WalkDir(d.root.FS(), rel, func(name string, _ stdfs.DirEntry, err error) error {
		if err != nil {
			return d.wrap("rename", oldPath, err)
		}
		if err := d.checkAccess("rename", "/"+name, true, false); err != nil {
			return err
		}
		suffix := strings.TrimPrefix(name, rel)
		return d.checkAccess("rename", normalizePath(newPath)+suffix, true, false)
	})
}
