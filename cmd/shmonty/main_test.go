package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

func TestViewClearsUnusedTerminalRows(t *testing.T) {
	m := newModel(nil, nil, nil)
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 100, Height: 20})
	m = updated.(model)

	for _, entries := range [][]entry{
		{{code: "1 + 1", result: "2"}},
		nil,
	} {
		m.entries = entries
		view := m.View()
		if got := len(strings.Split(view, "\n")); got != m.height {
			t.Fatalf("view with %d entries has %d rows, want %d", len(entries), got, m.height)
		}
		if strings.Contains(view, "40 + 2") {
			t.Fatal("empty input still shows the old placeholder")
		}
		if got := strings.Count(view, "\n> "); got != 1 {
			t.Fatalf("view with %d entries has %d live prompts, want 1", len(entries), got)
		}
	}
}

func TestRenderStatusLine(t *testing.T) {
	m := newModel(nil, nil, &logBuffer{})
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 100, Height: 20})
	m = updated.(model)
	m.history = []string{"1 + 1", "2 + 2"}

	if got := m.renderStatusLine(); !strings.Contains(got, "debug:off") || !strings.Contains(got, "history:2") {
		t.Fatalf("status line %q missing debug:off/history:2", got)
	}
	if strings.Contains(m.renderStatusLine(), "log:") {
		t.Fatal("status line shows log state while debug is off")
	}

	m.debugEnabled = true
	m.logLines = []string{"a", "b", "c"}
	if got := m.renderStatusLine(); !strings.Contains(got, "debug:on") || !strings.Contains(got, "log:3 following") {
		t.Fatalf("status line %q missing debug:on/log:3 following", got)
	}

	m.logFollowing = false
	if got := m.renderStatusLine(); !strings.Contains(got, "log:3 scrolled") {
		t.Fatalf("status line %q missing log:3 scrolled", got)
	}
}

// TestPythonScripts runs every python-scripts/*.py through execScript. Each
// script asserts its own results, so a clean exit is the main check;
// workload_* scripts additionally have their final value pinned here.
func TestPythonScripts(t *testing.T) {
	checkPythonScripts(t)
}

// checkPythonScripts runs every script through execScript with whatever
// backend newRepl and execScript are currently set up to use.
func checkPythonScripts(t *testing.T) {
	t.Helper()
	scripts, err := filepath.Glob(filepath.Join("..", "..", "python-scripts", "*.py"))
	if err != nil || len(scripts) == 0 {
		t.Fatalf("no python scripts found: %v", err)
	}
	// Final value of each workload script (printed on stdout's last line).
	workloads := map[string]string{
		"sales_rollup":        "west:128.5",
		"error_latency":       "worker:600",
		"rolling_window":      "65",
		"word_frequency":      "birds:3, blue:3, green:2",
		"interval_merge":      "55",
		"inventory_reorder":   "38.7",
		"grid_path":           "10",
		"coin_change":         "24",
		"shipment_json":       "east:119, west:102",
		"regex_job_ids":       "2333",
		"target_combinations": "12",
		"matrix_checksum":     "1428",
		"base64_orders":       "40",
		"ranking_tiebreak":    "dee, bo, cy, ana, eli",
		"sha256":              "ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad",
	}
	seen := map[string]bool{}
	for _, script := range scripts {
		name := strings.TrimSuffix(filepath.Base(script), ".py")
		t.Run(name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			if err := execScript(script, &stdout, &stderr); err != nil {
				t.Fatalf("script failed: %v\nstderr:\n%s", err, stderr.String())
			}
			workload, ok := strings.CutPrefix(name, "workload_")
			if !ok {
				return
			}
			want, ok := workloads[workload]
			if !ok {
				t.Fatalf("workload %q has no expected value in TestPythonScripts", workload)
			}
			seen[workload] = true
			lines := strings.Split(strings.TrimRight(stdout.String(), "\n"), "\n")
			if got := lines[len(lines)-1]; got != want {
				t.Errorf("final value = %q, want %q", got, want)
			}
		})
	}
	for workload := range workloads {
		if !seen[workload] {
			t.Errorf("expected workload script workload_%s.py did not run", workload)
		}
	}
}

func TestExecScriptReportsFailure(t *testing.T) {
	path := filepath.Join(t.TempDir(), "boom.py")
	if err := os.WriteFile(path, []byte("raise ValueError('boom')\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	if err := execScript(path, &stdout, &stderr); err == nil {
		t.Fatal("execScript succeeded, want error")
	}
	if !strings.Contains(stderr.String(), "ValueError") {
		t.Errorf("stderr = %q, want it to mention ValueError", stderr.String())
	}
}
