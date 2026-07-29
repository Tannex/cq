package compaz

import (
	"slices"
	"testing"

	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"
)

func keyPress(code rune, text string) tea.KeyPressMsg {
	return tea.KeyPressMsg(tea.Key{Code: code, Text: text})
}

func TestFunctionKeysUseActualBubbleTeaMessagesInEveryWideMode(t *testing.T) {
	keys := DefaultKeyMap()
	f10 := keyPress(tea.KeyF10, "")
	f11 := keyPress(tea.KeyF11, "")
	for _, mode := range []RecordMode{ModeRaw, ModeTable, ModeJSON} {
		ctx := keyContext{Screen: ScreenRecords, Mode: mode}
		if got := keys.actionFor(f10, ctx); got != actionWideLeft {
			t.Fatalf("mode %d F10 action = %d", mode, got)
		}
		if got := keys.actionFor(f11, ctx); got != actionWideRight {
			t.Fatalf("mode %d F11 action = %d", mode, got)
		}
	}
}

func TestHorizontalPanKeysAreScopedToRecordViews(t *testing.T) {
	keys := DefaultKeyMap()
	records := keyContext{Screen: ScreenRecords}
	for _, test := range []struct {
		message tea.KeyPressMsg
		want    action
	}{
		{keyPress(tea.KeyF10, ""), actionWideLeft},
		{tea.KeyPressMsg(tea.Key{Code: tea.KeyLeft}), actionWideLeft},
		{keyPress('h', "h"), actionWideLeft},
		{keyPress(tea.KeyF11, ""), actionWideRight},
		{tea.KeyPressMsg(tea.Key{Code: tea.KeyRight}), actionWideRight},
		{keyPress('l', "l"), actionWideRight},
	} {
		if got := keys.actionFor(test.message, records); got != test.want {
			t.Fatalf("records key %q action = %d, want %d", test.message.String(), got, test.want)
		}
	}
	for _, screen := range []Screen{ScreenDataSets, ScreenMembers} {
		ctx := keyContext{Screen: screen}
		for _, message := range []tea.KeyPressMsg{
			keyPress(tea.KeyF10, ""), tea.KeyPressMsg(tea.Key{Code: tea.KeyLeft}), keyPress('h', "h"),
			keyPress(tea.KeyF11, ""), tea.KeyPressMsg(tea.Key{Code: tea.KeyRight}), keyPress('l', "l"),
		} {
			if got := keys.actionFor(message, ctx); got != actionNone {
				t.Fatalf("screen %d key %q dispatched action %d", screen, message.String(), got)
			}
		}
	}
}

func TestSlashFiltersListsAndLocatesRecords(t *testing.T) {
	keys := DefaultKeyMap()
	for _, screen := range []Screen{ScreenDataSets, ScreenMembers, ScreenRecords} {
		if got := keys.actionFor(keyPress('/', "/"), keyContext{Screen: screen}); got != actionSearch {
			t.Fatalf("screen %d slash action = %d", screen, got)
		}
	}
	if help := keys.Locate.Help(); help.Desc != "locate" {
		t.Fatalf("locate help = %#v", help)
	}
}

func TestFocusedInputAndDialogSuppressGlobalShortcuts(t *testing.T) {
	keys := DefaultKeyMap()
	messages := []tea.KeyPressMsg{
		keyPress('q', "q"),
		keyPress('r', "r"),
		keyPress('c', "c"),
		keyPress(tea.KeyF10, ""),
		keyPress(tea.KeyF11, ""),
	}
	for _, context := range []keyContext{
		{Screen: ScreenRecords, Mode: ModeTable, InputFocused: true},
		{Screen: ScreenRecords, Mode: ModeJSON, DialogOpen: true},
	} {
		for _, message := range messages {
			if got := keys.actionFor(message, context); got != actionNone {
				t.Fatalf("focused context %#v key %q dispatched action %d", context, message.String(), got)
			}
		}
	}
	if got := keys.actionFor(keyPress(tea.KeyEnter, ""), keyContext{InputFocused: true}); got != actionAccept {
		t.Fatalf("focused Enter action = %d", got)
	}
	if got := keys.actionFor(keyPress(tea.KeyEscape, ""), keyContext{DialogOpen: true}); got != actionCancel {
		t.Fatalf("dialog Esc action = %d", got)
	}
}

func TestMappingListKeysRouteToListActionsAndFormKeepsTyping(t *testing.T) {
	keys := DefaultKeyMap()
	list := keyContext{Screen: ScreenRecords, DialogOpen: true}
	for _, want := range []struct {
		msg    tea.KeyPressMsg
		action action
	}{
		{keyPress('a', "a"), actionMappingAdd},
		{keyPress('e', "e"), actionMappingEdit},
		{keyPress('x', "x"), actionMappingRemove},
		{keyPress(tea.KeyUp, ""), actionUp},
		{keyPress(tea.KeyDown, ""), actionDown},
		{keyPress(tea.KeyEnter, ""), actionAccept},
		{keyPress(tea.KeyEscape, ""), actionCancel},
	} {
		if got := keys.actionFor(want.msg, list); got != want.action {
			t.Fatalf("list key %q action = %d, want %d", want.msg.String(), got, want.action)
		}
	}
	form := keyContext{Screen: ScreenRecords, DialogOpen: true, DialogFormFocused: true}
	for _, msg := range []tea.KeyPressMsg{keyPress('a', "a"), keyPress('e', "e"), keyPress('x', "x")} {
		if got := keys.actionFor(msg, form); got != actionNone {
			t.Fatalf("form key %q dispatched action %d instead of typing", msg.String(), got)
		}
	}
}

func TestShowHelpSuppressesNavigationAndEnablesScrolling(t *testing.T) {
	keys := DefaultKeyMap()
	ctx := keyContext{Screen: ScreenRecords, Mode: ModeTable, ShowHelp: true}
	if got := keys.actionFor(keyPress('r', "r"), ctx); got != actionNone {
		t.Fatalf("r dispatched action %d while help is open", got)
	}
	if got := keys.actionFor(tea.KeyPressMsg(tea.Key{Code: tea.KeyUp}), ctx); got != actionHelpUp {
		t.Fatalf("up dispatched action %d while help is open", got)
	}
	if got := keys.actionFor(tea.KeyPressMsg(tea.Key{Code: tea.KeyDown}), ctx); got != actionHelpDown {
		t.Fatalf("down dispatched action %d while help is open", got)
	}
	if got := keys.actionFor(tea.KeyPressMsg(tea.Key{Code: tea.KeyPgUp}), ctx); got != actionHelpPageUp {
		t.Fatalf("pgup dispatched action %d while help is open", got)
	}
	if got := keys.actionFor(tea.KeyPressMsg(tea.Key{Code: tea.KeyPgDown}), ctx); got != actionHelpPageDown {
		t.Fatalf("pgdn dispatched action %d while help is open", got)
	}
	if got := keys.actionFor(tea.KeyPressMsg(tea.Key{Code: tea.KeyHome}), ctx); got != actionHelpTop {
		t.Fatalf("home dispatched action %d while help is open", got)
	}
	if got := keys.actionFor(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnd}), ctx); got != actionHelpBottom {
		t.Fatalf("end dispatched action %d while help is open", got)
	}
	if got := keys.actionFor(keyPress('?', "?"), ctx); got != actionHelp {
		t.Fatalf("? did not toggle help while help is open")
	}
	if got := keys.actionFor(tea.KeyPressMsg(tea.Key{Code: tea.KeyEscape}), ctx); got != actionHelp {
		t.Fatalf("esc did not close help while help is open")
	}
	if got := keys.actionFor(keyPress('q', "q"), ctx); got != actionQuit {
		t.Fatalf("q did not quit while help is open")
	}
}

// TestDrillDownScreensMatchNavigateBack makes the drift warning on
// drillDownScreen mechanical: a screen advertises "esc back" exactly when
// navigateBack actually leaves it. A new screen that joins one set without
// the other fails here instead of shipping a dead hint or an unhinted key.
func TestDrillDownScreensMatchNavigateBack(t *testing.T) {
	for _, screen := range []Screen{
		ScreenDataSets, ScreenMembers, ScreenRecords,
		ScreenJobs, ScreenSpoolFiles, ScreenSpoolContent,
	} {
		model := readyModel(t, Options{}, &fakeBrowser{}, "IBMUSER", "", 90, 16)
		model.screen = screen
		executeCommand(t, model, model.navigateBack())
		if moved := model.screen != screen; moved != drillDownScreen(screen) {
			t.Errorf("screen %d: navigateBack moved=%v but drillDownScreen=%v — the hint and the key disagree",
				screen, moved, drillDownScreen(screen))
		}
	}
}

// containsBinding reports whether bindings includes one with want's keys —
// the single notion of binding membership every help test shares.
func containsBinding(bindings []key.Binding, want key.Binding) bool {
	return slices.ContainsFunc(bindings, func(binding key.Binding) bool {
		return slices.Equal(binding.Keys(), want.Keys())
	})
}

// fullHelpContains reports whether any group of the ? popup lists want.
func fullHelpContains(groups []helpGroup, want key.Binding) bool {
	return slices.ContainsFunc(groups, func(group helpGroup) bool {
		return containsBinding(group.Bindings, want)
	})
}

// sameBindings reports the strips are element-wise identical by keys.
func sameBindings(got, want []key.Binding) bool {
	return slices.EqualFunc(got, want, func(a, b key.Binding) bool {
		return slices.Equal(a.Keys(), b.Keys())
	})
}

// TestBrowseShortHelpStaysMinimal pins the deliberate strip diet exactly:
// top-level browse screens advertise help/quit and nothing else, drill-downs
// add only Back, and everything removed from the strip must remain
// discoverable in the ? popup under the same conditions as before.
func TestBrowseShortHelpStaysMinimal(t *testing.T) {
	keys := DefaultKeyMap()

	minimal := []key.Binding{keys.Help, keys.Quit}
	withBack := []key.Binding{keys.Help, keys.Quit, keys.Back}
	for screen, want := range map[Screen][]key.Binding{
		ScreenDataSets: minimal, ScreenJobs: minimal,
		ScreenMembers: withBack, ScreenRecords: withBack,
		ScreenSpoolFiles: withBack, ScreenSpoolContent: withBack,
	} {
		if got := keys.shortHelp(keyContext{Screen: screen}); !sameBindings(got, want) {
			t.Fatalf("screen %d strip = %#v, want exactly %#v", screen, got, want)
		}
	}

	// Overlay keys stay overlay-conditional in the popup: present with an
	// overlay, absent without one (they are dead keys until one is loaded).
	withOverlay := keys.fullHelp(keyContext{Screen: ScreenRecords}, true)
	withoutOverlay := keys.fullHelp(keyContext{Screen: ScreenRecords}, false)
	for _, binding := range []key.Binding{keys.ClearOverlay, keys.Query, keys.ToggleOverlay, keys.ToggleView} {
		if !fullHelpContains(withOverlay, binding) {
			t.Fatalf("popup missing %v with an active overlay", binding.Help())
		}
		if fullHelpContains(withoutOverlay, binding) {
			t.Fatalf("popup advertises dead key %v without an overlay", binding.Help())
		}
	}

	// The view-toggle keys' only remaining advertisement is the popup's
	// GENERAL group, gated on the session actually browsing jobs.
	if !fullHelpContains(keys.fullHelp(keyContext{Screen: ScreenDataSets, JobsAvailable: true}, false), keys.NextView) {
		t.Fatal("popup missing the view toggle with jobs available")
	}
	if fullHelpContains(keys.fullHelp(keyContext{Screen: ScreenDataSets}, false), keys.NextView) {
		t.Fatal("popup advertises the view toggle without job support")
	}
}

func TestJSONHelpDescribesPageKeysAsViewportScrolling(t *testing.T) {
	groups := DefaultKeyMap().fullHelp(keyContext{Screen: ScreenRecords, Mode: ModeJSON}, true)
	var descriptions []string
	for _, group := range groups {
		for _, binding := range group.Bindings {
			descriptions = append(descriptions, binding.Help().Desc)
		}
	}
	if !slices.Contains(descriptions, "scroll JSON up") || !slices.Contains(descriptions, "scroll JSON down") {
		t.Fatalf("JSON help descriptions = %#v", descriptions)
	}
}

func TestF10AndF11DoNotCollideWithOtherBindings(t *testing.T) {
	keys := DefaultKeyMap()
	others := []key.Binding{
		keys.Up, keys.Down, keys.PageUp, keys.PageDown, keys.Top, keys.Bottom,
		keys.Open, keys.Back, keys.Search, keys.Locate, keys.Refresh, keys.Copybook,
		keys.ClearOverlay, keys.ToggleOverlay, keys.ToggleView, keys.Diagnostics,
		keys.Help, keys.Quit, keys.Accept, keys.Cancel, keys.NextField, keys.PreviousField,
	}
	for _, binding := range others {
		for _, configured := range binding.Keys() {
			if configured == "f10" || configured == "f11" {
				t.Fatalf("binding %v collides with reserved function key %q", binding.Help(), configured)
			}
		}
	}
	if got := keys.WideLeft.Keys(); !slices.Equal(got, []string{"f10", "left", "h"}) {
		t.Fatalf("left pan binding = %#v", got)
	}
	if got := keys.WideRight.Keys(); !slices.Equal(got, []string{"f11", "right", "l"}) {
		t.Fatalf("right pan binding = %#v", got)
	}
}

func TestProfileKeysSwitchOnlyWhenTabsEnabledAndUnfocused(t *testing.T) {
	keys := DefaultKeyMap()
	tab := tea.KeyPressMsg(tea.Key{Code: tea.KeyTab})
	shiftTab := tea.KeyPressMsg(tea.Key{Code: tea.KeyTab, Mod: tea.ModShift})

	if got := keys.actionFor(tab, keyContext{Tabs: true}); got != actionNextProfile {
		t.Fatalf("tab without tabs action = %d", got)
	}
	if got := keys.actionFor(shiftTab, keyContext{Tabs: true}); got != actionPreviousProfile {
		t.Fatalf("shift+tab without tabs action = %d", got)
	}
	if got := keys.actionFor(tab, keyContext{Tabs: false}); got != actionNone {
		t.Fatalf("tab dispatched profile action without tabs: %d", got)
	}
	if got := keys.actionFor(tab, keyContext{Tabs: true, InputFocused: true}); got != actionNone {
		t.Fatalf("tab switched profiles while input focused: %d", got)
	}
	if got := keys.actionFor(tab, keyContext{Tabs: true, DialogOpen: true, DialogFormFocused: true}); got != actionNextField {
		t.Fatalf("tab did not move form field while mapping form focused: %d", got)
	}
	if got := keys.actionFor(tab, keyContext{Tabs: true, DialogOpen: true}); got != actionNone {
		t.Fatalf("tab switched profiles while mapping list open: %d", got)
	}
	if got := keys.actionFor(tab, keyContext{Tabs: true, ShowHelp: true}); got != actionNone {
		t.Fatalf("tab switched profiles while help open: %d", got)
	}
}

func TestProfileHelpAppearsOnlyWithTabs(t *testing.T) {
	keys := DefaultKeyMap()

	fullWithout := keys.fullHelp(keyContext{Screen: ScreenDataSets, Tabs: false}, false)
	fullWith := keys.fullHelp(keyContext{Screen: ScreenDataSets, Tabs: true}, false)
	bindingInGroup := func(groups []helpGroup, groupName string, want key.Binding) bool {
		for _, group := range groups {
			if group.Name == groupName && containsBinding(group.Bindings, want) {
				return true
			}
		}
		return false
	}
	if bindingInGroup(fullWithout, "GENERAL", keys.NextProfile) {
		t.Fatal("next profile appeared in full help without tabs")
	}
	if !bindingInGroup(fullWith, "GENERAL", keys.NextProfile) || !bindingInGroup(fullWith, "GENERAL", keys.PreviousProfile) {
		t.Fatalf("profile keys missing from full help GENERAL group with tabs")
	}
}
