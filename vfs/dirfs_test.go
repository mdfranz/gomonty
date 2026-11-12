package vfs

import (
	"errors"
	stdfs "io/fs"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

const secretText = "top secret"

// dirFixture is a mounted directory next to a sibling "outside" directory
// holding a secret that the mount must never expose or modify.
type dirFixture struct {
	base    string
	root    string
	outside string
	fs      *DirFS
}

func newDirFixture(t *testing.T) *dirFixture {
	t.Helper()
	base := t.TempDir()
	root := filepath.Join(base, "root")
	outside := filepath.Join(base, "outside")
	for _, dir := range []string{root, outside} {
		if err := os.Mkdir(dir, 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", dir, err)
		}
	}
	if err := os.WriteFile(filepath.Join(outside, "secret.txt"), []byte(secretText), 0o644); err != nil {
		t.Fatalf("write secret: %v", err)
	}
	mounted, err := NewDirFS(root)
	if err != nil {
		t.Fatalf("NewDirFS: %v", err)
	}
	t.Cleanup(func() { _ = mounted.Close() })
	return &dirFixture{base: base, root: root, outside: outside, fs: mounted}
}

// symlink creates root/name -> target, skipping the test where the platform
// cannot create symlinks.
func (f *dirFixture) symlink(t *testing.T, target string, name string) {
	t.Helper()
	if err := os.Symlink(target, filepath.Join(f.root, name)); err != nil {
		t.Skipf("cannot create symlink: %v", err)
	}
}

func (f *dirFixture) write(t *testing.T, name string, content string) {
	t.Helper()
	full := filepath.Join(f.root, name)
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
}

// requireOutsideUntouched fails if anything beyond the secret appeared in, or
// the secret changed within, the directory outside the mount.
func (f *dirFixture) requireOutsideUntouched(t *testing.T) {
	t.Helper()
	entries, err := os.ReadDir(f.outside)
	if err != nil {
		t.Fatalf("read outside: %v", err)
	}
	if len(entries) != 1 || entries[0].Name() != "secret.txt" {
		names := make([]string, 0, len(entries))
		for _, entry := range entries {
			names = append(names, entry.Name())
		}
		t.Fatalf("outside directory was modified: %v", names)
	}
	data, err := os.ReadFile(filepath.Join(f.outside, "secret.txt"))
	if err != nil {
		t.Fatalf("read secret: %v", err)
	}
	if string(data) != secretText {
		t.Fatalf("secret was modified: %q", data)
	}
}

func requireErrorIs(t *testing.T, err error, target error) {
	t.Helper()
	if !errors.Is(err, target) {
		t.Fatalf("got error %v, want one wrapping %v", err, target)
	}
}

func TestNewDirFSRejectsBadRoots(t *testing.T) {
	base := t.TempDir()
	if _, err := NewDirFS(filepath.Join(base, "missing")); err == nil {
		t.Fatal("NewDirFS accepted a missing directory")
	}
	file := filepath.Join(base, "file.txt")
	if err := os.WriteFile(file, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := NewDirFS(file); err == nil {
		t.Fatal("NewDirFS accepted a file")
	}
}

func TestDirFSReadWriteRoundTrip(t *testing.T) {
	f := newDirFixture(t)
	f.write(t, "config.txt", "alpha")

	text, err := f.fs.ReadText("/config.txt")
	if err != nil || text != "alpha" {
		t.Fatalf("ReadText = %q, %v", text, err)
	}
	if err := f.fs.Mkdir("/data", false, false); err != nil {
		t.Fatalf("Mkdir: %v", err)
	}
	if n, err := f.fs.WriteText("/data/out.txt", "beta"); err != nil || n != 4 {
		t.Fatalf("WriteText = %d, %v", n, err)
	}
	if _, err := f.fs.WriteBytes("/data/raw.bin", []byte{0xff, 0x00, 0x01}); err != nil {
		t.Fatalf("WriteBytes: %v", err)
	}
	raw, err := f.fs.ReadBytes("/data/raw.bin")
	if err != nil || len(raw) != 3 || raw[0] != 0xff {
		t.Fatalf("ReadBytes = %v, %v", raw, err)
	}
	// The write really landed on the host, inside the mounted directory.
	hostData, err := os.ReadFile(filepath.Join(f.root, "data", "out.txt"))
	if err != nil || string(hostData) != "beta" {
		t.Fatalf("host file = %q, %v", hostData, err)
	}

	entries, err := f.fs.Iterdir("/data")
	if err != nil {
		t.Fatalf("Iterdir: %v", err)
	}
	if len(entries) != 2 || entries[0] != "/data/out.txt" || entries[1] != "/data/raw.bin" {
		t.Fatalf("Iterdir = %#v", entries)
	}
	rootEntries, err := f.fs.Iterdir("/")
	if err != nil || len(rootEntries) != 2 || rootEntries[0] != "/config.txt" || rootEntries[1] != "/data" {
		t.Fatalf("Iterdir(/) = %#v, %v", rootEntries, err)
	}

	if err := f.fs.Rename("/data/out.txt", "/data/result.txt"); err != nil {
		t.Fatalf("Rename: %v", err)
	}
	if exists, _ := f.fs.Exists("/data/out.txt"); exists {
		t.Fatal("old name still exists after Rename")
	}
	if err := f.fs.Unlink("/data/result.txt"); err != nil {
		t.Fatalf("Unlink: %v", err)
	}
	if exists, _ := f.fs.Exists("/data/result.txt"); exists {
		t.Fatal("file still exists after Unlink")
	}
	f.requireOutsideUntouched(t)
}

func TestDirFSPredicatesAndStat(t *testing.T) {
	f := newDirFixture(t)
	f.write(t, "dir/file.txt", "hello")

	for _, tc := range []struct {
		path                  string
		exists, isFile, isDir bool
	}{
		{"/dir", true, false, true},
		{"/dir/file.txt", true, true, false},
		{"/dir/missing", false, false, false},
		{"/dir/file.txt/child", false, false, false}, // component is a file
		{"/", true, false, true},
	} {
		exists, err := f.fs.Exists(tc.path)
		if err != nil || exists != tc.exists {
			t.Errorf("Exists(%s) = %v, %v", tc.path, exists, err)
		}
		isFile, err := f.fs.IsFile(tc.path)
		if err != nil || isFile != tc.isFile {
			t.Errorf("IsFile(%s) = %v, %v", tc.path, isFile, err)
		}
		isDir, err := f.fs.IsDir(tc.path)
		if err != nil || isDir != tc.isDir {
			t.Errorf("IsDir(%s) = %v, %v", tc.path, isDir, err)
		}
	}

	stat, err := f.fs.Stat("/dir/file.txt")
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if stat.Size != 5 || stat.Mode&0o170000 != 0o100000 {
		t.Fatalf("Stat file = %+v", stat)
	}
	if stat.UID != 0 || stat.GID != 0 || stat.Ino != 0 || stat.Dev != 0 {
		t.Fatalf("Stat exposed host identity: %+v", stat)
	}
	dirStat, err := f.fs.Stat("/dir")
	if err != nil || dirStat.Mode&0o170000 != 0o040000 {
		t.Fatalf("Stat dir = %+v, %v", dirStat, err)
	}
}

func TestDirFSMkdir(t *testing.T) {
	f := newDirFixture(t)

	if err := f.fs.Mkdir("/a/b/c", true, false); err != nil {
		t.Fatalf("Mkdir parents: %v", err)
	}
	if isDir, _ := f.fs.IsDir("/a/b/c"); !isDir {
		t.Fatal("parents were not created")
	}
	requireErrorIs(t, f.fs.Mkdir("/a/b/c", true, false), stdfs.ErrExist)
	if err := f.fs.Mkdir("/a/b/c", true, true); err != nil {
		t.Fatalf("Mkdir existOK: %v", err)
	}
	requireErrorIs(t, f.fs.Mkdir("/x/y", false, false), stdfs.ErrNotExist)
	if err := f.fs.Mkdir("/", false, false); err != nil {
		t.Fatalf("Mkdir(/): %v", err)
	}
	f.write(t, "file.txt", "x")
	requireErrorIs(t, f.fs.Mkdir("/file.txt", false, true), stdfs.ErrExist)
}

func TestDirFSErrorKinds(t *testing.T) {
	f := newDirFixture(t)
	f.write(t, "dir/file.txt", "x")
	f.write(t, "bad.txt", "")
	if err := os.WriteFile(filepath.Join(f.root, "bad.txt"), []byte{0xff, 0xfe}, 0o644); err != nil {
		t.Fatal(err)
	}

	_, err := f.fs.ReadText("/missing.txt")
	requireErrorIs(t, err, stdfs.ErrNotExist)
	_, err = f.fs.ReadText("/dir")
	requireErrorIs(t, err, syscall.EISDIR)
	_, err = f.fs.ReadText("/bad.txt")
	requireErrorIs(t, err, syscall.EILSEQ)
	_, err = f.fs.ReadBytes("/bad.txt") // raw bytes are fine
	if err != nil {
		t.Fatalf("ReadBytes on non-UTF-8 file: %v", err)
	}
	_, err = f.fs.Iterdir("/dir/file.txt")
	requireErrorIs(t, err, syscall.ENOTDIR)
	_, err = f.fs.Iterdir("/missing")
	requireErrorIs(t, err, stdfs.ErrNotExist)
	_, err = f.fs.WriteText("/dir", "x")
	requireErrorIs(t, err, syscall.EISDIR)
	_, err = f.fs.WriteText("/nodir/file.txt", "x")
	requireErrorIs(t, err, stdfs.ErrNotExist)
	_, err = f.fs.WriteText("/dir/file.txt/child", "x")
	requireErrorIs(t, err, syscall.ENOTDIR)
	requireErrorIs(t, f.fs.Unlink("/dir"), syscall.EISDIR)
	requireErrorIs(t, f.fs.Unlink("/missing"), stdfs.ErrNotExist)
	requireErrorIs(t, f.fs.Rmdir("/dir/file.txt"), syscall.ENOTDIR)
	requireErrorIs(t, f.fs.Rmdir("/"), stdfs.ErrPermission)
	requireErrorIs(t, f.fs.Rename("/missing", "/other"), stdfs.ErrNotExist)
	requireErrorIs(t, f.fs.Rename("/dir", "/bad.txt"), stdfs.ErrExist)
	requireErrorIs(t, f.fs.Rename("/", "/other"), stdfs.ErrPermission)
	if _, err := f.fs.Stat("/missing"); !errors.Is(err, stdfs.ErrNotExist) {
		t.Fatalf("Stat(missing) = %v", err)
	}
	if err := f.fs.Rmdir("/dir"); err == nil {
		t.Fatal("Rmdir removed a non-empty directory")
	}
	f.requireOutsideUntouched(t)
}

// ".." in a sandbox path is clamped at "/", exactly as MemoryFS does, so it
// can never reach the sibling directory.
func TestDirFSDotDotCannotLeave(t *testing.T) {
	f := newDirFixture(t)
	f.write(t, "a/inside.txt", "inside")

	for _, p := range []string{
		"/../outside/secret.txt",
		"../outside/secret.txt",
		"/a/../../outside/secret.txt",
		"/a/../../../../../../outside/secret.txt",
	} {
		_, err := f.fs.ReadText(p)
		requireErrorIs(t, err, stdfs.ErrNotExist)
		if _, err := f.fs.WriteText(p, "pwned"); err == nil {
			// A write may succeed only by landing inside the mount.
			if _, statErr := os.Stat(filepath.Join(f.outside, "pwned")); statErr == nil {
				t.Fatalf("write through %q escaped the mount", p)
			}
		}
	}
	text, err := f.fs.ReadText("/a/../a/inside.txt")
	if err != nil || text != "inside" {
		t.Fatalf("legitimate .. path = %q, %v", text, err)
	}
	f.requireOutsideUntouched(t)
}

func TestDirFSSymlinkEscapes(t *testing.T) {
	f := newDirFixture(t)
	f.symlink(t, filepath.Join(f.outside, "secret.txt"), "abs-file")
	f.symlink(t, filepath.Join("..", "outside", "secret.txt"), "rel-file")
	f.symlink(t, f.outside, "abs-dir")
	f.symlink(t, filepath.Join("..", "outside"), "rel-dir")
	f.write(t, "movable.txt", "m")

	cases := []struct {
		name string
		run  func() error
	}{
		{"read abs file link", func() error { _, err := f.fs.ReadText("/abs-file"); return err }},
		{"read rel file link", func() error { _, err := f.fs.ReadText("/rel-file"); return err }},
		{"read bytes abs file link", func() error { _, err := f.fs.ReadBytes("/abs-file"); return err }},
		{"stat abs file link", func() error { _, err := f.fs.Stat("/abs-file"); return err }},
		{"exists abs file link", func() error { _, err := f.fs.Exists("/abs-file"); return err }},
		{"is file abs file link", func() error { _, err := f.fs.IsFile("/rel-file"); return err }},
		{"write abs file link", func() error { _, err := f.fs.WriteText("/abs-file", "pwned"); return err }},
		{"write rel file link", func() error { _, err := f.fs.WriteBytes("/rel-file", []byte("pwned")); return err }},
		{"read through abs dir link", func() error { _, err := f.fs.ReadText("/abs-dir/secret.txt"); return err }},
		{"read through rel dir link", func() error { _, err := f.fs.ReadText("/rel-dir/secret.txt"); return err }},
		{"list abs dir link", func() error { _, err := f.fs.Iterdir("/abs-dir"); return err }},
		{"list rel dir link", func() error { _, err := f.fs.Iterdir("/rel-dir"); return err }},
		{"write through dir link", func() error { _, err := f.fs.WriteText("/abs-dir/new.txt", "pwned"); return err }},
		{"mkdir through dir link", func() error { return f.fs.Mkdir("/abs-dir/sub", false, false) }},
		{"mkdir parents through dir link", func() error { return f.fs.Mkdir("/rel-dir/a/b", true, false) }},
		{"unlink through dir link", func() error { return f.fs.Unlink("/abs-dir/secret.txt") }},
		{"rmdir through dir link", func() error { return f.fs.Rmdir("/abs-dir/sub") }},
		{"rename into dir link", func() error { return f.fs.Rename("/movable.txt", "/abs-dir/movable.txt") }},
		{"rename out of dir link", func() error { return f.fs.Rename("/abs-dir/secret.txt", "/stolen.txt") }},
		{"resolve abs file link", func() error { _, err := f.fs.Resolve("/abs-file"); return err }},
		{"resolve rel file link", func() error { _, err := f.fs.Resolve("/rel-file"); return err }},
		{"resolve through dir link", func() error { _, err := f.fs.Resolve("/abs-dir/secret.txt"); return err }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			requireErrorIs(t, tc.run(), stdfs.ErrPermission)
		})
	}

	// Nothing above may have changed the outside directory or moved files out.
	f.requireOutsideUntouched(t)
	if _, err := os.Stat(filepath.Join(f.root, "stolen.txt")); err == nil {
		t.Fatal("a file was moved out of the outside directory")
	}
	if _, err := os.Stat(filepath.Join(f.root, "movable.txt")); err != nil {
		t.Fatalf("movable.txt left the mount: %v", err)
	}
}

func TestDirFSSymlinksInsideRootWork(t *testing.T) {
	f := newDirFixture(t)
	f.write(t, "real/target.txt", "payload")
	f.symlink(t, filepath.Join("real", "target.txt"), "link.txt")
	f.symlink(t, "real", "dirlink")
	f.symlink(t, "link.txt", "chain.txt")

	text, err := f.fs.ReadText("/link.txt")
	if err != nil || text != "payload" {
		t.Fatalf("ReadText through link = %q, %v", text, err)
	}
	text, err = f.fs.ReadText("/dirlink/target.txt")
	if err != nil || text != "payload" {
		t.Fatalf("ReadText through dir link = %q, %v", text, err)
	}
	if isLink, err := f.fs.IsSymlink("/link.txt"); err != nil || !isLink {
		t.Fatalf("IsSymlink(link) = %v, %v", isLink, err)
	}
	if isLink, _ := f.fs.IsSymlink("/real/target.txt"); isLink {
		t.Fatal("IsSymlink true for a regular file")
	}
	resolved, err := f.fs.Resolve("/chain.txt")
	if err != nil || resolved != "/real/target.txt" {
		t.Fatalf("Resolve chain = %q, %v", resolved, err)
	}
	if resolved, err := f.fs.Resolve("/real/target.txt"); err != nil || resolved != "/real/target.txt" {
		t.Fatalf("Resolve plain = %q, %v", resolved, err)
	}
	if resolved, err := f.fs.Resolve("/missing"); err != nil || resolved != "/missing" {
		t.Fatalf("Resolve missing = %q, %v", resolved, err)
	}

	// Unlink removes the link, not what it points to.
	if err := f.fs.Unlink("/link.txt"); err != nil {
		t.Fatalf("Unlink link: %v", err)
	}
	if text, err := f.fs.ReadText("/real/target.txt"); err != nil || text != "payload" {
		t.Fatalf("target changed after unlinking link: %q, %v", text, err)
	}
}

func TestDirFSSymlinkLoop(t *testing.T) {
	f := newDirFixture(t)
	f.symlink(t, "b", "a")
	f.symlink(t, "a", "b")

	_, err := f.fs.Resolve("/a")
	requireErrorIs(t, err, syscall.ELOOP)
	if exists, err := f.fs.Exists("/a"); err != nil || exists {
		t.Fatalf("Exists(loop) = %v, %v", exists, err)
	}
}

func TestDirFSErrorsDoNotLeakHostPaths(t *testing.T) {
	f := newDirFixture(t)
	f.symlink(t, filepath.Join(f.outside, "secret.txt"), "abs-file")
	f.write(t, "dir/file.txt", "x")

	var errs []error
	_, err := f.fs.ReadText("/abs-file")
	errs = append(errs, err)
	_, err = f.fs.ReadText("/missing.txt")
	errs = append(errs, err)
	_, err = f.fs.Iterdir("/dir/file.txt")
	errs = append(errs, err)
	errs = append(errs, f.fs.Rename("/missing", "/other"))
	errs = append(errs, f.fs.Mkdir("/dir", false, false))
	_, err = f.fs.Resolve("/abs-file")
	errs = append(errs, err)

	for _, err := range errs {
		if err == nil {
			t.Fatal("expected an error")
		}
		if strings.Contains(err.Error(), f.base) {
			t.Errorf("error leaks a host path: %v", err)
		}
	}
}

func TestDirFSUsedThroughConcurrentWrites(t *testing.T) {
	f := newDirFixture(t)
	done := make(chan error, 8)
	for i := range 8 {
		go func() {
			_, err := f.fs.WriteText("/shared.txt", strings.Repeat("x", i+1))
			done <- err
		}()
	}
	for range 8 {
		if err := <-done; err != nil {
			t.Fatalf("concurrent write: %v", err)
		}
	}
	if exists, _ := f.fs.IsFile("/shared.txt"); !exists {
		t.Fatal("shared.txt missing")
	}
}
