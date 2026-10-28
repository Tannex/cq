package compaz

import (
	"context"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/Tannex/cq/internal/favorites"
)

type fakePrefixStore struct {
	values map[string]string
	writes [][2]string
}

func (f *fakePrefixStore) Prefix(scope string) string { return f.values[scope] }

func (f *fakePrefixStore) SetPrefix(scope, prefix string) {
	if f.values == nil {
		f.values = make(map[string]string)
	}
	f.values[scope] = prefix
	f.writes = append(f.writes, [2]string{scope, prefix})
}

func readyPrefixModel(t *testing.T, options Options, deps Dependencies) *Model {
	t.Helper()
	if deps.LoadSession == nil {
		deps.LoadSession = func(context.Context, string) (Session, error) {
			return Session{Browser: &fakeBrowser{}, User: "ibmuser", Encoding: "latin1"}, nil
		}
	}
	model, err := NewModel(options, deps)
	if err != nil {
		t.Fatal(err)
	}
	applyMessage(t, model, tea.WindowSizeMsg{Width: 100, Height: 20})
	executeCommand(t, model, model.Init())
	return model
}

func editPrefix(t *testing.T, model *Model, prefix string) {
	t.Helper()
	executeCommand(t, model, model.handleKey(keyPress('/', "/")))
	model.prefixInput.SetValue(prefix)
	executeCommand(t, model, model.handleKey(keyPress(tea.KeyEnter, "")))
}

func TestPrefixRestoredAfterRestart(t *testing.T) {
	store := &fakePrefixStore{}
	deps := Dependencies{Prefixes: store}
	model := readyPrefixModel(t, Options{}, deps)
	editPrefix(t, model, " prod.customer.* ")
	if len(store.writes) != 1 || store.writes[0] != [2]string{"", "PROD.CUSTOMER.*"} {
		t.Fatalf("saved prefixes = %#v", store.writes)
	}

	restarted := readyPrefixModel(t, Options{}, deps)
	if restarted.prefix != "PROD.CUSTOMER.*" || restarted.prefixInput.Value() != "PROD.CUSTOMER.*" {
		t.Fatalf("restart prefix=%q input=%q", restarted.prefix, restarted.prefixInput.Value())
	}
	requests := restarted.browser.(*fakeBrowser).dataSetRequests
	if len(requests) != 1 || requests[0].Prefix != "PROD.CUSTOMER.*" {
		t.Fatalf("restart requests = %#v", requests)
	}
	if len(store.writes) != 1 {
		t.Fatalf("restoring a prefix unexpectedly saved it again: %#v", store.writes)
	}
}

func TestPrefixStartupPrecedence(t *testing.T) {
	for _, test := range []struct {
		name     string
		explicit string
		saved    string
		user     string
		want     string
	}{
		{name: "explicit overrides saved", explicit: " override.* ", saved: "SAVED.*", user: "ibmuser", want: "OVERRIDE.*"},
		{name: "saved overrides user", saved: "SAVED.*", user: "ibmuser", want: "SAVED.*"},
		{name: "missing state uses user", user: "ibmuser", want: "IBMUSER.*"},
		{name: "token session restores saved", saved: "SAVED.*", want: "SAVED.*"},
		{name: "token session without saved prefix prompts"},
	} {
		t.Run(test.name, func(t *testing.T) {
			store := &fakePrefixStore{values: map[string]string{"": test.saved}}
			browser := &fakeBrowser{}
			model := readyPrefixModel(t, Options{Prefix: test.explicit}, Dependencies{
				Prefixes: store,
				LoadSession: func(context.Context, string) (Session, error) {
					return Session{Browser: browser, User: test.user}, nil
				},
			})
			if model.prefix != test.want || model.prefixInput.Value() != test.want {
				t.Fatalf("prefix=%q input=%q, want %q", model.prefix, model.prefixInput.Value(), test.want)
			}
			if test.want == "" {
				if !model.prefixInput.Focused() || len(browser.dataSetRequests) != 0 {
					t.Fatalf("missing prefix: input focused=%v requests=%#v", model.prefixInput.Focused(), browser.dataSetRequests)
				}
			} else if len(browser.dataSetRequests) != 1 || browser.dataSetRequests[0].Prefix != test.want {
				t.Fatalf("startup requests = %#v, want prefix %q", browser.dataSetRequests, test.want)
			}
			if len(store.writes) != 0 || store.Prefix("") != test.saved {
				t.Fatalf("startup changed saved default: values=%#v writes=%#v", store.values, store.writes)
			}
		})
	}
}

func TestPrefixCanceledAndEmptyEditsDoNotReplaceSavedPrefix(t *testing.T) {
	store := &fakePrefixStore{values: map[string]string{"": "SAVED.*"}}
	model := readyPrefixModel(t, Options{}, Dependencies{Prefixes: store})
	executeCommand(t, model, model.handleKey(keyPress('/', "/")))
	model.prefixInput.SetValue("CANCELED.*")
	executeCommand(t, model, model.handleKey(keyPress(tea.KeyEscape, "")))
	if model.prefix != "SAVED.*" || model.prefixInput.Focused() {
		t.Fatalf("cancel prefix=%q focused=%v", model.prefix, model.prefixInput.Focused())
	}

	editPrefix(t, model, "   ")
	if model.prefix != "SAVED.*" || !model.prefixInput.Focused() {
		t.Fatalf("empty edit prefix=%q focused=%v", model.prefix, model.prefixInput.Focused())
	}
	if len(store.writes) != 0 || store.Prefix("") != "SAVED.*" {
		t.Fatalf("unapplied edits changed saved default: values=%#v writes=%#v", store.values, store.writes)
	}

	model.prefixInput.SetValue("accepted.*")
	executeCommand(t, model, model.handleKey(keyPress(tea.KeyEnter, "")))
	if len(store.writes) != 1 || store.Prefix("") != "ACCEPTED.*" {
		t.Fatalf("accepted edit not saved: values=%#v writes=%#v", store.values, store.writes)
	}
}

func TestPrefixProfileSelectionsSurviveSwitchAndRestart(t *testing.T) {
	store := &fakePrefixStore{values: map[string]string{"alpha": "ALPHA.*", "beta": "BETA.*"}}
	deps := Dependencies{
		Prefixes: store,
		ListProfiles: func(context.Context) ([]string, error) {
			return []string{"alpha", "beta"}, nil
		},
	}
	model := readyPrefixModel(t, Options{}, deps)
	if model.prefix != "ALPHA.*" {
		t.Fatalf("alpha startup prefix = %q", model.prefix)
	}
	editPrefix(t, model, "ALPHA.NEW.*")
	executeCommand(t, model, model.handleKey(keyPress(tea.KeyTab, "")))
	if model.activeProfile() != "beta" || model.prefix != "BETA.*" {
		t.Fatalf("beta activation profile=%q prefix=%q", model.activeProfile(), model.prefix)
	}
	editPrefix(t, model, "BETA.NEW.*")
	executeCommand(t, model, model.handleKey(keyPress(tea.KeyTab, "")))
	if model.activeProfile() != "alpha" || model.prefix != "ALPHA.NEW.*" || model.prefixInput.Value() != "ALPHA.NEW.*" {
		t.Fatalf("alpha return profile=%q prefix=%q input=%q", model.activeProfile(), model.prefix, model.prefixInput.Value())
	}
	if len(store.writes) != 2 || store.Prefix("alpha") != "ALPHA.NEW.*" || store.Prefix("beta") != "BETA.NEW.*" {
		t.Fatalf("saved profile selections: values=%#v writes=%#v", store.values, store.writes)
	}

	restarted := readyPrefixModel(t, Options{}, deps)
	if restarted.prefix != "ALPHA.NEW.*" {
		t.Fatalf("alpha restart prefix = %q", restarted.prefix)
	}
	executeCommand(t, restarted, restarted.handleKey(keyPress(tea.KeyTab, "")))
	if restarted.prefix != "BETA.NEW.*" || restarted.prefixInput.Value() != "BETA.NEW.*" {
		t.Fatalf("beta restart prefix=%q input=%q", restarted.prefix, restarted.prefixInput.Value())
	}
}

func TestPrefixResolvedScopeOverridesWorkspaceProfile(t *testing.T) {
	for _, profile := range []string{"", "display-name"} {
		t.Run("profile="+profile, func(t *testing.T) {
			store := &fakePrefixStore{values: map[string]string{profile: "UNRELATED.*", "resolved-connection": "RESOLVED.*"}}
			model := readyPrefixModel(t, Options{}, Dependencies{
				Prefixes: store,
				ListProfiles: func(context.Context) ([]string, error) {
					return []string{profile}, nil
				},
				LoadSession: func(context.Context, string) (Session, error) {
					return Session{Browser: &fakeBrowser{}, User: "ibmuser", PrefixScope: "resolved-connection"}, nil
				},
			})
			if model.prefix != "RESOLVED.*" {
				t.Fatalf("resolved startup prefix = %q", model.prefix)
			}
			editPrefix(t, model, "RESOLVED.NEW.*")
			if store.Prefix(profile) != "UNRELATED.*" || len(store.writes) != 1 || store.writes[0] != [2]string{"resolved-connection", "RESOLVED.NEW.*"} {
				t.Fatalf("resolved prefix saved under wrong scope: values=%#v writes=%#v", store.values, store.writes)
			}
		})
	}
}

func TestPrefixFavoriteJumpSavesSelectedPattern(t *testing.T) {
	store := &fakePrefixStore{}
	model := readyPrefixModel(t, Options{}, Dependencies{
		Prefixes: store,
		Favorites: &fakeFavoriteStore{entries: []favorites.Favorite{
			{Pattern: "PROD.CUSTOMER.*"},
		}},
	})
	executeCommand(t, model, model.handleKey(keyPress('F', "F")))
	executeCommand(t, model, model.handleKey(keyPress(tea.KeyEnter, "")))
	if model.prefix != "PROD.CUSTOMER.*" || len(store.writes) != 1 || store.Prefix("") != "PROD.CUSTOMER.*" {
		t.Fatalf("favorite jump prefix=%q saved=%#v writes=%#v", model.prefix, store.values, store.writes)
	}
}
