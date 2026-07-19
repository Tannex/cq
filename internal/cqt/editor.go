package cqt

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"charm.land/bubbles/v2/textarea"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/Tannex/cq/internal/zosmf"
)

// editTarget identifies the data set or member being edited together with the
// geometry needed to validate a write-back.
type editTarget struct {
	DataSet      string
	Member       string
	RecordFormat string
	RecordLength int
}

func (t editTarget) label() string {
	if t.Member != "" {
		return t.DataSet + "(" + t.Member + ")"
	}
	return t.DataSet
}

// lineLimit is the longest text line the target can store: LRECL for
// fixed-format records, LRECL minus the four-byte record descriptor word for
// variable formats, and unlimited when the length is unknown.
func (t editTarget) lineLimit() int {
	if t.RecordLength <= 0 {
		return 0
	}
	if strings.HasPrefix(strings.ToUpper(strings.TrimSpace(t.RecordFormat)), "V") {
		return max(1, t.RecordLength-4)
	}
	return t.RecordLength
}

// editorState is the modal editor opened over the active workspace. The buffer
// only ever reaches the host through the explicit save binding.
type editorState struct {
	profile string
	target  editTarget
	area    textarea.Model
	// baseline is the last content known to match the host; the buffer is
	// dirty whenever it differs.
	baseline string
	etag     string
	// pendingSave holds the exact content sent by an in-flight save so a
	// successful result promotes what was written, not what was typed since.
	pendingSave    string
	saving         bool
	confirmDiscard bool
}

func (e *editorState) dirty() bool {
	return e.area.Value() != e.baseline
}

type editFetchResultMsg struct {
	Profile    string
	Generation uint64
	Target     editTarget
	Text       string
	ETag       string
	Err        error
}

type editSaveResultMsg struct {
	Profile    string
	Generation uint64
	ETag       string
	Err        error
}

// textEditor returns the session's write surface when it provides one.
func (ws *workspace) textEditor() (zosmf.TextEditor, bool) {
	editor, ok := ws.browser.(zosmf.TextEditor)
	return editor, ok
}

// selectedEditTarget resolves what e would edit on the current screen, or a
// status message explaining why the selection is not editable.
func (m *Model) selectedEditTarget(ws *workspace) (editTarget, string) {
	switch ws.screen {
	case ScreenDataSets:
		index := ws.datasetPage.selectedIndex()
		if index < 0 || index >= len(ws.datasets) {
			return editTarget{}, ""
		}
		selected := ws.datasets[index]
		if !editableRecordFormat(selected.RecordFormat) {
			return editTarget{}, fmt.Sprintf("%s uses RECFM %s; edit mode is for text content", selected.Name, selected.RecordFormat)
		}
		organization := strings.ToUpper(strings.ReplaceAll(strings.TrimSpace(selected.Organization), " ", ""))
		switch organization {
		case "PS", "SEQ", "PS-L", "PSL":
		case "PO", "PO-E", "POE", "PDS", "PDSE":
			return editTarget{}, "open the library and press e on a member to edit it"
		default:
			return editTarget{}, fmt.Sprintf("%s is not editable; edit mode supports sequential data sets and PDS members", selected.Name)
		}
		return editTarget{
			DataSet: selected.Name, RecordFormat: selected.RecordFormat, RecordLength: parseRecordLength(selected.RecordLength),
		}, ""
	case ScreenMembers:
		index := ws.memberPage.selectedIndex()
		if index < 0 || index >= len(ws.members) {
			return editTarget{}, ""
		}
		if !editableRecordFormat(ws.dataSet.RecordFormat) {
			return editTarget{}, fmt.Sprintf("%s uses RECFM %s; edit mode is for text content", ws.dataSet.Name, ws.dataSet.RecordFormat)
		}
		return editTarget{
			DataSet: ws.dataSet.Name, Member: ws.members[index].Name,
			RecordFormat: ws.dataSet.RecordFormat, RecordLength: parseRecordLength(ws.dataSet.RecordLength),
		}, ""
	default:
		return editTarget{}, ""
	}
}

// editableRecordFormat rejects undefined-format (load module) content, which
// text mode cannot round-trip.
func editableRecordFormat(recordFormat string) bool {
	return !strings.HasPrefix(strings.ToUpper(strings.TrimSpace(recordFormat)), "U")
}

func parseRecordLength(value string) int {
	length, err := strconv.Atoi(strings.TrimSpace(value))
	if err != nil || length < 0 {
		return 0
	}
	return length
}

// beginEdit fetches the selection as text and opens the editor when the result
// arrives. Nothing is written back until the explicit save binding.
func (m *Model) beginEdit() tea.Cmd {
	ws := m.ws()
	if m.options.ReadOnly {
		ws.status = status{Level: statusWarn, Text: "edit mode is disabled by --read-only"}
		return nil
	}
	if !ws.sessionReady {
		return nil
	}
	editor, ok := ws.textEditor()
	if !ok {
		ws.status = status{Level: statusWarn, Text: "this session does not support editing"}
		return nil
	}
	target, reason := m.selectedEditTarget(ws)
	if target.DataSet == "" {
		if reason != "" {
			ws.status = status{Level: statusWarn, Text: reason}
		}
		return nil
	}
	return m.startEditFetch(ws, editor, target)
}

func (m *Model) startEditFetch(ws *workspace, editor zosmf.TextEditor, target editTarget) tea.Cmd {
	m.cancelEdit()
	m.editGeneration++
	generation := m.editGeneration
	m.editPending = true
	ctx, cancel := context.WithTimeout(context.Background(), m.deps.Timeout)
	m.editCancel = cancel
	profile := ws.profile
	ws.status = status{Level: statusLoading, Text: "fetching " + target.label() + " for edit"}
	return m.loadingCommand(func() tea.Msg {
		content, err := editor.ReadText(ctx, target.label())
		return editFetchResultMsg{
			Profile: profile, Generation: generation, Target: target,
			Text: string(content.Text), ETag: content.ETag, Err: err,
		}
	})
}

func (m *Model) handleEditFetchResult(msg editFetchResultMsg) tea.Cmd {
	if msg.Generation != m.editGeneration || !m.editPending {
		return nil
	}
	m.cancelEdit()
	ws := m.ws()
	if msg.Err != nil {
		ws.status = status{Level: statusError, Text: msg.Err.Error()}
		return nil
	}
	area := textarea.New()
	area.Prompt = ""
	area.ShowLineNumbers = true
	area.CharLimit = 0
	area.MaxHeight = 0
	area.SetWidth(max(1, m.width))
	area.SetHeight(m.editorBodyHeight())
	styles := area.Styles()
	styles.Cursor.Blink = false
	area.SetStyles(styles)
	text := normalizeEditText(msg.Text)
	area.SetValue(text)
	area.MoveToBegin()
	m.editor = &editorState{
		profile: msg.Profile, target: msg.Target, area: area,
		baseline: text, etag: msg.ETag,
	}
	if msg.ETag == "" {
		ws.status = status{Level: statusWarn, Text: "editing " + msg.Target.label() + "; host returned no ETag, so saves cannot detect concurrent changes"}
	} else {
		ws.status = status{Level: statusReady, Text: "editing " + msg.Target.label()}
	}
	return m.editor.area.Focus()
}

// normalizeEditText strips carriage returns and a single trailing newline so
// the textarea buffer round-trips without growing a blank final line.
func normalizeEditText(text string) string {
	text = strings.ReplaceAll(text, "\r\n", "\n")
	return strings.TrimSuffix(text, "\n")
}

// handleEditorKey routes keys while the editor is open. Every key that is not
// an editor action feeds the textarea buffer.
func (m *Model) handleEditorKey(msg tea.KeyPressMsg, selected action) tea.Cmd {
	editor := m.editor
	switch selected {
	case actionSaveEdit:
		return m.startEditSave()
	case actionReloadEdit:
		ws := m.ws()
		remote, ok := ws.textEditor()
		if !ok {
			return nil
		}
		return m.startEditFetch(ws, remote, editor.target)
	case actionCancel:
		if editor.confirmDiscard {
			editor.confirmDiscard = false
			m.ws().status = status{Level: statusReady, Text: "editing " + editor.target.label()}
			return nil
		}
		if editor.dirty() {
			editor.confirmDiscard = true
			m.ws().status = status{Level: statusWarn, Text: "unsaved changes in " + editor.target.label() + " — d discards, esc keeps editing"}
			return nil
		}
		m.closeEditor("closed " + editor.target.label())
		return nil
	case actionDiscardEdit:
		m.closeEditor("discarded changes to " + editor.target.label())
		return nil
	case actionQuit:
		m.cancelAll()
		return tea.Quit
	default:
		if editor.confirmDiscard {
			editor.confirmDiscard = false
			m.ws().status = status{Level: statusReady, Text: "editing " + editor.target.label()}
			return nil
		}
		updated, cmd := editor.area.Update(msg)
		editor.area = updated
		return cmd
	}
}

func (m *Model) closeEditor(text string) {
	m.cancelEdit()
	m.editor = nil
	m.ws().status = status{Level: statusReady, Text: text}
}

// startEditSave validates the buffer against the target's record length and
// writes it back with If-Match protection. This is the only path that writes.
func (m *Model) startEditSave() tea.Cmd {
	editor := m.editor
	ws := m.ws()
	if editor.saving {
		return nil
	}
	if limit := editor.target.lineLimit(); limit > 0 {
		for i, line := range strings.Split(editor.area.Value(), "\n") {
			if len(line) > limit {
				ws.status = status{Level: statusError, Text: fmt.Sprintf("not saved: line %d is %d characters, longer than LRECL %d", i+1, len(line), limit)}
				return nil
			}
		}
	}
	remote, ok := ws.textEditor()
	if !ok {
		ws.status = status{Level: statusError, Text: "this session does not support editing"}
		return nil
	}
	m.cancelEdit()
	m.editGeneration++
	generation := m.editGeneration
	ctx, cancel := context.WithTimeout(context.Background(), m.deps.Timeout)
	m.editCancel = cancel
	editor.saving = true
	editor.pendingSave = editor.area.Value()
	body := editor.pendingSave
	if body != "" {
		body += "\n"
	}
	request := zosmf.WriteTextRequest{Target: editor.target.label(), Body: []byte(body), ETag: editor.etag}
	profile := ws.profile
	ws.status = status{Level: statusLoading, Text: "saving " + editor.target.label()}
	return m.loadingCommand(func() tea.Msg {
		etag, err := remote.WriteText(ctx, request)
		return editSaveResultMsg{Profile: profile, Generation: generation, ETag: etag, Err: err}
	})
}

func (m *Model) handleEditSaveResult(msg editSaveResultMsg) tea.Cmd {
	editor := m.editor
	if editor == nil || msg.Generation != m.editGeneration || !editor.saving {
		return nil
	}
	m.cancelEdit()
	editor.saving = false
	ws := m.ws()
	if msg.Err != nil {
		if zosmf.IsConflict(msg.Err) {
			ws.status = status{Level: statusError, Text: editor.target.label() + " changed on the host since it was fetched — ctrl+r reloads (discarding this buffer), or save elsewhere"}
			return nil
		}
		ws.status = status{Level: statusError, Text: "save failed: " + msg.Err.Error()}
		return nil
	}
	editor.baseline = editor.pendingSave
	if msg.ETag != "" {
		editor.etag = msg.ETag
	}
	suffix := ""
	if editor.dirty() {
		suffix = " (buffer modified again since)"
	}
	ws.status = status{Level: statusReady, Text: "saved " + editor.target.label() + suffix}
	return nil
}

func (m *Model) cancelEdit() {
	if m.editCancel != nil {
		m.editCancel()
	}
	m.editCancel = nil
	m.editPending = false
}

// editorBodyHeight is the textarea height inside the fixed editor chrome:
// title, info line, status line, and help line.
func (m *Model) editorBodyHeight() int {
	return max(1, m.height-4)
}

func (m *Model) resizeEditor() {
	if m.editor == nil {
		return
	}
	m.editor.area.SetWidth(max(1, m.width))
	m.editor.area.SetHeight(m.editorBodyHeight())
}

// editorView is the full-area editor screen with the standard chrome.
func (m *Model) editorView() string {
	editor := m.editor
	dirtyChip := ""
	if editor.saving {
		dirtyChip = consolePalette.amber.Bold(true).Inherit(consolePalette.navy).Render("SAVING")
	} else if editor.dirty() {
		dirtyChip = consolePalette.amber.Bold(true).Inherit(consolePalette.navy).Render("MODIFIED")
	}
	accent := consolePalette.cyan.Bold(true).Inherit(consolePalette.navy)
	plain := consolePalette.navy.Foreground(consolePalette.plain.GetForeground())
	title := plain.Render(" ") + accent.Render("EDIT") + plain.Render("  "+editor.target.label())
	if dirtyChip != "" {
		gap := m.width - lipgloss.Width(title) - lipgloss.Width(dirtyChip) - 1
		if gap > 0 {
			title += plain.Render(strings.Repeat(" ", gap)) + dirtyChip
		}
	}
	title = consolePalette.navy.Width(max(0, m.width)).Render(truncateStyled(title, m.width))

	info := fmt.Sprintf("RECFM %s  LRECL %s  explicit save only — nothing is written until ctrl+s",
		displayOr(editor.target.RecordFormat, "?"), displayOr(strconv.Itoa(editor.target.RecordLength), "?"))
	if editor.target.RecordLength == 0 {
		info = "explicit save only — nothing is written until ctrl+s"
	}
	infoLine := consolePalette.panel.Width(max(0, m.width)).Render(truncateStyled(" "+info, m.width))

	return fitHeight(strings.Join([]string{
		title,
		infoLine,
		editor.area.View(),
		m.statusLine(),
		m.helpLine(),
	}, "\n"), m.width, m.height)
}
