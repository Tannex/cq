package compaz

import (
	"fmt"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/Tannex/cq/internal/events"
)

func TestQueryHistoryNavigation(t *testing.T) {
	m, _ := queryModel(t, nil)
	openQuery(t, m)
	m.query.input.SetValue("draft")
	m.query.input.SetCursor(2)
	for _, key := range []rune{tea.KeyUp, tea.KeyDown} {
		applyMessage(t, m, keyPress(key, ""))
	}
	if m.query.input.Value() != "draft" {
		t.Fatal("empty history changed draft")
	}
	runQueryExpr(t, m, "length")
	runQueryExpr(t, m, "bad(")
	m.query.input.SetValue("draft")
	m.query.input.SetCursor(2)
	generation := m.query.generation
	for _, step := range []struct {
		key  rune
		want string
	}{
		{tea.KeyUp, "bad("}, {tea.KeyUp, "length"}, {tea.KeyUp, "length"},
		{tea.KeyDown, "bad("}, {tea.KeyDown, "draft"}, {tea.KeyDown, "draft"},
	} {
		applyMessage(t, m, keyPress(step.key, ""))
		if got := m.query.input.Value(); got != step.want {
			t.Fatalf("got %q, want %q", got, step.want)
		}
		if m.query.generation != generation || m.query.running {
			t.Fatal("recall executed query")
		}
	}
	if m.query.input.Position() != 2 {
		t.Fatal("draft cursor was not restored")
	}
	applyMessage(t, m, keyPress(tea.KeyUp, ""))
	m.query.input.SetValue("length + 1")
	runQueryExpr(t, m, m.query.input.Value())
	applyMessage(t, m, keyPress(tea.KeyEscape, ""))
	openQuery(t, m)
	applyMessage(t, m, keyPress(tea.KeyUp, ""))
	if m.query.input.Value() != "length + 1" {
		t.Fatal("edited query not retained across reopen")
	}
	applyMessage(t, m, keyPress(tea.KeyUp, ""))
	if m.query.input.Value() != "bad(" {
		t.Fatal("editing changed original history")
	}
}

func TestQueryHistoryUsesEventLogAcrossModels(t *testing.T) {
	dir := t.TempDir()
	store := &events.Store{UserConfigDir: func() (string, error) { return dir, nil }}
	store.RecordOpen("A.DATA")
	store.RecordQuery("length", true)
	store.RecordQuery("bad(", false)
	store.RecordQuery("", true)
	m, _ := queryModel(t, nil)
	m.deps.Events = store
	openQuery(t, m)
	applyMessage(t, m, keyPress(tea.KeyUp, ""))
	if m.query.input.Value() != "bad(" {
		t.Fatal("failed submission missing from log history")
	}
	runQueryExpr(t, m, "map(.)")
	fresh, _ := queryModel(t, nil)
	fresh.deps.Events = &events.Store{UserConfigDir: func() (string, error) { return dir, nil }}
	openQuery(t, fresh)
	for _, want := range []string{"map(.)", "bad(", "length", "length"} {
		applyMessage(t, fresh, keyPress(tea.KeyUp, ""))
		if fresh.query.input.Value() != want {
			t.Fatalf("got %q, want %q", fresh.query.input.Value(), want)
		}
	}
}

func TestQueryHistoryCompletionAndScrolling(t *testing.T) {
	m, _ := queryModel(t, nil)
	openQuery(t, m)
	runQueryExpr(t, m, "length")
	m.query.input.SetValue("draft")
	m.query.comp = &queryCompletion{filtered: []completionField{{label: "A"}, {label: "B"}}}
	applyMessage(t, m, keyPress(tea.KeyDown, ""))
	if m.query.comp.selected != 1 || m.query.input.Value() != "draft" {
		t.Fatal("history intercepted completion")
	}
	applyMessage(t, m, keyPress(tea.KeyUp, ""))
	if m.query.comp.selected != 0 {
		t.Fatal("completion up failed")
	}
	m.query.comp = nil
	m.query.lines = make([]string, 100)
	applyMessage(t, m, keyPress(tea.KeyPgDown, ""))
	if m.query.scroll == 0 || m.query.input.Value() != "draft" {
		t.Fatal("page down did not scroll independently")
	}
	applyMessage(t, m, keyPress(tea.KeyPgUp, ""))
	if m.query.scroll != 0 {
		t.Fatal("page up did not restore scroll")
	}
}

func TestQueryHistorySurvivesUnavailableLogAndKeepsDuplicates(t *testing.T) {
	m, _ := queryModel(t, nil)
	m.deps.Events = &events.Store{UserConfigDir: func() (string, error) { return "", fmt.Errorf("unavailable") }}
	openQuery(t, m)
	runQueryExpr(t, m, "length")
	runQueryExpr(t, m, "length")
	if len(m.queryHistory) != 2 {
		t.Fatal("execution order lost duplicate submissions")
	}
	applyMessage(t, m, keyPress(tea.KeyEscape, ""))
	openQuery(t, m)
	applyMessage(t, m, keyPress(tea.KeyUp, ""))
	if m.query.input.Value() != "length" {
		t.Fatal("unavailable log lost session history")
	}
	for range queryHistoryLimit {
		m.recordQuery(".", true)
	}
	if len(m.queryHistory) != queryHistoryLimit || m.query.historyIndex != queryHistoryLimit {
		t.Fatal("history not bounded or cursor not reset")
	}
}
