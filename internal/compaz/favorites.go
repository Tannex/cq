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

// favoritesPopup is the composited favorites list opened from the data set
// screen. It works on a most-recently-used snapshot of the store; every
// mutation is written through and reflected in the snapshot immediately.
type favoritesPopup struct {
	entries  []favorites.Favorite
	selected int
	note     textinput.Model
	editing  bool
	// pattern is the add/edit input for favorite patterns; adding
	// distinguishes a new favorite from renaming the selected one.
	pattern        textinput.Model
	patternEditing bool
	adding         bool
	err            string
}

func newFavoritesPopup(entries []favorites.Favorite, width int) *favoritesPopup {
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
	popup := &favoritesPopup{entries: entries, note: note, pattern: pattern}
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

// openFavoritesPopup snapshots the store in most-recently-used order and shows
// the popup. Without a configured store the action degrades to a status note.
func (m *Model) openFavoritesPopup() {
	if m.deps.Favorites == nil {
		m.ws().status = status{Level: statusWarn, Text: "favorites persistence is unavailable"}
		return
	}
	m.favPopup = newFavoritesPopup(m.deps.Favorites.Favorites(), m.width)
}

// toggleFavorite favorites the selected data set by exact name, or removes the
// exact entry. Wildcard favorites covering the row are never mutated.
func (m *Model) toggleFavorite() {
	ws := m.ws()
	if m.deps.Favorites == nil {
		ws.status = status{Level: statusWarn, Text: "favorites persistence is unavailable"}
		return
	}
	index := ws.datasetPage.selectedIndex()
	if index < 0 || index >= len(ws.datasets) {
		return
	}
	name := strings.ToUpper(strings.TrimSpace(ws.datasets[index].Name))
	added, err := m.deps.Favorites.Toggle(name)
	if err != nil {
		ws.status = status{Level: statusError, Text: err.Error()}
		return
	}
	if added {
		ws.status = status{Level: statusReady, Text: "favorited " + name}
		return
	}
	text := "removed favorite " + name
	if m.deps.Favorites.Matches(name) {
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
// prefilled with the selected pattern for a rename.
func (m *Model) beginFavoritePattern(adding bool) tea.Cmd {
	popup := m.favPopup
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
		err = m.deps.Favorites.Add(typed)
	} else {
		entry, ok := popup.selectedEntry()
		if !ok {
			popup.patternEditing = false
			popup.pattern.Blur()
			return
		}
		err = m.deps.Favorites.Rename(entry.Pattern, typed)
	}
	if err != nil {
		popup.err = err.Error()
		return
	}
	popup.entries = m.deps.Favorites.Favorites()
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
	if err := m.deps.Favorites.SetNote(entry.Pattern, note); err != nil {
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
	removed, err := m.deps.Favorites.Remove(entry.Pattern)
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

// jumpToFavorite applies the selected favorite as the data set prefix and
// fetches. An exact favorite lists (and therefore selects) that single data
// set; a wildcard favorite acts as the prefix filter for the family.
func (m *Model) jumpToFavorite() tea.Cmd {
	popup := m.favPopup
	entry, ok := popup.selectedEntry()
	if !ok {
		return nil
	}
	m.favPopup = nil
	_ = m.deps.Favorites.Touch(entry.Pattern)

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

// openFavorite opens an exact favorite directly: the jump fetch runs with the
// data set queued for auto-open once the listing lands, so the normal
// openSelection path (PS/PO routing, migrated-recall suggestion, usage
// tracking) applies. Pattern favorites have nothing unambiguous to open and
// degrade to the set-as-filter jump.
func (m *Model) openFavorite() tea.Cmd {
	popup := m.favPopup
	entry, ok := popup.selectedEntry()
	if !ok {
		return nil
	}
	command := m.jumpToFavorite()
	if !entry.Wildcard() {
		m.ws().autoOpen = entry.Pattern
	}
	return command
}

// favoriteMarker returns the row marker for a data set name: an exact or
// wildcard favorite shows the star.
func (m *Model) favoriteMarker(name string) string {
	if m.deps.Favorites != nil && m.deps.Favorites.Matches(name) {
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
		line := fmt.Sprintf("%s %s %s %s", marker, favoriteMark, kind, entry.Pattern)
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

// favoritesPanel is the tiny-terminal fallback: a full-area panel in the data
// region, mirroring helpPanel.
func (m *Model) favoritesPanel() string {
	lines := []string{consolePalette.panel.Bold(true).Width(m.width).Render("  FAVORITES  enter jump  o open  a add  e edit  n note  x remove  esc close")}
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
	footer := muted.Render("enter jump  o open  a add  e edit  n note  x remove  esc close")
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
