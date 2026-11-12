package vfs

import (
	"errors"
	"fmt"
	stdfs "io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"syscall"
	"unicode/utf8"

	"github.com/mdfranz/gomonty"
)

// maxSymlinkDepth bounds how many symlinks Resolve follows before reporting ELOOP.
const maxSymlinkDepth = 40

// errOutsideRoot is returned when a path, or a symlink on the way to it,
// resolves outside the mounted directory. It wraps fs.ErrPermission.
var errOutsideRoot = fmt.Errorf("resolves outside the mounted directory: %w", stdfs.ErrPermission)

// DirFS is a FileSystem backed by a real directory on the host.
//
// Sandbox paths are POSIX paths rooted at "/", and "/" is the mounted
// directory. Paths are cleaned like MemoryFS cleans them, so ".." can never
// climb above "/": "/../etc/passwd" is "/etc/passwd" inside the mount.
//
// Symlinks inside the directory work when their target stays inside it.
// A symlink that resolves outside the directory, whether absolute or through
// "..", fails with an error that wraps fs.ErrPermission. Confinement is
// enforced by os.Root, which checks each path component as it opens it, so a
// symlink swapped in after a check cannot redirect an operation.
//
// Errors report sandbox paths, never host paths. UID, GID, inode and device
// numbers are not exposed in Stat results.
//
// DirFS does not defend against another host process that rewrites the
// directory concurrently beyond keeping operations inside the mount. Policies
// and size limits coordinate only operations through this instance.
//
// A DirFS is safe for concurrent use. Call Close when finished.
//
// Mutating operations and their policy checks are serialized. Reads run
// concurrently with one another and wait for mutations, so staged files and
// intermediate policy checks are not visible through this instance.
type DirFS struct {
	root    *os.Root
	options DirFSOptions
	mu      sync.RWMutex // coordinates policy checks and mutations
}

var _ FileSystem = (*DirFS)(nil)

// NewDirFS mounts the host directory dir as the sandbox's "/". Optional
// DirFSOptions restrict writes, byte sizes, and allowed paths; omitted options
// leave the mount unrestricted.
func NewDirFS(dir string, options ...DirFSOptions) (*DirFS, error) {
	var opts DirFSOptions
	if len(options) > 1 {
		return nil, fmt.Errorf("vfs: expected at most one DirFSOptions")
	}
	if len(options) == 1 {
		opts = options[0]
	}
	if err := validateDirFSOptions(opts); err != nil {
		return nil, err
	}
	opts.Allow = append([]string(nil), opts.Allow...)
	opts.Deny = append([]string(nil), opts.Deny...)
	root, err := os.OpenRoot(dir)
	if err != nil {
		return nil, err
	}
	info, err := root.Stat(".")
	if err != nil {
		_ = root.Close()
		return nil, err
	}
	if !info.IsDir() {
		_ = root.Close()
		return nil, fmt.Errorf("vfs: %s is not a directory", dir)
	}
	return &DirFS{root: root, options: opts}, nil
}

// Close releases the directory handle. The DirFS must not be used afterwards.
func (d *DirFS) Close() error {
	return d.root.Close()
}

// Exists reports whether the path exists, following symlinks.
func (d *DirFS) Exists(p string) (bool, error) {
	_, found, err := d.statIfPresent(p, true)
	return found, err
}

// IsFile reports whether the path is a regular file, following symlinks.
func (d *DirFS) IsFile(p string) (bool, error) {
	info, found, err := d.statIfPresent(p, true)
	return found && info.Mode().IsRegular(), err
}

// IsDir reports whether the path is a directory, following symlinks.
func (d *DirFS) IsDir(p string) (bool, error) {
	info, found, err := d.statIfPresent(p, true)
	return found && info.IsDir(), err
}

// IsSymlink reports whether the path itself is a symlink.
func (d *DirFS) IsSymlink(p string) (bool, error) {
	info, found, err := d.statIfPresent(p, false)
	return found && info.Mode()&stdfs.ModeSymlink != 0, err
}

// ReadText reads a UTF-8 file as text.
func (d *DirFS) ReadText(p string) (string, error) {
	data, err := d.readFile("read_text", p)
	if err != nil {
		return "", err
	}
	if !utf8.Valid(data) {
		return "", &stdfs.PathError{Op: "read_text", Path: p, Err: syscall.EILSEQ}
	}
	return string(data), nil
}

// ReadBytes reads a file as raw bytes.
func (d *DirFS) ReadBytes(p string) ([]byte, error) {
	return d.readFile("read", p)
}

// WriteText writes a UTF-8 text file, creating or truncating it.
func (d *DirFS) WriteText(p string, data string) (int, error) {
	return d.writeFile(p, []byte(data))
}

// WriteBytes writes a binary file, creating or truncating it.
func (d *DirFS) WriteBytes(p string, data []byte) (int, error) {
	return d.writeFile(p, data)
}

// Mkdir creates a directory.
func (d *DirFS) Mkdir(p string, parents bool, existOK bool) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if err := d.checkAccess("mkdir", p, true, true); err != nil {
		return err
	}
	if normalizePath(p) == "/" {
		return nil
	}
	rel := d.rel(p)
	info, err := d.root.Stat(rel)
	switch {
	case err == nil:
		if info.IsDir() && existOK {
			return nil
		}
		return &stdfs.PathError{Op: "mkdir", Path: p, Err: stdfs.ErrExist}
	case isEscape(err):
		return d.wrap("mkdir", p, err)
	}
	if parents {
		err = d.root.MkdirAll(rel, 0o755)
	} else {
		err = d.root.Mkdir(rel, 0o755)
	}
	return d.wrap("mkdir", p, err)
}

// Unlink removes a file or symlink. It never follows a final symlink and
// refuses directories.
func (d *DirFS) Unlink(p string) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if err := d.checkAccess("unlink", p, true, false); err != nil {
		return err
	}
	info, err := d.root.Lstat(d.rel(p))
	if err != nil {
		return d.wrap("unlink", p, err)
	}
	if info.IsDir() {
		return &stdfs.PathError{Op: "unlink", Path: p, Err: syscall.EISDIR}
	}
	return d.wrap("unlink", p, d.root.Remove(d.rel(p)))
}

// Rmdir removes an empty directory. The mount root cannot be removed.
func (d *DirFS) Rmdir(p string) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if err := d.checkAccess("rmdir", p, true, false); err != nil {
		return err
	}
	if normalizePath(p) == "/" {
		return &stdfs.PathError{Op: "rmdir", Path: p, Err: stdfs.ErrPermission}
	}
	info, err := d.root.Lstat(d.rel(p))
	if err != nil {
		return d.wrap("rmdir", p, err)
	}
	if !info.IsDir() {
		return &stdfs.PathError{Op: "rmdir", Path: p, Err: syscall.ENOTDIR}
	}
	return d.wrap("rmdir", p, d.root.Remove(d.rel(p)))
}

// Iterdir lists the direct children of a directory as sandbox paths.
func (d *DirFS) Iterdir(p string) ([]string, error) {
	d.mu.RLock()
	defer d.mu.RUnlock()
	if err := d.checkAccess("iterdir", p, false, true); err != nil {
		return nil, err
	}
	rel := d.rel(p)
	info, err := d.root.Stat(rel)
	if err != nil {
		return nil, d.wrap("iterdir", p, err)
	}
	if !info.IsDir() {
		return nil, &stdfs.PathError{Op: "iterdir", Path: p, Err: syscall.ENOTDIR}
	}
	dir, err := d.root.Open(rel)
	if err != nil {
		return nil, d.wrap("iterdir", p, err)
	}
	defer dir.Close()
	entries, err := dir.ReadDir(-1)
	if err != nil {
		return nil, d.wrap("iterdir", p, err)
	}
	base := normalizePath(p)
	children := make([]string, 0, len(entries))
	for _, entry := range entries {
		child := path.Join(base, entry.Name())
		if d.checkAccess("iterdir", child, false, true) == nil {
			children = append(children, child)
		}
	}
	sort.Strings(children)
	return children, nil
}

// Stat returns stat metadata for a path, following symlinks.
func (d *DirFS) Stat(p string) (monty.StatResult, error) {
	d.mu.RLock()
	defer d.mu.RUnlock()
	if err := d.checkAccess("stat", p, false, true); err != nil {
		return monty.StatResult{}, err
	}
	info, err := d.root.Stat(d.rel(p))
	if err != nil {
		return monty.StatResult{}, d.wrap("stat", p, err)
	}
	mode := int64(info.Mode().Perm())
	if info.IsDir() {
		return DirStat(mode, info.ModTime()), nil
	}
	return FileStat(info.Size(), mode, info.ModTime()), nil
}

// Rename renames a file or directory. It fails if the destination exists.
func (d *DirFS) Rename(oldPath string, newPath string) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if err := d.checkAccess("rename", oldPath, true, false); err != nil {
		return err
	}
	if err := d.checkAccess("rename", newPath, true, false); err != nil {
		return err
	}
	if normalizePath(oldPath) == "/" || normalizePath(newPath) == "/" {
		return &stdfs.PathError{Op: "rename", Path: oldPath, Err: stdfs.ErrPermission}
	}
	if _, err := d.root.Lstat(d.rel(oldPath)); err != nil {
		return d.wrap("rename", oldPath, err)
	}
	_, err := d.root.Lstat(d.rel(newPath))
	switch {
	case err == nil:
		return &stdfs.PathError{Op: "rename", Path: newPath, Err: stdfs.ErrExist}
	case isEscape(err):
		return d.wrap("rename", newPath, err)
	}
	if err := d.checkRenameTree(oldPath, newPath); err != nil {
		return err
	}
	return d.wrap("rename", oldPath, d.root.Rename(d.rel(oldPath), d.rel(newPath)))
}

// Resolve returns the absolute sandbox path with a final symlink followed
// repeatedly. Like MemoryFS, it resolves only the last path component. A link
// that points outside the mount fails with fs.ErrPermission.
func (d *DirFS) Resolve(p string) (string, error) {
	d.mu.RLock()
	defer d.mu.RUnlock()
	if err := d.checkAccess("resolve", p, false, true); err != nil {
		return "", err
	}
	current := normalizePath(p)
	for range maxSymlinkDepth {
		if current == "/" {
			return current, nil
		}
		target, err := d.root.Readlink(d.rel(current))
		if err != nil {
			if isEscape(err) {
				return "", d.wrap("resolve", p, err)
			}
			// Not a symlink, or missing: nothing more to follow.
			return current, nil
		}
		next, ok := joinWithin(path.Dir(current), target)
		if !ok {
			return "", &stdfs.PathError{Op: "resolve", Path: p, Err: errOutsideRoot}
		}
		current = next
	}
	return "", &stdfs.PathError{Op: "resolve", Path: p, Err: syscall.ELOOP}
}

// Absolute normalizes the path into Monty's absolute POSIX form.
func (d *DirFS) Absolute(p string) (string, error) {
	d.mu.RLock()
	defer d.mu.RUnlock()
	if err := d.checkAccess("absolute", p, false, false); err != nil {
		return "", err
	}
	return normalizePath(p), nil
}

func (d *DirFS) readFile(op, p string) ([]byte, error) {
	d.mu.RLock()
	defer d.mu.RUnlock()
	if err := d.checkAccess(op, p, false, true); err != nil {
		return nil, err
	}
	rel := d.rel(p)
	info, err := d.root.Stat(rel)
	if err != nil {
		return nil, d.wrap(op, p, err)
	}
	if info.IsDir() {
		return nil, &stdfs.PathError{Op: op, Path: p, Err: syscall.EISDIR}
	}
	// Opening a FIFO or device inside the mount could block or misbehave.
	if !info.Mode().IsRegular() {
		return nil, &stdfs.PathError{Op: op, Path: p, Err: syscall.EINVAL}
	}
	data, err := d.root.ReadFile(rel)
	if err != nil {
		return nil, d.wrap(op, p, err)
	}
	return data, nil
}

func (d *DirFS) writeFile(p string, data []byte) (int, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.writeLimited(p, data)
}

// statIfPresent stats the path. A path that is missing, has a non-directory
// component, or loops reports found=false with no error, matching pathlib's
// exists(); every other failure, including an escape, is returned.
func (d *DirFS) statIfPresent(p string, follow bool) (info stdfs.FileInfo, found bool, err error) {
	d.mu.RLock()
	defer d.mu.RUnlock()
	if err := d.checkAccess("stat", p, false, follow); err != nil {
		return nil, false, err
	}
	if follow {
		info, err = d.root.Stat(d.rel(p))
	} else {
		info, err = d.root.Lstat(d.rel(p))
	}
	switch {
	case err == nil:
		return info, true, nil
	case errors.Is(err, stdfs.ErrNotExist),
		errors.Is(err, syscall.ENOTDIR),
		errors.Is(err, syscall.ELOOP):
		return nil, false, nil
	}
	return nil, false, d.wrap("stat", p, err)
}

// rel converts a sandbox path to a path relative to the mounted directory.
func (d *DirFS) rel(p string) string {
	normalized := normalizePath(p)
	if normalized == "/" {
		return "."
	}
	return filepath.FromSlash(normalized[1:])
}

// wrap rewrites an os error so it reports the sandbox path and never a host
// path, and turns os.Root's escape error into one wrapping fs.ErrPermission.
func (d *DirFS) wrap(op, p string, err error) error {
	if err == nil {
		return nil
	}
	if isEscape(err) {
		return &stdfs.PathError{Op: op, Path: p, Err: errOutsideRoot}
	}
	var pathErr *stdfs.PathError
	if errors.As(err, &pathErr) {
		return &stdfs.PathError{Op: op, Path: p, Err: pathErr.Err}
	}
	var linkErr *os.LinkError
	if errors.As(err, &linkErr) {
		return &stdfs.PathError{Op: op, Path: p, Err: linkErr.Err}
	}
	return &stdfs.PathError{Op: op, Path: p, Err: err}
}

// isEscape reports whether err is os.Root refusing a path that leaves the
// root. The standard library does not export this error, so match its text;
// TestDirFSSymlinkEscapes fails if a Go release changes it.
func isEscape(err error) bool {
	return err != nil && strings.Contains(err.Error(), "path escapes from parent")
}

// joinWithin resolves a symlink target against dir without clamping: it
// reports false if the target is absolute or climbs above "/".
func joinWithin(dir string, target string) (string, bool) {
	target = filepath.ToSlash(target)
	if strings.HasPrefix(target, "/") || filepath.IsAbs(target) {
		return "", false
	}
	var parts []string
	for _, part := range strings.Split(dir, "/") {
		if part != "" {
			parts = append(parts, part)
		}
	}
	for _, part := range strings.Split(target, "/") {
		switch part {
		case "", ".":
		case "..":
			if len(parts) == 0 {
				return "", false
			}
			parts = parts[:len(parts)-1]
		default:
			parts = append(parts, part)
		}
	}
	return "/" + strings.Join(parts, "/"), true
}
