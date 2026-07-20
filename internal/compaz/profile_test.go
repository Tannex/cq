package compaz

import (
	"context"
	"errors"
	"strings"
	"testing"

	"charm.land/bubbles/v2/spinner"
	tea "charm.land/bubbletea/v2"

	"github.com/Tannex/cq/internal/zosmf"
)

func TestMultiProfileInitListsProfilesAndLoadsFirstSessionOnly(t *testing.T) {
	loadCalls := []string{}
	browser := &fakeBrowser{listDataSets: func(_ context.Context, request zosmf.ListDataSetsRequest) (zosmf.DataSetPage, error) {
		return zosmf.DataSetPage{Items: []zosmf.DataSet{{Name: "A.DATA", Organization: "PS"}}}, nil
	}}
	model, err := NewModel(Options{Prefix: "A*"}, Dependencies{
		ListProfiles: func(context.Context) ([]string, error) {
			return []string{"alpha", "beta", "gamma"}, nil
		},
		LoadSession: func(_ context.Context, profile string) (Session, error) {
			loadCalls = append(loadCalls, profile)
			return Session{Browser: browser, User: "USER" + profile, Encoding: "latin1"}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	applyMessage(t, model, tea.WindowSizeMsg{Width: 100, Height: 20})
	executeCommand(t, model, model.Init())

	if len(model.profiles) != 3 || !model.hasTabs() {
		t.Fatalf("profiles=%#v tabs=%v", model.profiles, model.hasTabs())
	}
	if model.active != 0 || model.activeProfile() != "alpha" {
		t.Fatalf("active profile = %q", model.activeProfile())
	}
	if len(loadCalls) != 1 || loadCalls[0] != "alpha" {
		t.Fatalf("session loads = %#v, want only alpha", loadCalls)
	}
	if model.user != "USERALPHA" {
		t.Fatalf("active user = %q", model.user)
	}
	if len(model.datasets) != 1 {
		t.Fatalf("first session did not fetch rows: datasets=%#v", model.datasets)
	}
}

func TestSingleProfileModeDoesNotListProfiles(t *testing.T) {
	browser := &fakeBrowser{}
	model, err := NewModel(Options{}, Dependencies{
		LoadSession: func(_ context.Context, profile string) (Session, error) {
			if profile != "" {
				t.Fatalf("single-profile load got profile %q", profile)
			}
			return Session{Browser: browser, User: "USER", Encoding: "latin1"}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	applyMessage(t, model, tea.WindowSizeMsg{Width: 100, Height: 20})
	executeCommand(t, model, model.Init())
	if model.hasTabs() {
		t.Fatal("single-profile mode rendered tabs")
	}
}

func TestProfileSwitchPreservesStateRoundTrip(t *testing.T) {
	browser := &fakeBrowser{listDataSets: func(_ context.Context, request zosmf.ListDataSetsRequest) (zosmf.DataSetPage, error) {
		return zosmf.DataSetPage{Items: []zosmf.DataSet{{Name: "A.DATA", Organization: "PS"}}}, nil
	}}
	model := readyModelWithProfiles(t, Options{Prefix: "A*"}, browser, []string{"alpha", "beta"})

	// Record alpha's state before switching.
	alphaScreen := model.screen
	alphaSelected := model.datasetPage.selectedKey()
	alphaPrefix := model.prefix

	executeCommand(t, model, model.handleAction(actionNextProfile))
	if model.activeProfile() != "beta" {
		t.Fatalf("switched to %q", model.activeProfile())
	}
	// Beta loads its session on first activation; set a distinct prefix there.
	model.prefix = "B*"
	model.prefixInput.SetValue("B*")

	executeCommand(t, model, model.handleAction(actionPreviousProfile))
	if model.activeProfile() != "alpha" {
		t.Fatalf("returned to %q", model.activeProfile())
	}
	if model.screen != alphaScreen || model.datasetPage.selectedKey() != alphaSelected || model.prefix != alphaPrefix {
		t.Fatalf("alpha state not preserved: screen=%d selected=%q prefix=%q", model.screen, model.datasetPage.selectedKey(), model.prefix)
	}

	executeCommand(t, model, model.handleAction(actionNextProfile))
	if model.activeProfile() != "beta" {
		t.Fatalf("second switch to %q", model.activeProfile())
	}
	if model.prefix != "B*" {
		t.Fatalf("beta state not preserved: prefix=%q", model.prefix)
	}
}

func TestLazySessionLoadOnFirstActivation(t *testing.T) {
	loadCalls := []string{}
	browser := &fakeBrowser{listDataSets: func(_ context.Context, request zosmf.ListDataSetsRequest) (zosmf.DataSetPage, error) {
		return zosmf.DataSetPage{Items: []zosmf.DataSet{{Name: request.Prefix + ".DATA", Organization: "PS"}}}, nil
	}}
	load := func(profile string) (Session, error) {
		loadCalls = append(loadCalls, profile)
		return Session{Browser: browser, User: strings.ToUpper(profile), Encoding: "latin1"}, nil
	}
	model := readyModelWithProfilesFunc(t, Options{Prefix: "A*"}, load, []string{"alpha", "beta"})
	if len(loadCalls) != 1 || loadCalls[0] != "alpha" {
		t.Fatalf("initial loads = %#v", loadCalls)
	}

	executeCommand(t, model, model.handleAction(actionNextProfile))
	if model.activeProfile() != "beta" {
		t.Fatalf("switched to %q", model.activeProfile())
	}
	if len(loadCalls) != 2 || loadCalls[1] != "beta" {
		t.Fatalf("lazy loads = %#v", loadCalls)
	}
	if model.user != "BETA" {
		t.Fatalf("beta user = %q", model.user)
	}
}

func TestCrossProfileResultDoesNotCorruptActiveView(t *testing.T) {
	alphaBrowser := &fakeBrowser{listDataSets: func(_ context.Context, request zosmf.ListDataSetsRequest) (zosmf.DataSetPage, error) {
		return zosmf.DataSetPage{Items: []zosmf.DataSet{{Name: "A.DATA", Organization: "PS"}}}, nil
	}}
	betaBrowser := &fakeBrowser{listDataSets: func(_ context.Context, request zosmf.ListDataSetsRequest) (zosmf.DataSetPage, error) {
		return zosmf.DataSetPage{Items: []zosmf.DataSet{{Name: "B.DATA", Organization: "PS"}}}, nil
	}}
	browsers := map[string]zosmf.Browser{"alpha": alphaBrowser, "beta": betaBrowser}
	model := readyModelWithProfilesFunc(t, Options{Prefix: "A*"}, func(profile string) (Session, error) {
		return Session{Browser: browsers[profile], User: strings.ToUpper(profile), Encoding: "latin1"}, nil
	}, []string{"alpha", "beta"})

	// Switch to beta so alpha is inactive.
	executeCommand(t, model, model.handleAction(actionNextProfile))
	if model.activeProfile() != "beta" {
		t.Fatalf("active = %q", model.activeProfile())
	}
	betaDatasets := len(model.datasets)

	// Manually dispatch an alpha data-set result while beta is active.
	alphaWS := model.workspaces[0]
	alphaWS.browseGeneration = 7
	alphaWS.browsePending = &requestMeta{
		Generation: 7, Screen: ScreenDataSets,
		Identity: "A*", Profile: "alpha", Budget: model.budget,
		NamePlan: alphaWS.datasetPage.initialPlan(""),
	}
	applyMessage(t, model, dataSetsResultMsg{
		Meta: requestMeta{Generation: 7, Screen: ScreenDataSets, Identity: "A*", Profile: "alpha", Budget: model.budget, NamePlan: alphaWS.datasetPage.initialPlan("")},
		Page: zosmf.DataSetPage{Items: []zosmf.DataSet{{Name: "A.LATE", Organization: "PS"}}},
	})

	// Beta view must be untouched.
	if model.activeProfile() != "beta" || len(model.datasets) != betaDatasets {
		t.Fatalf("beta view corrupted: active=%q datasets=%d", model.activeProfile(), len(model.datasets))
	}
	// Alpha workspace must have received the result.
	if len(alphaWS.datasets) != 1 || alphaWS.datasets[0].Name != "A.LATE" {
		t.Fatalf("alpha workspace not updated: %#v", alphaWS.datasets)
	}
}

func TestProfileSessionErrorIsIsolated(t *testing.T) {
	model := readyModelWithProfilesFunc(t, Options{Prefix: "A*"}, func(profile string) (Session, error) {
		if profile == "beta" {
			return Session{}, errors.New("beta login failed")
		}
		return Session{Browser: &fakeBrowser{}, User: strings.ToUpper(profile), Encoding: "latin1"}, nil
	}, []string{"alpha", "beta"})

	// Record alpha's status before switching to the failing profile.
	alphaStatus := model.status

	executeCommand(t, model, model.handleAction(actionNextProfile))
	if model.activeProfile() != "beta" {
		t.Fatalf("active = %q", model.activeProfile())
	}
	if model.status.Level != statusError || !strings.Contains(model.status.Text, "beta login failed") {
		t.Fatalf("beta status = %#v", model.status)
	}

	executeCommand(t, model, model.handleAction(actionPreviousProfile))
	if model.activeProfile() != "alpha" {
		t.Fatalf("returned to %q", model.activeProfile())
	}
	if model.status != alphaStatus {
		t.Fatalf("alpha status corrupted by beta error: got %#v want %#v", model.status, alphaStatus)
	}
}

func TestTabBarRendersActiveAndInactiveProfiles(t *testing.T) {
	model := readyModelWithProfiles(t, Options{Prefix: "A*"}, &fakeBrowser{}, []string{"alpha", "beta", "gamma"})
	view := model.View().Content
	for _, profile := range []string{"alpha", "beta", "gamma"} {
		if !strings.Contains(view, profile) {
			t.Fatalf("tab bar missing profile %q: %q", profile, view)
		}
	}
}

func TestTabBarConsumesExactlyOneRow(t *testing.T) {
	model := readyModelWithProfiles(t, Options{Prefix: "A*"}, &fakeBrowser{}, []string{"alpha", "beta"})
	content := model.View().Content
	lines := strings.Count(content, "\n") + 1
	if lines != model.height {
		t.Fatalf("tabbed view lines = %d, want terminal height %d", lines, model.height)
	}
	// Without the tab bar the same terminal would show one extra data row.
	withoutTabs := VisibleRows(model.width, model.height, false)
	withTabs := VisibleRows(model.width, model.height, true)
	if withoutTabs != withTabs+1 {
		t.Fatalf("tab bar did not consume one visible row: %d vs %d", withoutTabs, withTabs)
	}
}

func TestTabBarTruncatesAndKeepsActiveVisible(t *testing.T) {
	model := readyModelWithProfiles(t, Options{Prefix: "A*"}, &fakeBrowser{}, []string{"alpha", "beta", "gamma", "delta"})
	model.width = 20
	view := model.tabBar()
	if !strings.Contains(view, "alpha") {
		t.Fatalf("active profile not visible in narrow tab bar: %q", view)
	}
	// With only 20 columns at least one profile should be replaced by an ellipsis.
	if !strings.Contains(view, "…") {
		t.Fatalf("narrow tab bar did not truncate: %q", view)
	}
}

func TestTabSwitchBlursInputsAndSyncsValues(t *testing.T) {
	browser := &fakeBrowser{listDataSets: func(_ context.Context, request zosmf.ListDataSetsRequest) (zosmf.DataSetPage, error) {
		return zosmf.DataSetPage{Items: []zosmf.DataSet{{Name: "A.DATA", Organization: "PS"}}}, nil
	}}
	model := readyModelWithProfiles(t, Options{Prefix: "A*"}, browser, []string{"alpha", "beta"})
	model.prefixInput.Focus()
	model.prefixInput.SetValue("TEMP")

	executeCommand(t, model, model.handleAction(actionNextProfile))
	if model.prefixInput.Focused() {
		t.Fatal("input remained focused after profile switch")
	}
	// Beta inherits the same initial prefix from options.
	if model.prefixInput.Value() != "A*" {
		t.Fatalf("beta prefix input not synced: %q", model.prefixInput.Value())
	}

	model.prefixInput.SetValue("B*")
	model.prefix = "B*"
	executeCommand(t, model, model.handleAction(actionPreviousProfile))
	if model.prefixInput.Value() != "A*" {
		t.Fatalf("alpha prefix input not synced: %q", model.prefixInput.Value())
	}

	executeCommand(t, model, model.handleAction(actionNextProfile))
	if model.prefixInput.Value() != "B*" {
		t.Fatalf("beta prefix input not preserved: %q", model.prefixInput.Value())
	}
}

func TestProfileSwitchDoesNothingInSingleProfileMode(t *testing.T) {
	model := readyModel(t, Options{Prefix: "A*"}, &fakeBrowser{}, "USER", "latin1", 100, 20)
	if model.hasTabs() {
		t.Fatal("single-profile model has tabs")
	}
	if cmd := model.handleAction(actionNextProfile); cmd != nil {
		t.Fatalf("next profile dispatched a command in single-profile mode: %v", cmd)
	}
}

func TestProfileSwitchRestartsSpinnerForPendingRecalls(t *testing.T) {
	browser := &fakeBrowser{listDataSets: func(_ context.Context, request zosmf.ListDataSetsRequest) (zosmf.DataSetPage, error) {
		return zosmf.DataSetPage{Items: []zosmf.DataSet{{Name: "A.DATA", Organization: "PS"}}}, nil
	}}
	model := readyModelWithProfiles(t, Options{Prefix: "A*"}, browser, []string{"alpha", "beta"})
	model.ws().markRecall("A.DATA")

	executeCommand(t, model, model.handleAction(actionNextProfile))
	if model.ws().hasRecalls() {
		t.Fatal("recall leaked into the beta workspace")
	}
	command := model.handleAction(actionPreviousProfile)
	if !commandYieldsSpinnerTick(command) {
		t.Fatal("switching back to a recalling workspace did not restart the spinner tick loop")
	}
}

func commandYieldsSpinnerTick(command tea.Cmd) bool {
	return walkMessages(command, func(message tea.Msg) (tea.Cmd, bool) {
		_, ok := message.(spinner.TickMsg)
		return nil, ok
	})
}

func readyModelWithProfiles(t *testing.T, options Options, browser zosmf.Browser, profiles []string) *Model {
	t.Helper()
	return readyModelWithProfilesFunc(t, options, func(_ string) (Session, error) {
		return Session{Browser: browser, User: "USER", Encoding: "latin1"}, nil
	}, profiles)
}

func readyModelWithProfilesFunc(t *testing.T, options Options, load func(string) (Session, error), profiles []string) *Model {
	t.Helper()
	model, err := NewModel(options, Dependencies{
		ListProfiles: func(context.Context) ([]string, error) { return profiles, nil },
		LoadSession:  func(_ context.Context, profile string) (Session, error) { return load(profile) },
	})
	if err != nil {
		t.Fatal(err)
	}
	applyMessage(t, model, tea.WindowSizeMsg{Width: 100, Height: 20})
	executeCommand(t, model, model.Init())
	return model
}
