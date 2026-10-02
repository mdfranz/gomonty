package main

import (
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
