package compaz

import (
	"strings"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"

	"github.com/Tannex/cq/internal/dsnmap"
)

// mappingEntry is one persisted mapping shown in the mapping view.
type mappingEntry struct {
	Mapping dsnmap.Mapping
	Applied bool
}

// mappingView is the combined copybook mapping screen opened with c: a list of
// every persisted mapping matching the current data set (so unintentional
// matches are visible), plus an inline add/edit form. Persistence is
// transparent: applying a copybook saves its mapping in the background and no
// dedicated save key exists.
type mappingView struct {
	target   string // matchName the view was opened for
	entries  []mappingEntry
	selected int
	form     *mappingForm // nil while the list has focus
	err      string
	note     string
}

// handleMappingKey owns key dispatch while the mapping view is open,
// mirroring the editor/favorites/query popups' dedicated handlers.
func (m *Model) handleMappingKey(ws *workspace, msg tea.KeyPressMsg, selectedAction action) tea.Cmd {
	view := m.mappingView
	if view.form != nil {
		switch selectedAction {
		case actionAccept:
			return m.submitMappingForm(ws)
		case actionCancel:
			view.form = nil
			if len(view.entries) == 0 {
				m.mappingView = nil
			}
			return nil
		case actionNextField:
			return view.form.moveFocus(1)
		case actionPreviousField:
			return view.form.moveFocus(-1)
		default:
			return view.form.update(msg)
		}
	}
	switch selectedAction {
	case actionUp:
		view.move(-1)
		return nil
	case actionDown:
		view.move(1)
		return nil
	case actionAccept:
		return m.applySelectedMapping(ws)
	case actionMappingAdd:
		view.openForm(CopybookSource{}, view.target, false)
		view.setWidth(m.width)
		return nil
	case actionMappingEdit:
		if entry := view.selectedEntry(); entry != nil {
			view.openForm(mappingSource(entry.Mapping), entry.Mapping.Pattern, true)
			view.setWidth(m.width)
		}
		return nil
	case actionMappingRemove:
		return m.removeSelectedMapping(ws)
	case actionCancel:
		m.mappingView = nil
		return nil
	default:
		return nil
	}
}

// mappingForm edits one mapping. The quick add form exposes only the copybook
// DSN and the pattern; editing an existing entry additionally exposes the
// 01-level record name as an advanced field.
type mappingForm struct {
	copybook textinput.Model
	pattern  textinput.Model
	record   textinput.Model
	editing  bool
	// original is the pattern the edited entry was stored under, so a pattern
	// change replaces the old entry instead of leaving an orphan behind.
	original string
	// originalLocal preserves a local-file source across an edit that leaves
	// the copybook field untouched, since new values are entered as DSNs.
	originalLocal string
	focus         int
}

func newMappingForm(source CopybookSource, pattern string, editing bool) *mappingForm {
	newInput := func(prompt, placeholder string, limit int) textinput.Model {
		input := textinput.New()
		input.Prompt = prompt
		input.Placeholder = placeholder
		input.CharLimit = limit
		input.SetWidth(58)
		styleInput(&input, consolePalette.plain)
		return input
	}
	form := &mappingForm{
		copybook: newInput("COPYBOOK ", "HLQ.COPYLIB(MEMBER)", 4096),
		pattern:  newInput("PATTERN  ", "exact DSN, wildcard (* any run, % one char), or /regex/", 60),
		record:   newInput("RECORD   ", "optional 01-level record", 64),
		editing:  editing,
	}
	form.copybook.SetValue(source.label())
	form.pattern.SetValue(pattern)
	form.record.SetValue(source.Record)
	form.originalLocal = strings.TrimSpace(source.Local)
	if editing {
		form.original = dsnmap.NormalizePattern(pattern)
	}
	form.focusAt(0)
	return form
}

// formSource classifies the copybook input: an untouched prefill of a
// local-file mapping stays local, as does any value with a path separator
// (hand-edited or CLI-created mappings remain editable); anything else is a
// data set name. New sources are entered as DSNs.
func (f *mappingForm) formSource() CopybookSource {
	value := strings.TrimSpace(f.copybook.Value())
	source := CopybookSource{Record: strings.TrimSpace(f.record.Value())}
	if value != "" && (value == f.originalLocal || strings.ContainsAny(value, "/\\")) {
		source.Local = value
	} else {
		source.DSN = value
	}
	return source
}

func (f *mappingForm) inputs() []*textinput.Model {
	if f.editing {
		return []*textinput.Model{&f.copybook, &f.pattern, &f.record}
	}
	return []*textinput.Model{&f.copybook, &f.pattern}
}

func (f *mappingForm) setWidth(width int) {
	// Leave room for the three-column focus marker, the nine-column prompt,
	// and the cursor cell so a focused field never overflows the terminal.
	fieldWidth := max(8, width-16)
	for _, input := range f.inputs() {
		input.SetWidth(fieldWidth)
	}
}

func (f *mappingForm) focusAt(index int) tea.Cmd {
	inputs := f.inputs()
	if index < 0 {
		index = len(inputs) - 1
	}
	if index >= len(inputs) {
		index = 0
	}
	for _, input := range inputs {
		input.Blur()
	}
	f.focus = index
	return inputs[index].Focus()
}

func (f *mappingForm) moveFocus(delta int) tea.Cmd {
	return f.focusAt(f.focus + delta)
}

func (f *mappingForm) update(msg tea.Msg) tea.Cmd {
	inputs := f.inputs()
	updated, cmd := inputs[f.focus].Update(msg)
	*inputs[f.focus] = updated
	return cmd
}

func (v *mappingView) setWidth(width int) {
	if v.form != nil {
		v.form.setWidth(width)
	}
}

func (v *mappingView) move(delta int) {
	if len(v.entries) == 0 {
		v.selected = 0
		return
	}
	v.selected = min(len(v.entries)-1, max(0, v.selected+delta))
}

func (v *mappingView) selectedEntry() *mappingEntry {
	if v.selected < 0 || v.selected >= len(v.entries) {
		return nil
	}
	return &v.entries[v.selected]
}

// removeSelected drops the selected entry from the visible list and returns
// its pattern, or "" when the list is empty.
func (v *mappingView) removeSelected() string {
	entry := v.selectedEntry()
	if entry == nil {
		return ""
	}
	pattern := entry.Mapping.Pattern
	v.entries = append(v.entries[:v.selected], v.entries[v.selected+1:]...)
	v.move(0)
	return pattern
}

// openForm switches the view into form mode. Editing seeds the form from the
// selected entry; adding seeds the pattern with the exact data set name.
func (v *mappingView) openForm(source CopybookSource, pattern string, editing bool) {
	v.form = newMappingForm(source, pattern, editing)
	v.err = ""
	v.note = ""
}
