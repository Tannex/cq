package compaz

import (
	"context"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/Tannex/cq/internal/dsnmap"
	"github.com/Tannex/cq/internal/favorites"
	"github.com/Tannex/cq/internal/zosmf"
)

type fakeFavoriteStore struct {
	entries []favorites.Favorite
	touched []string
	noted   [][2]string
	noteErr error
}

func (f *fakeFavoriteStore) Favorites() []favorites.Favorite {
	return append([]favorites.Favorite(nil), f.entries...)
}

func (f *fakeFavoriteStore) Matches(name string) bool {
	for _, entry := range f.entries {
		if dsnmap.MatchPattern(name, entry.Pattern) {
			return true
		}
	}
	return false
}

func (f *fakeFavoriteStore) Toggle(name string) (bool, error) {
	name = strings.ToUpper(strings.TrimSpace(name))
	for i, entry := range f.entries {
		if entry.Pattern == name {
			f.entries = append(f.entries[:i], f.entries[i+1:]...)
			return false, nil
		}
	}
	f.entries = append(f.entries, favorites.Favorite{Pattern: name})
	return true, nil
}

func (f *fakeFavoriteStore) SetNote(pattern, note string) error {
	if f.noteErr != nil {
		return f.noteErr
	}
	f.noted = append(f.noted, [2]string{pattern, note})
	for i, entry := range f.entries {
		if entry.Pattern == pattern {
			f.entries[i].Note = note
		}
	}
	return nil
}

func (f *fakeFavoriteStore) Remove(pattern string) (bool, error) {
	for i, entry := range f.entries {
		if entry.Pattern == pattern {
			f.entries = append(f.entries[:i], f.entries[i+1:]...)
			return true, nil
		}
	}
	return false, nil
}

func (f *fakeFavoriteStore) Touch(pattern string) error {
	f.touched = append(f.touched, pattern)
	return nil
}

func favoriteModel(t *testing.T, store FavoriteStore) (*Model, *fakeBrowser) {
	t.Helper()
	browser := &fakeBrowser{
		listDataSets: func(context.Context, zosmf.ListDataSetsRequest) (zosmf.DataSetPage, error) {
			return zosmf.DataSetPage{Items: []zosmf.DataSet{
				{Name: "A.CUSTOMER.DATA", Organization: "PS"},
				{Name: "A.ORDER.DATA", Organization: "PS"},
			}}, nil
		},
	}
	model, err := NewModel(Options{Prefix: "A*", Codepage: "latin1"}, Dependencies{
		LoadSession: func(context.Context, string) (Session, error) {
			return Session{Browser: browser, User: "A", Encoding: "latin1"}, nil
		},
		Favorites: store,
	})
	if err != nil {
		t.Fatal(err)
	}
	applyMessage(t, model, tea.WindowSizeMsg{Width: 90, Height: 20})
	executeCommand(t, model, model.Init())
	return model, browser
}

func TestToggleFavoriteAddsExactEntryAndShowsMarker(t *testing.T) {
	store := &fakeFavoriteStore{}
	model, _ := favoriteModel(t, store)

	executeCommand(t, model, model.handleKey(keyPress('f', "f")))
	if len(store.entries) != 1 || store.entries[0].Pattern != "A.CUSTOMER.DATA" {
		t.Fatalf("entries after toggle = %#v", store.entries)
	}
	if !strings.Contains(model.status.Text, "favorited A.CUSTOMER.DATA") {
		t.Fatalf("status = %q", model.status.Text)
	}
	if view := model.dataSetTableView(); !strings.Contains(view, favoriteMark) {
		t.Fatal("favorite marker missing from data set table")
	}

	executeCommand(t, model, model.handleKey(keyPress('f', "f")))
	if len(store.entries) != 0 {
		t.Fatalf("entries after second toggle = %#v", store.entries)
	}
	if !strings.Contains(model.status.Text, "removed favorite") {
		t.Fatalf("status = %q", model.status.Text)
	}
}

func TestWildcardFavoriteMarksRowsAndToggleFavoritesExactName(t *testing.T) {
	store := &fakeFavoriteStore{entries: []favorites.Favorite{{Pattern: "A.CUSTOMER.*"}}}
	model, _ := favoriteModel(t, store)

	view := model.dataSetTableView()
	lines := strings.Split(view, "\n")
	marked := 0
	for _, line := range lines {
		if strings.Contains(line, favoriteMark) {
			marked++
		}
	}
	if marked != 1 {
		t.Fatalf("marked rows = %d, want 1 (only the wildcard-covered row)\n%s", marked, view)
	}

	// f on the wildcard-covered row favorites the exact DSN; the wildcard
	// entry survives untouched.
	executeCommand(t, model, model.handleKey(keyPress('f', "f")))
	patterns := make([]string, 0, len(store.entries))
	for _, entry := range store.entries {
		patterns = append(patterns, entry.Pattern)
	}
	if len(patterns) != 2 || patterns[0] != "A.CUSTOMER.*" || patterns[1] != "A.CUSTOMER.DATA" {
		t.Fatalf("patterns after toggle = %#v", patterns)
	}

	// Removing the exact entry keeps the row marked through the wildcard.
	executeCommand(t, model, model.handleKey(keyPress('f', "f")))
	if !strings.Contains(model.status.Text, "wildcard") {
		t.Fatalf("status = %q", model.status.Text)
	}
}

func TestFavoritesPopupJumpAppliesPatternAsPrefixAndFetches(t *testing.T) {
	store := &fakeFavoriteStore{entries: []favorites.Favorite{
		{Pattern: "PROD.CUSTOMER.*", Note: "check after EOM job"},
		{Pattern: "A.ORDER.DATA"},
	}}
	model, browser := favoriteModel(t, store)
	browser.dataSetRequests = nil

	executeCommand(t, model, model.handleKey(keyPress('F', "F")))
	if model.favPopup == nil {
		t.Fatal("popup did not open")
	}
	if view := model.View().Content; !strings.Contains(view, "FAVORITES") || !strings.Contains(view, "check after EOM job") {
		t.Fatal("popup view missing title or note")
	}

	executeCommand(t, model, model.handleKey(keyPress(tea.KeyEnter, "")))
	if model.favPopup != nil {
		t.Fatal("popup stayed open after jump")
	}
	if model.prefix != "PROD.CUSTOMER.*" || model.prefixInput.Value() != "PROD.CUSTOMER.*" {
		t.Fatalf("prefix after jump = %q input %q", model.prefix, model.prefixInput.Value())
	}
	if len(browser.dataSetRequests) == 0 || browser.dataSetRequests[0].Prefix != "PROD.CUSTOMER.*" {
		t.Fatalf("fetch requests = %#v", browser.dataSetRequests)
	}
	if len(store.touched) != 1 || store.touched[0] != "PROD.CUSTOMER.*" {
		t.Fatalf("touched = %#v", store.touched)
	}
}

func TestFavoritesPopupNavigationAndRemove(t *testing.T) {
	store := &fakeFavoriteStore{entries: []favorites.Favorite{
		{Pattern: "A.ONE"}, {Pattern: "B.TWO"}, {Pattern: "C.THREE"},
	}}
	model, _ := favoriteModel(t, store)

	executeCommand(t, model, model.handleKey(keyPress('F', "F")))
	executeCommand(t, model, model.handleKey(keyPress(tea.KeyDown, "")))
	if model.favPopup.selected != 1 {
		t.Fatalf("selected = %d", model.favPopup.selected)
	}

	executeCommand(t, model, model.handleKey(keyPress('x', "x")))
	if len(store.entries) != 2 || len(model.favPopup.entries) != 2 {
		t.Fatalf("entries after remove: store=%#v popup=%#v", store.entries, model.favPopup.entries)
	}
	if model.favPopup.entries[1].Pattern != "C.THREE" {
		t.Fatalf("popup entries = %#v", model.favPopup.entries)
	}

	// Selection clamps when the last entry is removed.
	executeCommand(t, model, model.handleKey(keyPress(tea.KeyDown, "")))
	executeCommand(t, model, model.handleKey(keyPress('x', "x")))
	if model.favPopup.selected != 0 {
		t.Fatalf("selected after tail remove = %d", model.favPopup.selected)
	}

	executeCommand(t, model, model.handleKey(keyPress(tea.KeyEscape, "")))
	if model.favPopup != nil {
		t.Fatal("esc did not close popup")
	}
}

func TestFavoritesPopupNoteEditing(t *testing.T) {
	store := &fakeFavoriteStore{entries: []favorites.Favorite{{Pattern: "A.ONE"}}}
	model, _ := favoriteModel(t, store)

	executeCommand(t, model, model.handleKey(keyPress('F', "F")))
	executeCommand(t, model, model.handleKey(keyPress('n', "n")))
	if !model.favPopup.editing {
		t.Fatal("note editing did not start")
	}

	// While editing, printable keys go to the input, not popup actions.
	executeCommand(t, model, model.handleKey(keyPress('x', "x")))
	if len(store.entries) != 1 {
		t.Fatal("x removed the entry while editing a note")
	}
	executeCommand(t, model, model.handleKey(keyPress(tea.KeyEnter, "")))
	if model.favPopup.editing {
		t.Fatal("enter did not finish note editing")
	}
	if len(store.noted) != 1 || store.noted[0][0] != "A.ONE" || store.noted[0][1] != "x" {
		t.Fatalf("noted = %#v", store.noted)
	}
	if model.favPopup.entries[0].Note != "x" {
		t.Fatalf("popup note = %q", model.favPopup.entries[0].Note)
	}

	// Esc cancels an edit without saving.
	executeCommand(t, model, model.handleKey(keyPress('n', "n")))
	executeCommand(t, model, model.handleKey(keyPress(tea.KeyEscape, "")))
	if model.favPopup == nil || model.favPopup.editing {
		t.Fatal("esc during edit closed the popup instead of the editor")
	}
	if len(store.noted) != 1 {
		t.Fatalf("noted after cancel = %#v", store.noted)
	}
}

func TestFavoritesWithoutStoreDegradeToStatus(t *testing.T) {
	model, _ := favoriteModel(t, nil)

	executeCommand(t, model, model.handleKey(keyPress('f', "f")))
	if model.status.Level != statusWarn {
		t.Fatalf("toggle status = %#v", model.status)
	}
	executeCommand(t, model, model.handleKey(keyPress('F', "F")))
	if model.favPopup != nil {
		t.Fatal("popup opened without a store")
	}
	if view := model.dataSetTableView(); strings.Contains(view, favoriteMark) {
		t.Fatal("marker column rendered without a store")
	}
}

func TestFavoritesPopupEmptyState(t *testing.T) {
	store := &fakeFavoriteStore{}
	model, _ := favoriteModel(t, store)

	executeCommand(t, model, model.handleKey(keyPress('F', "F")))
	if view := model.View().Content; !strings.Contains(view, "no favorites yet") {
		t.Fatal("empty popup message missing")
	}
	// Enter, note, and remove are inert with no entries.
	executeCommand(t, model, model.handleKey(keyPress(tea.KeyEnter, "")))
	executeCommand(t, model, model.handleKey(keyPress('n', "n")))
	executeCommand(t, model, model.handleKey(keyPress('x', "x")))
	if model.favPopup == nil || model.favPopup.editing {
		t.Fatalf("empty popup state changed: %#v", model.favPopup)
	}
}
