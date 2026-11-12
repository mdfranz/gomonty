package vfs

import (
	"errors"
	"io"
	stdfs "io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func mountOptions(t *testing.T, dir string, opts DirFSOptions) *DirFS {
	t.Helper()
	d, err := NewDirFS(dir, opts)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.Close() })
	return d
}

func TestDirFSReadOnly(t *testing.T) {
	f := newDirFixture(t)
	f.write(t, "data/file", "original")
	d := mountOptions(t, f.root, DirFSOptions{ReadOnly: true})
	operations := map[string]func() error{
		"text":       func() error { _, err := d.WriteText("/data/file", "new"); return err },
		"bytes":      func() error { _, err := d.WriteBytes("/new", []byte("new")); return err },
		"mkdir":      func() error { return d.Mkdir("/new", false, false) },
		"mkdir root": func() error { return d.Mkdir("/", true, true) },
		"unlink":     func() error { return d.Unlink("/data/file") },
		"rmdir":      func() error { return d.Rmdir("/data") },
		"rename":     func() error { return d.Rename("/data/file", "/other") },
	}
	for name, operation := range operations {
		t.Run(name, func(t *testing.T) { requireErrorIs(t, operation(), stdfs.ErrPermission) })
	}
	if got, err := d.ReadText("/data/file"); err != nil || got != "original" {
		t.Fatalf("read=%q,%v", got, err)
	}
	children, err := d.Iterdir("/")
	if err != nil || len(children) != 1 || children[0] != "/data" {
		t.Fatalf("children=%v,%v", children, err)
	}
}

func TestDirFSSizeLimits(t *testing.T) {
	f := newDirFixture(t)
	f.write(t, "one", "1234")
	f.write(t, "two", "12")
	d := mountOptions(t, f.root, DirFSOptions{MaxFileBytes: 5, MaxTotalBytes: 8})
	if _, err := d.WriteText("/one", "12345"); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ p, text, limit string }{
		{"/one", "123456", "MaxFileBytes"},
		{"/new", "12", "MaxTotalBytes"},
		{"/two", "1234", "MaxTotalBytes"},
	} {
		t.Run(tc.p+tc.limit, func(t *testing.T) {
			n, err := d.WriteText(tc.p, tc.text)
			if n != 0 || err == nil || !strings.Contains(err.Error(), tc.limit) {
				t.Fatalf("write=%d,%v", n, err)
			}
		})
	}
	if got, err := d.ReadText("/one"); err != nil || got != "12345" {
		t.Fatalf("old file=%q,%v", got, err)
	}
	if exists, _ := d.Exists("/new"); exists {
		t.Fatal("rejected create left a file")
	}
	if err := d.Rename("/one", "/renamed"); err != nil {
		t.Fatal(err)
	}
	if _, err := d.WriteText("/two", "1"); err != nil {
		t.Fatal(err)
	}
	if _, err := d.WriteText("/new", "12"); err != nil {
		t.Fatal(err)
	}
	if err := d.Unlink("/renamed"); err != nil {
		t.Fatal(err)
	}
	if _, err := d.WriteText("/new", "12345"); err != nil {
		t.Fatal(err)
	}
	// Limits belong to the instance; another mount can have a different cap.
	other := mountOptions(t, f.root, DirFSOptions{})
	if _, err := other.WriteText("/large", strings.Repeat("x", 20)); err != nil {
		t.Fatal(err)
	}
	if _, err := d.WriteText("/two", "12"); err == nil {
		t.Fatal("existing oversized tree was not counted")
	}
}

func TestDirFSConcurrentTotalLimit(t *testing.T) {
	d := mountOptions(t, t.TempDir(), DirFSOptions{MaxTotalBytes: 8})
	var wg sync.WaitGroup
	var mu sync.Mutex
	successes := 0
	for _, p := range []string{"/one", "/two"} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := d.WriteText(p, "12345678"); err == nil {
				mu.Lock()
				successes++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	if successes != 1 {
		t.Fatalf("successful writes=%d,want 1", successes)
	}
}

func TestDirFSPatternPolicy(t *testing.T) {
	f := newDirFixture(t)
	f.write(t, "src/good.txt", "yes")
	f.write(t, "src/private/secret.txt", "secret")
	f.write(t, "other.txt", "no")
	d := mountOptions(t, f.root, DirFSOptions{Allow: []string{".", "src", "alias"}, Deny: []string{"src/private"}})
	if got, err := d.ReadText("/src/good.txt"); err != nil || got != "yes" {
		t.Fatalf("read=%q,%v", got, err)
	}
	for _, p := range []string{"/other.txt", "/src/private/secret.txt", "/src/../other.txt"} {
		_, err := d.ReadText(p)
		requireErrorIs(t, err, stdfs.ErrPermission)
		_, err = d.WriteText(p, "changed")
		requireErrorIs(t, err, stdfs.ErrPermission)
	}
	if got, err := d.Iterdir("/src"); err != nil || len(got) != 1 || got[0] != "/src/good.txt" {
		t.Fatalf("listing=%v,%v", got, err)
	}
	// Moving a parent must not expose a descendant denied at its original path.
	requireErrorIs(t, d.Rename("/src", "/alias"), stdfs.ErrPermission)
	f.symlink(t, "src/private", "alias")
	_, err := d.ReadText("/alias/secret.txt")
	requireErrorIs(t, err, stdfs.ErrPermission)
	_, err = d.WriteText("/alias/secret.txt", "changed")
	requireErrorIs(t, err, stdfs.ErrPermission)
}

func TestDirFSPatternValidationAndCopy(t *testing.T) {
	for _, opts := range []DirFSOptions{{MaxFileBytes: -1}, {MaxTotalBytes: -1}, {Allow: []string{"["}}, {Deny: []string{"/absolute"}}, {Deny: []string{"../escape"}}} {
		if d, err := NewDirFS(t.TempDir(), opts); err == nil {
			d.Close()
			t.Fatalf("accepted invalid options: %+v", opts)
		}
	}
	patterns := []string{"secret"}
	dir := t.TempDir()
	d := mountOptions(t, dir, DirFSOptions{Deny: patterns})
	patterns[0] = "other"
	_, err := d.WriteText("/secret", "no")
	requireErrorIs(t, err, stdfs.ErrPermission)
}

type failedReader struct{ err error }

func (r failedReader) Read([]byte) (int, error) { return 0, r.err }

func TestDirFSAtomicWriteFailure(t *testing.T) {
	dir := t.TempDir()
	d := mountOptions(t, dir, DirFSOptions{})
	if _, err := d.WriteText("/existing", "original"); err != nil {
		t.Fatal(err)
	}
	failure := errors.New("failure after partial staging write")
	for _, name := range []string{"existing", "new"} {
		err := d.atomicWrite(name, io.MultiReader(strings.NewReader("partial"), failedReader{failure}), 0o644)
		if !errors.Is(err, failure) {
			t.Fatalf("error=%v", err)
		}
	}
	got, err := os.ReadFile(filepath.Join(dir, "existing"))
	if err != nil || string(got) != "original" {
		t.Fatalf("original=%q,%v", got, err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) != 1 || entries[0].Name() != "existing" {
		t.Fatalf("leftover staging files=%v,%v", entries, err)
	}
}

func TestDirFSGlobPolicyAndRenameDestination(t *testing.T) {
	f := newDirFixture(t)
	f.write(t, "src/visible.txt", "yes")
	f.write(t, "src/secret.key", "secret")
	d := mountOptions(t, f.root, DirFSOptions{Allow: []string{".", "src", "dst"}, Deny: []string{"dst/*.key"}})
	requireErrorIs(t, d.Rename("/src", "/dst"), stdfs.ErrPermission)
	requireText(t, d, "/src/secret.key", "secret")
	filtered := mountOptions(t, f.root, DirFSOptions{Allow: []string{".", "src", "src/*.txt"}, Deny: []string{"src/*.key"}})
	requireListing(t, filtered, "/src", []string{"/src/visible.txt"})
	for _, operation := range []func() error{
		func() error { _, err := filtered.Stat("/src/secret.key"); return err },
		func() error { _, err := filtered.Exists("/src/secret.key"); return err },
		func() error { return filtered.Unlink("/src/secret.key") },
		func() error { return filtered.Rename("/src/secret.key", "/src/new.txt") },
		func() error { _, err := filtered.Resolve("/src/secret.key"); return err },
		func() error { return filtered.Mkdir("/src/private.key", false, false) },
	} {
		requireErrorIs(t, operation(), stdfs.ErrPermission)
	}
	rootOnly := mountOptions(t, f.root, DirFSOptions{Allow: []string{"."}})
	requireListing(t, rootOnly, "/", []string{})
	_, err := rootOnly.ReadText("/src/visible.txt")
	requireErrorIs(t, err, stdfs.ErrPermission)
}

func TestDirFSLimitedWriteThroughSymlink(t *testing.T) {
	f := newDirFixture(t)
	f.write(t, "real/file", "original")
	f.symlink(t, "real/file", "alias")
	d := mountOptions(t, f.root, DirFSOptions{MaxTotalBytes: 8})
	if _, err := d.WriteText("/alias", "new"); err != nil {
		t.Fatal(err)
	}
	requireText(t, d, "/real/file", "new")
	if link, err := d.IsSymlink("/alias"); err != nil || !link {
		t.Fatalf("symlink was replaced: %v,%v", link, err)
	}
	if _, err := d.WriteText("/alias", strings.Repeat("x", 9)); err == nil {
		t.Fatal("over-limit symlink write succeeded")
	}
	requireText(t, d, "/real/file", "new")
}
