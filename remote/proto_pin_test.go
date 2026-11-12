package remote

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"regexp"
	"strings"
	"testing"
)

func upstreamField(t *testing.T, name string) string {
	t.Helper()
	data, err := os.ReadFile("proto/UPSTREAM")
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range strings.Split(string(data), "\n") {
		if value, ok := strings.CutPrefix(line, name+": "); ok {
			return strings.TrimSpace(value)
		}
	}
	t.Fatalf("proto/UPSTREAM has no %q field", name)
	return ""
}

// The vendored proto is pinned to the same upstream commit as the FFI.
func TestProtoPinMatchesCargoPin(t *testing.T) {
	cargo, err := os.ReadFile("../Cargo.toml")
	if err != nil {
		t.Fatal(err)
	}
	matches := regexp.MustCompile(`rev = "([0-9a-f]{40})"`).FindAllSubmatch(cargo, -1)
	if len(matches) == 0 {
		t.Fatal("no monty rev found in Cargo.toml")
	}
	commit := upstreamField(t, "commit")
	for _, m := range matches {
		if string(m[1]) != commit {
			t.Fatalf("Cargo.toml rev %s != proto/UPSTREAM commit %s; re-vendor the proto from the new pin", m[1], commit)
		}
	}
}

// The vendored proto must stay byte-identical to upstream's file.
func TestVendoredProtoIsUnmodified(t *testing.T) {
	data, err := os.ReadFile("proto/monty/v1/monty.proto")
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(data)
	if got, want := hex.EncodeToString(sum[:]), upstreamField(t, "sha256"); got != want {
		t.Fatalf("monty.proto sha256 %s != proto/UPSTREAM %s; do not edit the vendored file, re-vendor it", got, want)
	}
}
