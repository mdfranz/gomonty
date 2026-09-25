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
