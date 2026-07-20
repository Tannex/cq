package compaz

import (
	"context"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/Tannex/cq/internal/favorites"
	"github.com/Tannex/cq/internal/zosmf"
)

func paste(text string) tea.PasteMsg { return tea.PasteMsg{Content: text} }

func TestPasteReachesTheFocusedSearchInput(t *testing.T) {
	model := readyModel(t, Options{Prefix: "A*"}, &fakeBrowser{}, "USER", "latin1", 90, 20)

	// Nothing focused: the paste is dropped without side effects.
	applyMessage(t, model, paste("IGNORED"))
	if model.prefixInput.Value() != "A*" {
		t.Fatalf("unfocused paste mutated the prefix input: %q", model.prefixInput.Value())
	}

	executeCommand(t, model, model.handleKey(keyPress('/', "/")))
	if !model.prefixInput.Focused() {
		t.Fatal("/ did not focus the prefix input")
	}
	// The input opens prefilled with the current prefix; the paste inserts
	// at the cursor like any typed text.
	applyMessage(t, model, paste("PROD.CUSTOMER"))
	if got := model.prefixInput.Value(); got != "A*PROD.CUSTOMER" {
		t.Fatalf("prefix input after paste = %q", got)
	}
}

func TestPasteReachesTheQueryConsole(t *testing.T) {
	model, _ := queryModel(t, singleRecordPage(zosmf.Record{Number: 1, Data: []byte("ABC")}))
	openQuery(t, model)

	applyMessage(t, model, paste(`.[] | select(.NAME == "ABC")`))
	if got := model.query.input.Value(); got != `.[] | select(.NAME == "ABC")` {
		t.Fatalf("query input after paste = %q", got)
	}
}

func TestPasteReachesTheFavoritesPatternInput(t *testing.T) {
	store := &fakeFavoriteStore{entries: []favorites.Favorite{{Pattern: "A.ONE"}}}
	browser := &fakeBrowser{listDataSets: func(context.Context, zosmf.ListDataSetsRequest) (zosmf.DataSetPage, error) {
		return zosmf.DataSetPage{Items: []zosmf.DataSet{{Name: "A.DATA", Organization: "PS"}}}, nil
	}}
	model, err := NewModel(Options{Prefix: "A*"}, Dependencies{
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

	executeCommand(t, model, model.handleKey(keyPress('F', "F")))
	// List mode: a paste has no input and is dropped.
	applyMessage(t, model, paste("/dropped/"))
	if model.favPopup.pattern.Value() != "" {
		t.Fatalf("list-mode paste reached the pattern input: %q", model.favPopup.pattern.Value())
	}
	executeCommand(t, model, model.handleKey(keyPress('a', "a")))
	applyMessage(t, model, paste(`/^prod\.cust/`))
	if got := model.favPopup.pattern.Value(); got != `/^prod\.cust/` {
		t.Fatalf("pattern input after paste = %q", got)
	}
}
