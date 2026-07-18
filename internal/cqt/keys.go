package cqt

import (
	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"
)

type Screen uint8

const (
	ScreenDataSets Screen = iota
	ScreenMembers
	ScreenRecords
)

type RecordMode uint8

const (
	ModeRaw RecordMode = iota
	ModeTable
	ModeJSON
)

type action uint8

const (
	actionNone action = iota
	actionUp
	actionDown
	actionPageUp
	actionPageDown
	actionTop
	actionBottom
	actionOpen
	actionBack
	actionSearch
	actionRefresh
	actionCopybook
	actionClearOverlay
	actionToggleOverlay
	actionToggleView
	actionDiagnostics
	actionHelp
	actionQuit
	actionWideLeft
	actionWideRight
	actionAccept
	actionCancel
	actionNextField
	actionPreviousField
	actionHelpUp
	actionHelpDown
	actionHelpPageUp
	actionHelpPageDown
	actionHelpTop
	actionHelpBottom
	actionNextProfile
	actionPreviousProfile
)

type keyContext struct {
	Screen       Screen
	Mode         RecordMode
	InputFocused bool
	DialogOpen   bool
	ShowHelp     bool
	Tabs         bool
}

// KeyMap is the single source of truth for application, input, dialog, and
// wide-data bindings. Function keys are intentionally assigned only here.
type KeyMap struct {
	Up              key.Binding
	Down            key.Binding
	PageUp          key.Binding
	PageDown        key.Binding
	Top             key.Binding
	Bottom          key.Binding
	Open            key.Binding
	Back            key.Binding
	Search          key.Binding
	Locate          key.Binding
	Refresh         key.Binding
	Copybook        key.Binding
	ClearOverlay    key.Binding
	ToggleOverlay   key.Binding
	ToggleView      key.Binding
	Diagnostics     key.Binding
	Help            key.Binding
	Quit            key.Binding
	WideLeft        key.Binding
	WideRight       key.Binding
	Accept          key.Binding
	Cancel          key.Binding
	NextField       key.Binding
	PreviousField   key.Binding
	NextProfile     key.Binding
	PreviousProfile key.Binding
}

func DefaultKeyMap() KeyMap {
	return KeyMap{
		Up:              key.NewBinding(key.WithKeys("up", "k"), key.WithHelp("↑/k", "up")),
		Down:            key.NewBinding(key.WithKeys("down", "j"), key.WithHelp("↓/j", "down")),
		PageUp:          key.NewBinding(key.WithKeys("pgup"), key.WithHelp("pgup", "page up")),
		PageDown:        key.NewBinding(key.WithKeys("pgdown"), key.WithHelp("pgdn", "page down")),
		Top:             key.NewBinding(key.WithKeys("home", "g"), key.WithHelp("g", "top")),
		Bottom:          key.NewBinding(key.WithKeys("end", "G"), key.WithHelp("G", "bottom")),
		Open:            key.NewBinding(key.WithKeys("enter"), key.WithHelp("enter", "open")),
		Back:            key.NewBinding(key.WithKeys("esc"), key.WithHelp("esc", "back")),
		Search:          key.NewBinding(key.WithKeys("/"), key.WithHelp("/", "filter")),
		Locate:          key.NewBinding(key.WithKeys("/"), key.WithHelp("/", "locate")),
		Refresh:         key.NewBinding(key.WithKeys("r"), key.WithHelp("r", "refresh")),
		Copybook:        key.NewBinding(key.WithKeys("c"), key.WithHelp("c", "copybook")),
		ClearOverlay:    key.NewBinding(key.WithKeys("x"), key.WithHelp("x", "clear overlay")),
		ToggleOverlay:   key.NewBinding(key.WithKeys("o"), key.WithHelp("o", "raw/overlay")),
		ToggleView:      key.NewBinding(key.WithKeys("v"), key.WithHelp("v", "table/json")),
		Diagnostics:     key.NewBinding(key.WithKeys("d"), key.WithHelp("d", "diagnostics")),
		Help:            key.NewBinding(key.WithKeys("?"), key.WithHelp("?", "help")),
		Quit:            key.NewBinding(key.WithKeys("q", "ctrl+c"), key.WithHelp("q", "quit")),
		WideLeft:        key.NewBinding(key.WithKeys("f10", "left", "h"), key.WithHelp("←/h", "pan left")),
		WideRight:       key.NewBinding(key.WithKeys("f11", "right", "l"), key.WithHelp("→/l", "pan right")),
		Accept:          key.NewBinding(key.WithKeys("enter"), key.WithHelp("enter", "apply")),
		Cancel:          key.NewBinding(key.WithKeys("esc"), key.WithHelp("esc", "cancel")),
		NextField:       key.NewBinding(key.WithKeys("tab"), key.WithHelp("tab", "next field")),
		PreviousField:   key.NewBinding(key.WithKeys("shift+tab"), key.WithHelp("shift+tab", "previous field")),
		NextProfile:     key.NewBinding(key.WithKeys("tab"), key.WithHelp("tab", "next profile")),
		PreviousProfile: key.NewBinding(key.WithKeys("shift+tab"), key.WithHelp("shift+tab", "previous profile")),
	}
}

func (k KeyMap) actionFor(msg tea.KeyPressMsg, ctx keyContext) action {
	if ctx.DialogOpen {
		switch {
		case key.Matches(msg, k.Accept):
			return actionAccept
		case key.Matches(msg, k.Cancel):
			return actionCancel
		case key.Matches(msg, k.NextField):
			return actionNextField
		case key.Matches(msg, k.PreviousField):
			return actionPreviousField
		default:
			return actionNone
		}
	}
	if ctx.InputFocused {
		switch {
		case key.Matches(msg, k.Accept):
			return actionAccept
		case key.Matches(msg, k.Cancel):
			return actionCancel
		default:
			return actionNone
		}
	}
	if ctx.ShowHelp {
		switch {
		case key.Matches(msg, k.Up):
			return actionHelpUp
		case key.Matches(msg, k.Down):
			return actionHelpDown
		case key.Matches(msg, k.PageUp):
			return actionHelpPageUp
		case key.Matches(msg, k.PageDown):
			return actionHelpPageDown
		case key.Matches(msg, k.Top):
			return actionHelpTop
		case key.Matches(msg, k.Bottom):
			return actionHelpBottom
		case key.Matches(msg, k.Back), key.Matches(msg, k.Help):
			return actionHelp
		case key.Matches(msg, k.Quit):
			return actionQuit
		default:
			return actionNone
		}
	}

	switch {
	case key.Matches(msg, k.Up):
		return actionUp
	case key.Matches(msg, k.Down):
		return actionDown
	case key.Matches(msg, k.PageUp):
		return actionPageUp
	case key.Matches(msg, k.PageDown):
		return actionPageDown
	case key.Matches(msg, k.Top):
		return actionTop
	case key.Matches(msg, k.Bottom):
		return actionBottom
	case key.Matches(msg, k.Open):
		return actionOpen
	case key.Matches(msg, k.Back):
		return actionBack
	case key.Matches(msg, k.Search) && ctx.Screen != ScreenRecords:
		return actionSearch
	case key.Matches(msg, k.Locate) && ctx.Screen == ScreenRecords:
		return actionSearch
	case key.Matches(msg, k.Refresh):
		return actionRefresh
	case key.Matches(msg, k.Copybook) && ctx.Screen == ScreenRecords:
		return actionCopybook
	case key.Matches(msg, k.ClearOverlay) && ctx.Screen == ScreenRecords:
		return actionClearOverlay
	case key.Matches(msg, k.ToggleOverlay) && ctx.Screen == ScreenRecords:
		return actionToggleOverlay
	case key.Matches(msg, k.ToggleView) && ctx.Screen == ScreenRecords:
		return actionToggleView
	case key.Matches(msg, k.Diagnostics) && ctx.Screen == ScreenRecords:
		return actionDiagnostics
	case key.Matches(msg, k.WideLeft) && ctx.Screen == ScreenRecords:
		return actionWideLeft
	case key.Matches(msg, k.WideRight) && ctx.Screen == ScreenRecords:
		return actionWideRight
	case key.Matches(msg, k.Help):
		return actionHelp
	case key.Matches(msg, k.Quit):
		return actionQuit
	case ctx.Tabs && key.Matches(msg, k.NextProfile):
		return actionNextProfile
	case ctx.Tabs && key.Matches(msg, k.PreviousProfile):
		return actionPreviousProfile
	default:
		return actionNone
	}
}

func (k KeyMap) shortHelp(ctx keyContext, overlay bool) []key.Binding {
	if ctx.ShowHelp {
		return []key.Binding{k.Up, k.Down, k.PageUp, k.PageDown, k.Back, k.Help}
	}
	if ctx.DialogOpen {
		return []key.Binding{k.Accept, k.Cancel, k.NextField}
	}
	if ctx.InputFocused {
		return []key.Binding{k.Accept, k.Cancel}
	}
	bindings := []key.Binding{k.Up, k.Down, k.Open}
	if ctx.Screen != ScreenRecords {
		bindings = append(bindings, k.Search)
	} else {
		bindings = append(bindings, k.Locate, k.WideLeft, k.WideRight, k.Copybook)
		if overlay {
			bindings = append(bindings, k.ToggleOverlay, k.ToggleView, k.ClearOverlay)
		}
	}
	if ctx.Tabs {
		bindings = append(bindings, k.NextProfile, k.PreviousProfile)
	}
	return append(bindings, k.Help, k.Quit)
}

type helpGroup struct {
	Name     string
	Bindings []key.Binding
}

func (k KeyMap) fullHelp(ctx keyContext, overlay bool) []helpGroup {
	if ctx.DialogOpen || ctx.InputFocused {
		return []helpGroup{{Name: "KEYS", Bindings: k.shortHelp(ctx, overlay)}}
	}
	pageUp, pageDown := k.PageUp, k.PageDown
	if ctx.Screen == ScreenRecords && ctx.Mode == ModeJSON {
		pageUp.SetHelp("pgup", "scroll JSON up")
		pageDown.SetHelp("pgdn", "scroll JSON down")
	}
	navigation := []key.Binding{k.Up, k.Down, pageUp, pageDown, k.Top, k.Bottom, k.Open, k.Back}
	actions := []key.Binding{k.Refresh}
	if ctx.Screen != ScreenRecords {
		actions = append(actions, k.Search)
	} else {
		actions = append(actions, k.Locate, k.WideLeft, k.WideRight, k.Copybook)
		if overlay {
			actions = append(actions, k.ToggleOverlay, k.ToggleView, k.Diagnostics, k.ClearOverlay)
		}
	}
	general := []key.Binding{k.Help, k.Quit}
	if ctx.Tabs {
		general = append(general, k.NextProfile, k.PreviousProfile)
	}
	return []helpGroup{
		{Name: "NAVIGATION", Bindings: navigation},
		{Name: "ACTIONS", Bindings: actions},
		{Name: "GENERAL", Bindings: general},
	}
}
