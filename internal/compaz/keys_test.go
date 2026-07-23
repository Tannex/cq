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

// TestBrowseShortHelpStaysMinimal pins the deliberate strip diet: browse
// screens advertise only help/quit (plus Back on drill-downs) and leave the
// full key list to the ? popup. Overlay keys live in fullHelp only.
func TestBrowseShortHelpStaysMinimal(t *testing.T) {
	keys := DefaultKeyMap()

	contains := func(bindings []key.Binding, want key.Binding) bool {
		for _, binding := range bindings {
			if slices.Equal(binding.Keys(), want.Keys()) {
				return true
			}
		}
		return false
	}

	records := keys.shortHelp(keyContext{Screen: ScreenRecords}, true)
	if contains(records, keys.ClearOverlay) || contains(records, keys.Locate) {
		t.Fatalf("browse short help advertises more than help/quit/back: %#v", records)
	}
	if !contains(records, keys.Help) || !contains(records, keys.Quit) || !contains(records, keys.Back) {
		t.Fatalf("records short help missing help/quit/back: %#v", records)
	}
	if datasets := keys.shortHelp(keyContext{Screen: ScreenDataSets}, false); contains(datasets, keys.Back) {
		t.Fatal("top-level data sets screen advertises Back with nothing to go back to")
	}

	groups := keys.fullHelp(keyContext{Screen: ScreenRecords}, true)
	inFull := false
	for _, group := range groups {
		if contains(group.Bindings, keys.ClearOverlay) {
			inFull = true
		}
	}
	if !inFull {
		t.Fatal("clear overlay missing from the full help popup with an active overlay")
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
			if group.Name != groupName {
				continue
			}
			for _, binding := range group.Bindings {
				if slices.Equal(binding.Keys(), want.Keys()) {
					return true
				}
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
