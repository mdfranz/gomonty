package vfs

import (
	"errors"
	stdfs "io/fs"
	"os"
	"path/filepath"
	"reflect"
	"sync"
	"testing"
)

func overlayFixture(t *testing.T, disk bool) (*OverlayFS, FileSystem) {
	t.Helper()
	var lower FileSystem
	if disk {
		dir := t.TempDir()
		for name, text := range map[string]string{"dir/one": "one", "dir/two": "two", "top": "original"} {
			full := filepath.Join(dir, filepath.FromSlash(name))
			if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(full, []byte(text), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		lower = mountOptions(t, dir, DirFSOptions{ReadOnly: true})
	} else {
		m := NewMemoryFS()
		m.AddText("/dir/one", "one")
		m.AddText("/dir/two", "two")
		m.AddText("/top", "original")
		lower = m
	}
	return Overlay(lower, NewMemoryFS()), lower
}
func TestOverlayReadsWritesDeletesAndDiff(t *testing.T) {
	for _, disk := range []bool{false, true} {
		name := "memory"
		if disk {
			name = "disk"
		}
		t.Run(name, func(t *testing.T) {
			o, lower := overlayFixture(t, disk)
			requireText(t, o, "/top", "original")
			if _, err := o.WriteText("/top", "changed"); err != nil {
				t.Fatal(err)
			}
			requireText(t, o, "/top", "changed")
			requireText(t, lower, "/top", "original")
			if _, err := o.WriteBytes("/dir/three", []byte{0, 1, 2}); err != nil {
				t.Fatal(err)
			}
			if err := o.Unlink("/dir/one"); err != nil {
				t.Fatal(err)
			}
			if exists, err := o.Exists("/dir/one"); err != nil || exists {
				t.Fatalf("deleted path exists: %v,%v", exists, err)
			}
			if _, err := o.ReadText("/dir/one"); !errors.Is(err, stdfs.ErrNotExist) {
				t.Fatalf("read whiteout: %v", err)
			}
			requireListing(t, o, "/dir", []string{"/dir/three", "/dir/two"})
			want := []Change{{Path: "/dir/one", Kind: ChangeDeleted}, {Path: "/dir/three", Kind: ChangeCreated, Data: []byte{0, 1, 2}}, {Path: "/top", Kind: ChangeModified, Data: []byte("changed")}}
			got, err := o.Diff()
			if err != nil || !reflect.DeepEqual(got, want) {
				t.Fatalf("diff=%+v,%v;want %+v", got, err, want)
			}
			got[1].Data[0] = 99
			data, err := o.ReadBytes("/dir/three")
			if err != nil || data[0] != 0 {
				t.Fatalf("diff aliases upper contents: %v,%v", data, err)
			}
			requireText(t, lower, "/dir/one", "one")
			requireListing(t, lower, "/dir", []string{"/dir/one", "/dir/two"})
		})
	}
}

func TestOverlayRenameMergedDirectory(t *testing.T) {
	for _, disk := range []bool{false, true} {
		name := "memory"
		if disk {
			name = "disk"
		}
		t.Run(name, func(t *testing.T) {
			o, lower := overlayFixture(t, disk)
			if _, err := o.WriteText("/dir/one", "changed"); err != nil {
				t.Fatal(err)
			}
			if err := o.Unlink("/dir/two"); err != nil {
				t.Fatal(err)
			}
			if _, err := o.WriteText("/dir/three", "three"); err != nil {
				t.Fatal(err)
			}
			if err := o.Mkdir("/destination", false, false); err != nil {
				t.Fatal(err)
			}
			if err := o.Rename("/dir", "/destination/moved"); err != nil {
				t.Fatal(err)
			}
			requireText(t, o, "/destination/moved/one", "changed")
			requireText(t, o, "/destination/moved/three", "three")
			requireListing(t, o, "/", []string{"/destination", "/top"})
			if exists, err := o.Exists("/dir/one"); err != nil || exists {
				t.Fatalf("old tree visible: %v,%v", exists, err)
			}
			changes, err := o.Diff()
			if err != nil {
				t.Fatal(err)
			}
			paths := make([]string, 0, len(changes))
			for _, c := range changes {
				paths = append(paths, c.Path)
			}
			want := []string{"/destination", "/destination/moved", "/destination/moved/one", "/destination/moved/three", "/dir", "/dir/one", "/dir/two"}
			if !reflect.DeepEqual(paths, want) {
				t.Fatalf("diff paths=%v,want %v", paths, want)
			}
			requireText(t, lower, "/dir/one", "one")
			requireText(t, lower, "/dir/two", "two")
		})
	}
}

func TestOverlayNetDiffAndDirectoryRecreation(t *testing.T) {
	o, _ := overlayFixture(t, false)
	if _, err := o.WriteText("/temporary", "temp"); err != nil {
		t.Fatal(err)
	}
	if err := o.Unlink("/temporary"); err != nil {
		t.Fatal(err)
	}
	if _, err := o.WriteText("/top", "changed"); err != nil {
		t.Fatal(err)
	}
	if _, err := o.WriteText("/top", "original"); err != nil {
		t.Fatal(err)
	}
	changes, err := o.Diff()
	if err != nil || len(changes) != 0 {
		t.Fatalf("net diff=%+v,%v", changes, err)
	}
	for _, p := range []string{"/dir/one", "/dir/two"} {
		if err := o.Unlink(p); err != nil {
			t.Fatal(err)
		}
	}
	if err := o.Rmdir("/dir"); err != nil {
		t.Fatal(err)
	}
	if err := o.Mkdir("/dir", false, false); err != nil {
		t.Fatal(err)
	}
	requireListing(t, o, "/dir", []string{})
	if _, err := o.WriteText("/dir/new", "new"); err != nil {
		t.Fatal(err)
	}
	requireListing(t, o, "/dir", []string{"/dir/new"})
	changes, err = o.Diff()
	if err != nil {
		t.Fatal(err)
	}
	want := []Change{{Path: "/dir/new", Kind: ChangeCreated, Data: []byte("new")}, {Path: "/dir/one", Kind: ChangeDeleted}, {Path: "/dir/two", Kind: ChangeDeleted}}
	if !reflect.DeepEqual(changes, want) {
		t.Fatalf("diff=%+v,want %+v", changes, want)
	}
}

func TestOverlayErrorsLeaveViewIntact(t *testing.T) {
	o, _ := overlayFixture(t, false)
	for _, operation := range []func() error{
		func() error { return o.Rename("/dir", "/dir/child") },
		func() error { return o.Rename("/top", "/dir/one") },
		func() error { return o.Rename("/top", "/missing/new") },
		func() error { return o.Rmdir("/dir") },
		func() error { return o.Rmdir("/") },
		func() error { return o.Unlink("/dir") },
		func() error { _, err := o.WriteText("/top/child", "x"); return err },
		func() error { return o.Mkdir("/top/child/grandchild", true, false) },
	} {
		if err := operation(); err == nil {
			t.Fatal("invalid operation succeeded")
		}
	}
	diff, err := o.Diff()
	if err != nil || len(diff) != 0 {
		t.Fatalf("failed operation mutated view: %+v,%v", diff, err)
	}
	requireText(t, o, "/top", "original")
}

func TestOverlaySymlinksAreRejected(t *testing.T) {
	lower := NewMemoryFS()
	lower.AddText("/real/file", "original")
	lower.AddSymlink("/alias", "/real")
	o := Overlay(lower, nil)
	if _, err := o.ReadText("/alias/file"); !errors.Is(err, stdfs.ErrPermission) {
		t.Fatalf("symlink read=%v", err)
	}
	if _, err := o.WriteText("/alias/file", "changed"); !errors.Is(err, stdfs.ErrPermission) {
		t.Fatalf("symlink write=%v", err)
	}
	if err := o.Rename("/alias", "/moved"); !errors.Is(err, stdfs.ErrPermission) {
		t.Fatalf("symlink rename=%v", err)
	}
	requireText(t, lower, "/real/file", "original")
}

func TestOverlayConcurrentWrites(t *testing.T) {
	o, _ := overlayFixture(t, false)
	var wg sync.WaitGroup
	for _, p := range []string{"/dir/a", "/dir/b", "/dir/c"} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := o.WriteText(p, p); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	changes, err := o.Diff()
	if err != nil || len(changes) != 3 {
		t.Fatalf("diff=%+v,%v", changes, err)
	}
}

func TestOverlayRenameFailureDuringCopy(t *testing.T) {
	lower := NewMemoryFS()
	lower.AddText("/dir/file", "original")
	lower.AddSymlink("/dir/link", "/dir/file")
	o := Overlay(lower, nil)
	if err := o.Rename("/dir", "/moved"); !errors.Is(err, stdfs.ErrPermission) {
		t.Fatalf("rename=%v", err)
	}
	requireText(t, o, "/dir/file", "original")
	if exists, err := o.Exists("/moved"); err != nil || exists {
		t.Fatalf("partially copied destination: %v,%v", exists, err)
	}
	changes, err := o.Diff()
	if err != nil || len(changes) != 0 {
		t.Fatalf("failed move changed diff: %+v,%v", changes, err)
	}
}

func TestOverlayFileRenameAndTypeChanges(t *testing.T) {
	o, lower := overlayFixture(t, true)
	if err := o.Rename("/top", "/dir/new"); err != nil {
		t.Fatal(err)
	}
	requireText(t, o, "/dir/new", "original")
	requireText(t, lower, "/top", "original")
	if err := o.Rename("/dir/new", "/top"); err != nil {
		t.Fatal(err)
	}
	if _, err := o.WriteText("/transient", "temp"); err != nil {
		t.Fatal(err)
	}
	if err := o.Rename("/transient", "/renamed"); err != nil {
		t.Fatal(err)
	}
	if err := o.Unlink("/renamed"); err != nil {
		t.Fatal(err)
	}
	changes, err := o.Diff()
	if err != nil || len(changes) != 0 {
		t.Fatalf("restored net diff=%+v,%v", changes, err)
	}
	if err := o.Unlink("/top"); err != nil {
		t.Fatal(err)
	}
	if err := o.Mkdir("/top", false, false); err != nil {
		t.Fatal(err)
	}
	if _, err := o.WriteText("/top/child", "content"); err != nil {
		t.Fatal(err)
	}
	changes, err = o.Diff()
	if err != nil {
		t.Fatal(err)
	}
	want := []Change{{Path: "/top", Kind: ChangeModified, IsDir: true}, {Path: "/top/child", Kind: ChangeCreated, Data: []byte("content")}}
	if !reflect.DeepEqual(changes, want) {
		t.Fatalf("type-change diff=%+v,want %+v", changes, want)
	}
}
