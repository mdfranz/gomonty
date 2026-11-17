package monty

import (
	"bytes"
	"os/exec"
	"strings"
	"testing"
)

func TestCIClassifyChanges(t *testing.T) {
	tests := []struct {
		name  string
		paths []string
		want  map[string]string
	}{
		{
			name:  "docs only",
			paths: []string{"README.md", "docs/ci.md", ".agents/skills/review-ffi/SKILL.md"},
			want:  map[string]string{"code": "false", "proto": "false", "native": "false"},
		},
		{
			name: "go only library and submodules",
			paths: []string{
				"runner.go",
				"dispatch.go",
				"remote/session.go",
				"vfs/memory.go",
				"otelmonty/handler.go",
				"cmd/shmonty/main.go",
				"examples/cmd/example/main.go",
			},
			want: map[string]string{"code": "true", "proto": "true", "native": "false"},
		},
		{
			name:  "rust ffi and wire files",
			paths: []string{"crates/monty-go-ffi/src/lib.rs", "wire.go", "Cargo.toml", "rust-toolchain.toml", "internal/ffi/ffi.go"},
			want:  map[string]string{"code": "true", "proto": "true", "native": "true"},
		},
		{
			name:  "build constrained go file",
			paths: []string{"internal/ffi/load_library_unix.go"},
			want:  map[string]string{"code": "true", "proto": "true", "native": "true"},
		},
		{
			name:  "mixed docs and wire changes",
			paths: []string{"docs/ci.md", "wire.go"},
			want:  map[string]string{"code": "true", "proto": "true", "native": "true"},
		},
		{
			name:  "uncertain unknown file",
			paths: []string{"some_new_tool.py"},
			want:  map[string]string{"code": "true", "proto": "true", "native": "true"},
		},
		{
			name:  "empty input fallback",
			paths: nil,
			want:  map[string]string{"code": "true", "proto": "true", "native": "true"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cmd := exec.Command("bash", "scripts/ci-classify-changes.sh")
			cmd.Stdin = strings.NewReader(strings.Join(tc.paths, "\n"))
			var out, errBuf bytes.Buffer
			cmd.Stdout = &out
			cmd.Stderr = &errBuf
			if err := cmd.Run(); err != nil {
				t.Fatalf("ci-classify-changes.sh failed: %v\nstderr: %s", err, errBuf.String())
			}

			got := make(map[string]string)
			for _, line := range strings.Split(strings.TrimSpace(out.String()), "\n") {
				k, v, ok := strings.Cut(line, "=")
				if !ok {
					t.Fatalf("unexpected output line %q", line)
				}
				got[k] = v
			}

			for k, wantVal := range tc.want {
				if got[k] != wantVal {
					t.Errorf("%s = %q, want %q (full output: %q)", k, got[k], wantVal, out.String())
				}
			}
		})
	}
}
