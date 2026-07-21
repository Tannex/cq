package compaz

import (
	"fmt"
	"strings"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/Tannex/cq/internal/dsnmap"
	"github.com/Tannex/cq/internal/favorites"
	"github.com/Tannex/cq/internal/zosmf"
)

const favoriteMark = "★"

// favoritesPopup is the composited favorites list, opened from either the
// data set screen (kind data set) or the jobs screen (kind job — owner/prefix
// filter bookmarks). It works on a most-recently-used snapshot of the store;
// every mutation is written through and reflected in the snapshot
// immediately.
type favoritesPopup struct {
	kind     favorites.Kind
	entries  []favorites.Favorite
	selected int
	note     textinput.Model
	editing  bool
	// pattern is the add/edit input for favorite patterns; adding
	// distinguishes a new favorite from renaming the selected one. Only
	// used for KindDataSet — job bookmarks are captured from the current
	// owner/prefix filter instead of typed by hand.
	pattern        textinput.Model
	patternEditing bool
	adding         bool
	err            string
}

func newFavoritesPopup(kind favorites.Kind, entries []favorites.Favorite, width int) *favoritesPopup {
	note := textinput.New()
	note.Prompt = "NOTE  "
	note.Placeholder = "free-text note"
	note.CharLimit = 120
	styles := note.Styles()
	styles.Cursor.Blink = false
	note.SetStyles(styles)
	pattern := textinput.New()
	pattern.Prompt = "PATTERN  "
	pattern.Placeholder = `name, wildcard (*, %), or /regex/`
	pattern.CharLimit = 120
	patternStyles := pattern.Styles()
	patternStyles.Cursor.Blink = false
	pattern.SetStyles(patternStyles)
	popup := &favoritesPopup{kind: kind, entries: entries, note: note, pattern: pattern}
	popup.setWidth(width)
	return popup
}

func (p *favoritesPopup) setWidth(width int) {
	p.note.SetWidth(max(8, min(60, width-14)))
	p.pattern.SetWidth(max(8, min(60, width-16)))
}

// inputActive reports whether a text input owns the keyboard, so printable
// keys reach it instead of the list bindings.
func (p *favoritesPopup) inputActive() bool {
	return p.editing || p.patternEditing
}

func (p *favoritesPopup) selectedEntry() (favorites.Favorite, bool) {
	if p.selected < 0 || p.selected >= len(p.entries) {
		return favorites.Favorite{}, false
	}
	return p.entries[p.selected], true
}

func (p *favoritesPopup) move(delta int) {
	if len(p.entries) == 0 {
		return
	}
	p.selected = min(len(p.entries)-1, max(0, p.selected+delta))
}

// favoritesKindForScreen selects which family of favorites a screen deals
// in: job owner/prefix bookmarks on the jobs screen, data set names/patterns
// everywhere favorites are reachable from (currently only ScreenDataSets).
func favoritesKindForScreen(screen Screen) favorites.Kind {
	if screen == ScreenJobs {
		return favorites.KindJob
	}
	return favorites.KindDataSet
}

// openFavoritesPopup snapshots the store in most-recently-used order and shows
// the popup, scoped to the current screen's favorite kind. Without a
// configured store the action degrades to a status note.
func (m *Model) openFavoritesPopup() {
	if m.deps.Favorites == nil {
		m.ws().status = status{Level: statusWarn, Text: "favorites persistence is unavailable"}
		return
	}
	kind := favoritesKindForScreen(m.ws().screen)
	m.favPopup = newFavoritesPopup(kind, m.deps.Favorites.Favorites(m.activeProfile(), kind), m.width)
}

// toggleFavorite favorites the current row or filter, or removes the
// existing exact entry, depending on which screen is active.
func (m *Model) toggleFavorite() {
	ws := m.ws()
	if m.deps.Favorites == nil {
		ws.status = status{Level: statusWarn, Text: "favorites persistence is unavailable"}
		return
	}
	if ws.screen == ScreenJobs {
		m.toggleJobFavorite()
		return
	}
	m.toggleDataSetFavorite()
}

// toggleJobFavorite favorites (or un-favorites) the current owner/prefix
// filter as one bookmark, keyed the same way ws.jobIdentity() is.
func (m *Model) toggleJobFavorite() {
	ws := m.ws()
	name := ws.jobIdentity()
	added, err := m.deps.Favorites.Toggle(m.activeProfile(), favorites.KindJob, name)
	if err != nil {
		ws.status = status{Level: statusError, Text: err.Error()}
		return
	}
	label := displayOr(ws.jobOwner, "*") + " / " + displayOr(ws.jobPrefix, "*")
	if added {
		ws.status = status{Level: statusReady, Text: "favorited " + label}
		return
	}
	ws.status = status{Level: statusReady, Text: "removed favorite " + label}
}

// toggleDataSetFavorite favorites the selected data set by exact name, or
// removes the exact entry. Wildcard favorites covering the row are never
// mutated.
func (m *Model) toggleDataSetFavorite() {
	ws := m.ws()
	index := ws.datasetPage.selectedIndex()
	if index < 0 || index >= len(ws.datasets) {
		return
	}
	name := strings.ToUpper(strings.TrimSpace(ws.datasets[index].Name))
	added, err := m.deps.Favorites.Toggle(m.activeProfile(), favorites.KindDataSet, name)
	if err != nil {
		ws.status = status{Level: statusError, Text: err.Error()}
		return
	}
	if added {
		ws.status = status{Level: statusReady, Text: "favorited " + name}
		return
	}
	text := "removed favorite " + name
	if m.deps.Favorites.Matches(m.activeProfile(), favorites.KindDataSet, name) {
		text += " (still covered by a wildcard favorite)"
	}
	ws.status = status{Level: statusReady, Text: text}
}

// handleFavoritesKey routes keys while the favorites popup is open.
func (m *Model) handleFavoritesKey(msg tea.KeyPressMsg, selected action) tea.Cmd {
	popup := m.favPopup
	switch selected {
	case actionUp:
		popup.move(-1)
		return nil
	case actionDown:
		popup.move(1)
		return nil
	case actionAccept:
		if popup.patternEditing {
			m.saveFavoritePattern()
			return nil
		}
		if popup.editing {
			m.saveFavoriteNote()
			return nil
		}
		return m.jumpToFavorite()
	case actionCancel:
		if popup.patternEditing {
			popup.patternEditing = false
			popup.pattern.Blur()
			popup.err = ""
			return nil
		}
		if popup.editing {
			popup.editing = false
			popup.note.Blur()
			popup.err = ""
			return nil
		}
		m.favPopup = nil
		return nil
	case actionFavoriteNote:
		return m.beginFavoriteNote()
	case actionFavoriteAdd:
		return m.beginFavoritePattern(true)
	case actionFavoriteEdit:
		return m.beginFavoritePattern(false)
	case actionFavoriteOpen:
		return m.openFavorite()
	case actionFavoriteRemove:
		m.removePopupFavorite()
		return nil
	case actionQuit:
		m.cancelAll()
		return tea.Quit
	default:
		if popup.patternEditing {
			updated, cmd := popup.pattern.Update(msg)
			popup.pattern = updated
			return cmd
		}
		if popup.editing {
			updated, cmd := popup.note.Update(msg)
			popup.note = updated
			return cmd
		}
		return nil
	}
}

// beginFavoritePattern opens the pattern input: empty for a new favorite,
// prefilled with the selected pattern for a rename. Job filter bookmarks
// have nothing to hand-type — they are captured from the current owner/
// prefix filter via f — so this degrades to a status message for that kind.
func (m *Model) beginFavoritePattern(adding bool) tea.Cmd {
	popup := m.favPopup
	if popup.kind == favorites.KindJob {
		popup.err = "job favorites are captured with f on the jobs screen, not typed"
		return nil
	}
	value := ""
	if !adding {
		entry, ok := popup.selectedEntry()
		if !ok {
			return nil
		}
		value = entry.Pattern
	}
	popup.patternEditing = true
	popup.adding = adding
	popup.err = ""
	popup.pattern.SetValue(value)
	popup.pattern.CursorEnd()
	return popup.pattern.Focus()
}

// saveFavoritePattern validates and persists the typed pattern, then
// re-snapshots the store so ordering and normalization match what was saved.
// Validation errors keep the input open with the error shown inline.
func (m *Model) saveFavoritePattern() {
	popup := m.favPopup
	typed := strings.TrimSpace(popup.pattern.Value())
	pattern := dsnmap.NormalizePattern(typed)
	var err error
	if popup.adding {
		err = m.deps.Favorites.Add(m.activeProfile(), popup.kind, typed)
	} else {
		entry, ok := popup.selectedEntry()
		if !ok {
			popup.patternEditing = false
			popup.pattern.Blur()
			return
		}
		err = m.deps.Favorites.Rename(m.activeProfile(), popup.kind, entry.Pattern, typed)
	}
	if err != nil {
		popup.err = err.Error()
		return
	}
	popup.entries = m.deps.Favorites.Favorites(m.activeProfile(), popup.kind)
	for i, entry := range popup.entries {
		if entry.Pattern == pattern {
			popup.selected = i
			break
		}
	}
	popup.move(0)
	popup.patternEditing = false
	popup.pattern.Blur()
	popup.err = ""
}

func (m *Model) beginFavoriteNote() tea.Cmd {
	popup := m.favPopup
	entry, ok := popup.selectedEntry()
	if !ok {
		return nil
	}
	popup.editing = true
	popup.err = ""
	popup.note.SetValue(entry.Note)
	popup.note.CursorEnd()
	return popup.note.Focus()
}

func (m *Model) saveFavoriteNote() {
	popup := m.favPopup
	entry, ok := popup.selectedEntry()
	if !ok {
		popup.editing = false
		popup.note.Blur()
		return
	}
	note := strings.TrimSpace(popup.note.Value())
	if err := m.deps.Favorites.SetNote(m.activeProfile(), popup.kind, entry.Pattern, note); err != nil {
		popup.err = err.Error()
		return
	}
	popup.entries[popup.selected].Note = note
	popup.editing = false
	popup.note.Blur()
	popup.err = ""
}

func (m *Model) removePopupFavorite() {
	popup := m.favPopup
	entry, ok := popup.selectedEntry()
	if !ok {
		return
	}
	removed, err := m.deps.Favorites.Remove(m.activeProfile(), popup.kind, entry.Pattern)
	if err != nil {
		popup.err = err.Error()
		return
	}
	if !removed {
		popup.err = "no favorite stored for " + entry.Pattern
		return
	}
	popup.entries = append(popup.entries[:popup.selected], popup.entries[popup.selected+1:]...)
	popup.move(0)
	popup.err = ""
}

// jumpToFavorite applies the selected favorite, dispatching by kind: a data
// set favorite becomes the data set prefix filter, a job favorite becomes
// the jobs owner/prefix filter.
func (m *Model) jumpToFavorite() tea.Cmd {
	popup := m.favPopup
	if popup.kind == favorites.KindJob {
		return m.jumpToJobFavorite()
	}
	return m.jumpToDataSetFavorite()
}

// jumpToDataSetFavorite applies the selected favorite as the data set prefix
// and fetches. An exact favorite lists (and therefore selects) that single
// data set; a wildcard favorite acts as the prefix filter for the family.
func (m *Model) jumpToDataSetFavorite() tea.Cmd {
	popup := m.favPopup
	entry, ok := popup.selectedEntry()
	if !ok {
		return nil
	}
	m.favPopup = nil
	_ = m.deps.Favorites.Touch(m.activeProfile(), favorites.KindDataSet, entry.Pattern)

	ws := m.ws()
	m.cancelSearch()
	ws.cancelBrowse()
	ws.cancelDecode()
	ws.screen = ScreenDataSets
	ws.prefix = entry.Pattern
	m.prefixInput.SetValue(entry.Pattern)
	ws.datasets = nil
	ws.datasetTotal = nil
	ws.dataSet = zosmf.DataSet{}
	ws.resetMemberState()
	ws.datasetPage.reset(m.visible, m.budget)
	if m.budget <= 0 {
		return nil
	}
	return m.startDataSets(ws, ws.datasetPage.initialPlan(""))
}

// jumpToJobFavorite applies the selected owner/prefix bookmark to the jobs
// screen and fetches. The favorite's Pattern is "OWNER|PREFIX" (the same
// identity ws.jobIdentity() computes); an entry that predates a format
// change or was corrupted degrades to a status warning rather than a panic.
func (m *Model) jumpToJobFavorite() tea.Cmd {
	popup := m.favPopup
	entry, ok := popup.selectedEntry()
	if !ok {
		return nil
	}
	owner, prefix, ok := strings.Cut(entry.Pattern, "|")
	if !ok {
		m.favPopup = nil
		m.ws().status = status{Level: statusWarn, Text: "malformed job favorite " + entry.Pattern}
		return nil
	}
	m.favPopup = nil
	_ = m.deps.Favorites.Touch(m.activeProfile(), favorites.KindJob, entry.Pattern)

	ws := m.ws()
	if _, jobsOK := ws.browser.(zosmf.JobBrowser); !jobsOK {
		ws.status = status{Level: statusWarn, Text: "this session cannot browse jobs"}
		return nil
	}
	m.cancelSearch()
	ws.cancelBrowse()
	ws.screen = ScreenJobs
	ws.jobOwner = owner
	ws.jobPrefix = prefix
	ws.resetJobsState()
	if m.budget <= 0 {
		return nil
	}
	return m.startJobs(ws, ws.jobPage.initialPlan(""))
}

// openFavorite opens an exact data set favorite directly: the jump fetch
// runs with the data set queued for auto-open once the listing lands, so the
// normal openSelection path (PS/PO routing, migrated-recall suggestion,
// usage tracking) applies. Pattern favorites have nothing unambiguous to
// open and degrade to the set-as-filter jump, as does every job favorite
// (an owner/prefix bookmark is always a filter, never a single row).
func (m *Model) openFavorite() tea.Cmd {
	popup := m.favPopup
	entry, ok := popup.selectedEntry()
	if !ok {
		return nil
	}
	if popup.kind == favorites.KindJob {
		return m.jumpToJobFavorite()
	}
	command := m.jumpToDataSetFavorite()
	// Arm the queue only when the jump actually dispatched its fetch (a zero
	// row budget dispatches nothing), and bind it to that fetch's generation.
	if command != nil && !entry.Wildcard() {
		ws := m.ws()
		ws.autoOpen = entry.Pattern
		ws.autoOpenGeneration = ws.browseGeneration
	}
	return command
}

// favoriteMarker returns the row marker for a data set name: an exact or
// wildcard favorite shows the star.
func (m *Model) favoriteMarker(name string) string {
	if m.deps.Favorites != nil && m.deps.Favorites.Matches(m.activeProfile(), favorites.KindDataSet, name) {
		return favoriteMark
	}
	return " "
}

// favoritesContent renders the popup body lines through the supplied styles so
// every cell carries the popup background (see helpContent).
func (m *Model) favoritesContent(accent, body, muted lipgloss.Style, width int) []string {
	popup := m.favPopup
	var lines []string
	if popup.patternEditing && popup.adding {
		lines = append(lines, truncateStyled(popup.pattern.View(), width))
	}
	if len(popup.entries) == 0 {
		if popup.err != "" {
			lines = append(lines, "", body.Render(truncateStyled("ERROR  "+popup.err, width)))
		}
		if len(lines) > 0 {
			return lines
		}
		if popup.kind == favorites.KindJob {
			return []string{muted.Render("no job favorites yet — press f on the jobs screen to bookmark the current filter")}
		}
		return []string{muted.Render("no favorites yet — press f on a data set or a to add a pattern")}
	}
	for i, entry := range popup.entries {
		marker := " "
		if i == popup.selected {
			marker = ">"
		}
		kind := " "
		if entry.Wildcard() {
			kind = "~"
		}
		display := entry.Pattern
		if popup.kind == favorites.KindJob {
			if owner, prefix, ok := strings.Cut(entry.Pattern, "|"); ok {
				display = displayOr(owner, "*") + " / " + displayOr(prefix, "*")
			}
			kind = " "
		}
		line := fmt.Sprintf("%s %s %s %s", marker, favoriteMark, kind, display)
		if i == popup.selected {
			lines = append(lines, accent.Render(truncateStyled(line, width)))
		} else {
			lines = append(lines, body.Render(truncateStyled(line, width)))
		}
		if i == popup.selected && popup.patternEditing && !popup.adding {
			lines = append(lines, truncateStyled("      "+popup.pattern.View(), width))
			continue
		}
		if i == popup.selected && popup.editing {
			lines = append(lines, truncateStyled("      "+popup.note.View(), width))
			continue
		}
		if entry.Note != "" {
			lines = append(lines, muted.Render(truncateStyled("      "+entry.Note, width)))
		}
	}
	if popup.err != "" {
		lines = append(lines, "", body.Render(truncateStyled("ERROR  "+popup.err, width)))
	}
	return lines
}

// footerText is the popup's key hint line: job favorites are captured with
// f on the jobs screen rather than typed, so open/add/edit don't apply.
func (p *favoritesPopup) footerText() string {
	if p.kind == favorites.KindJob {
		return "enter jump  n note  x remove  esc close"
	}
	return "enter jump  o open  a add  e edit  n note  x remove  esc close"
}

// favoritesPanel is the tiny-terminal fallback: a full-area panel in the data
// region, mirroring helpPanel.
func (m *Model) favoritesPanel() string {
	lines := []string{consolePalette.panel.Bold(true).Width(m.width).Render("  FAVORITES  " + m.favPopup.footerText())}
	lines = append(lines, m.favoritesContent(consolePalette.selected, consolePalette.plain, consolePalette.muted, m.width)...)
	capacity := m.visible + 1
	offset := max(0, len(lines)-capacity)
	lines = scrollWindow(lines, offset, capacity)
	for len(lines) < capacity {
		lines = append(lines, "")
	}
	return strings.Join(lines, "\n")
}

// overlayFavorites composites the favorites popup over the live view with the
// same Canvas/Layer mechanism and sizing rules as the help popup.
func (m *Model) overlayFavorites(background string) string {
	popupWidth := min(64, m.width-4)
	popupHeight := min(max(10, int(float64(m.height)*0.8)), m.height-2)
	if popupWidth < 24 || popupHeight < 8 {
		return background
	}

	innerWidth := popupWidth - 4
	contentHeight := popupHeight - 4

	fill := consolePalette.popup.Width(innerWidth)
	accent := consolePalette.cyan.Bold(true).Inherit(consolePalette.popup)
	body := consolePalette.popup
	muted := consolePalette.muted.Inherit(consolePalette.popup)

	lines := m.favoritesContent(accent, body, muted, innerWidth)
	// Keep the selected entry visible: scroll so its first line is in window.
	offset := 0
	if selectedLine := m.favPopup.selectedLineIndex(); selectedLine >= contentHeight {
		offset = selectedLine - contentHeight + 1
	}
	contentLines := scrollWindow(lines, offset, contentHeight)

	title := accent.Render("FAVORITES")
	innerLines := []string{
		fill.Render(strings.Repeat(" ", max(0, (innerWidth-lipgloss.Width(title))/2)) + title),
	}
	for _, line := range contentLines {
		innerLines = append(innerLines, fill.Render(truncateStyled(line, innerWidth)))
	}
	for len(innerLines) < contentHeight+1 {
		innerLines = append(innerLines, fill.Render(""))
	}
	footer := muted.Render(m.favPopup.footerText())
	innerLines = append(innerLines, fill.Render(strings.Repeat(" ", max(0, (innerWidth-lipgloss.Width(footer))/2))+footer))

	popupStyle := consolePalette.popup.
		Border(lipgloss.RoundedBorder()).
		BorderForeground(consolePalette.popupBorder.GetForeground()).
		BorderBackground(consolePalette.popup.GetBackground()).
		Padding(0, 1).
		Width(popupWidth).
		Height(popupHeight)
	popup := popupStyle.Render(strings.Join(innerLines, "\n"))

	x := (m.width - popupWidth) / 2
	y := (m.height - popupHeight) / 2

	canvas := lipgloss.NewCanvas(m.width, m.height)
	mainLayer := lipgloss.NewLayer(background).X(0).Y(0).Z(0)
	popupLayer := lipgloss.NewLayer(popup).X(x).Y(y).Z(1)
	compositor := lipgloss.NewCompositor(mainLayer, popupLayer)
	return canvas.Compose(compositor).Render()
}

// selectedLineIndex returns the rendered line index of the selected entry, so
// the popup viewport can keep it in view (entries with notes span two lines,
// and the add-pattern input occupies the first line while open).
func (p *favoritesPopup) selectedLineIndex() int {
	line := 0
	if p.patternEditing && p.adding {
		line++
	}
	for i, entry := range p.entries {
		if i == p.selected {
			return line
		}
		line++
		if entry.Note != "" {
			line++
		}
	}
	return line
}
