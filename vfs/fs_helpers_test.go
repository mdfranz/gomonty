package vfs

import (
	"reflect"
	"testing"
)

func requireText(t *testing.T, fs FileSystem, p, want string) {
	t.Helper()
	got, err := fs.ReadText(p)
	if err != nil || got != want {
		t.Fatalf("ReadText(%q)=%q,%v;want %q", p, got, err, want)
	}
}
func requireListing(t *testing.T, fs FileSystem, p string, want []string) {
	t.Helper()
	got, err := fs.Iterdir(p)
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("Iterdir(%q)=%v,%v;want %v", p, got, err, want)
	}
}
