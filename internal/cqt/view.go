package cqt

import (
	"fmt"
	"strconv"
	"strings"

	"charm.land/bubbles/v2/table"
	"charm.land/bubbles/v2/viewport"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/Tannex/cq/internal/decode"
	"github.com/Tannex/cq/internal/record"
)

const hScrollStep = 8

var consolePalette = struct {
	navy, panel, header, activeTab, popup, popupBorder, cyan, green, amber, danger, muted, bright, selected, plain lipgloss.Style
}{
	navy:        lipgloss.NewStyle().Background(lipgloss.Color("#111827")).Foreground(lipgloss.Color("#CCFBF1")),
	panel:       lipgloss.NewStyle().Background(lipgloss.Color("#1F2937")).Foreground(lipgloss.Color("#CBD5E1")),
	header:      lipgloss.NewStyle().Background(lipgloss.Color("#334155")).Foreground(lipgloss.Color("#E2E8F0")).Bold(true),
	activeTab:   lipgloss.NewStyle().Background(lipgloss.Color("#334155")).Foreground(lipgloss.Color("#5EEAD4")).Bold(true),
	popup:       lipgloss.NewStyle().Background(lipgloss.Color("#0B1220")).Foreground(lipgloss.Color("#E2E8F0")),
	popupBorder: lipgloss.NewStyle().Foreground(lipgloss.Color("#2DD4BF")),
	cyan:        lipgloss.NewStyle().Foreground(lipgloss.Color("#5EEAD4")),
	green:       lipgloss.NewStyle().Foreground(lipgloss.Color("#4ADE80")),
	amber:       lipgloss.NewStyle().Foreground(lipgloss.Color("#FBBF24")),
	danger:      lipgloss.NewStyle().Foreground(lipgloss.Color("#FB7185")),
	muted:       lipgloss.NewStyle().Foreground(lipgloss.Color("#94A3B8")).Faint(true),
	bright:      lipgloss.NewStyle().Foreground(lipgloss.Color("#F8FAFC")),
	selected:    lipgloss.NewStyle().Background(lipgloss.Color("#263449")).Foreground(lipgloss.Color("#99F6E4")).Bold(true),
	plain:       lipgloss.NewStyle(),
}

func (m *Model) View() tea.View {
	var content string
	switch {
	case m.editor != nil:
		content = m.editorView()
	case m.visible <= 0 && !m.showHelp:
		content = m.tinyView()
	case m.mappingView != nil:
		content = m.mappingViewContent()
	default:
		content = m.mainView()
	}
	if m.editor == nil && m.showHelp && m.width >= MinTerminalWidth && m.visible > 0 && m.mappingView == nil {
		content = m.overlayHelp(content)
	} else if m.favPopup != nil && m.width >= MinTerminalWidth && m.visible > 0 && m.mappingView == nil && !m.showHelp {
		content = m.overlayFavorites(content)
	} else if m.query != nil && m.width >= MinTerminalWidth && m.visible > 0 && m.mappingView == nil && !m.showHelp {
		content = m.overlayQuery(content)
	}
	view := tea.NewView(content)
	view.AltScreen = true
	view.MouseMode = tea.MouseModeCellMotion
	view.WindowTitle = "cqt — z/OSMF data sets"
	return view
}

func (m *Model) tinyView() string {
	width := max(1, m.width)
	height := max(1, m.height)
	title := consolePalette.navy.Bold(true).Width(width).Render(" CQT  z/OSMF DATA SET CONSOLE ")
	message := fmt.Sprintf("TERMINAL TOO SMALL\nresize to at least %d columns × %d rows\ncurrent %d × %d\nno row request dispatched", MinTerminalWidth, MinTerminalHeight(m.hasTabs()), m.width, m.height)
	body := lipgloss.Place(width, max(1, height-1), lipgloss.Center, lipgloss.Center, consolePalette.amber.Render(message))
	return fitHeight(title+"\n"+body, width, height)
}

func (m *Model) mainView() string {
	var lines []string
	if m.hasTabs() {
		lines = append(lines, m.tabBar())
	}
	data := m.dataView()
	if m.showHelp && (m.width < MinTerminalWidth || m.visible <= 0) {
		// Tiny-terminal fallback: full-area help panel so every binding stays
		// readable; the chrome row count (and therefore the row budget) is unchanged.
		data = m.helpPanel()
	} else if m.favPopup != nil && (m.width < MinTerminalWidth || m.visible <= 0) {
		data = m.favoritesPanel()
	} else if m.query != nil && (m.width < MinTerminalWidth || m.visible <= 0) {
		data = m.queryPanel()
	}
	lines = append(lines, m.titleLine(), m.searchLine(), data, m.statusLine(), m.helpLine())
	return fitHeight(strings.Join(lines, "\n"), m.width, m.height)
}

// tabBar renders the profile tab strip. The active tab is always visible;
// inactive tabs are dropped from either end when the bar is too narrow.
func (m *Model) tabBar() string {
	if !m.hasTabs() {
		return strings.Repeat(" ", max(0, m.width))
	}
	activeStyle := consolePalette.activeTab
	inactiveStyle := consolePalette.panel
	separator := consolePalette.panel.Render("│")

	type tab struct {
		label  string
		width  int
		render string
	}
	tabs := make([]tab, len(m.profiles))
	totalWidth := 0
	for i, profile := range m.profiles {
		label := " " + profile + " "
		loaded := i == m.active || (i < len(m.workspaces) && m.workspaces[i].sessionReady)
		if !loaded {
			label += "· "
		}
		var rendered string
		if i == m.active {
			rendered = activeStyle.Render(label)
		} else {
			rendered = inactiveStyle.Render(label)
		}
		w := lipgloss.Width(rendered)
		tabs[i] = tab{label: label, width: w, render: rendered}
		totalWidth += w
		if i > 0 {
			totalWidth += lipgloss.Width(separator)
		}
	}
	if totalWidth <= m.width {
		var parts []string
		for i, t := range tabs {
			if i > 0 {
				parts = append(parts, separator)
			}
			parts = append(parts, t.render)
		}
		return consolePalette.panel.Width(m.width).Render(strings.Join(parts, ""))
	}

	// Narrow terminal: keep the active tab and add neighbors while they fit.
	ellipsis := consolePalette.panel.Render("…")
	ellipsisWidth := lipgloss.Width(ellipsis)
	available := m.width - tabs[m.active].width
	if m.active > 0 {
		available -= ellipsisWidth
	}
	if m.active < len(tabs)-1 {
		available -= ellipsisWidth
	}

	left := m.active - 1
	right := m.active + 1
	for left >= 0 {
		need := tabs[left].width
		if left < m.active-1 || right < len(tabs) {
			need += lipgloss.Width(separator)
		}
		if available < need {
			break
		}
		available -= need
		left--
	}
	for right < len(tabs) {
		need := tabs[right].width
		if right > m.active+1 || left >= 0 {
			need += lipgloss.Width(separator)
		}
		if available < need {
			break
		}
		available -= need
		right++
	}

	var parts []string
	if left >= 0 {
		parts = append(parts, ellipsis)
	}
	for i := left + 1; i < right; i++ {
		if len(parts) > 0 {
			parts = append(parts, separator)
		}
		parts = append(parts, tabs[i].render)
	}
	if right < len(tabs) {
		parts = append(parts, ellipsis)
	}
	bar := strings.Join(parts, "")
	return consolePalette.panel.Width(m.width).Render(bar)
}

// scrollWindow clamps offset to the scrollable range and returns the visible
// slice of lines for a panel of the given capacity.
func scrollWindow(lines []string, offset, capacity int) []string {
	offset = min(offset, max(0, len(lines)-capacity))
	if offset > 0 {
		lines = lines[offset:]
	}
	if len(lines) > capacity {
		lines = lines[:capacity]
	}
	return lines
}

// helpContent returns the grouped key bindings as styled text lines (no header
// or footer) so it can be consumed by both the full-area panel and the popup.
// Every line is rendered through the supplied styles so all cells carry the
// caller's background instead of falling back to the terminal default.
func (m *Model) helpContent(accent, body lipgloss.Style) []string {
	ctx := keyContext{Screen: m.screen, Mode: m.recordMode, InputFocused: m.inputFocused(), DialogOpen: m.mappingView != nil, DialogFormFocused: m.mappingView != nil && m.mappingView.form != nil, Tabs: m.hasTabs()}
	groups := m.keys.fullHelp(ctx, m.overlay != nil)
	var lines []string
	for i, group := range groups {
		if i > 0 {
			lines = append(lines, "")
		}
		lines = append(lines, accent.Render(group.Name))
		for _, binding := range group.Bindings {
			help := binding.Help()
			lines = append(lines, body.Render(fmt.Sprintf("  %-11s %s", help.Key, help.Desc)))
		}
	}
	return lines
}

// helpPanel renders the grouped key map as a dedicated panel in the data area.
// This is the fallback used when the terminal is too small for a popup.
func (m *Model) helpPanel() string {
	lines := []string{consolePalette.panel.Bold(true).Width(m.width).Render("  HELP  press ? to close")}
	lines = append(lines, m.helpContent(consolePalette.cyan.Bold(true), consolePalette.plain)...)
	capacity := m.visible + 1
	lines = scrollWindow(lines, m.helpVertical, capacity)
	for len(lines) < capacity {
		lines = append(lines, "")
	}
	return strings.Join(lines, "\n")
}

// overlayHelp renders the grouped key map as a centered floating popup over the
// supplied background using the lipgloss v2 Canvas/Layer compositor.
func (m *Model) overlayHelp(background string) string {
	popupWidth := min(60, m.width-4)
	popupHeight := min(max(10, int(float64(m.height)*0.8)), m.height-2)
	if popupWidth < 24 || popupHeight < 8 {
		return background
	}

	innerWidth := popupWidth - 4 // border + 1 cell horizontal padding each side
	contentHeight := popupHeight - 4

	// Each line carries the popup background itself: a styled segment ends in
	// an ANSI reset, which would otherwise drop the box background for the
	// remainder of that row.
	fill := consolePalette.popup.Width(innerWidth)
	accent := consolePalette.cyan.Bold(true).Inherit(consolePalette.popup)
	body := consolePalette.popup

	contentLines := scrollWindow(m.helpContent(accent, body), m.helpVertical, contentHeight)

	title := accent.Render("HELP")
	innerLines := []string{
		fill.Render(strings.Repeat(" ", max(0, (innerWidth-lipgloss.Width(title))/2)) + title),
	}
	for _, line := range contentLines {
		innerLines = append(innerLines, fill.Render(truncateStyled(line, innerWidth)))
	}
	for len(innerLines) < contentHeight+1 {
		innerLines = append(innerLines, fill.Render(""))
	}
	footer := consolePalette.muted.Inherit(consolePalette.popup).Render("? or esc to close")
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

// mappingViewContent renders the combined copybook mapping screen: the list
// of persisted mappings matching the current data set (transparency for
// unintentional matches) or the inline add/edit form. Persistence happens in
// the background, so the footer advertises no save keys.
func (m *Model) mappingViewContent() string {
	view := m.mappingView
	title := consolePalette.navy.Bold(true).Width(m.width).Render(" CQT  COPYBOOK MAPPINGS ")
	subtitle := consolePalette.panel.Width(m.width).Render(" " + view.target + "  mappings are matched most precise first and saved automatically ")

	var lines []string
	var footer string
	if form := view.form; form != nil {
		for i, input := range form.inputs() {
			marker := "   "
			if i == form.focus {
				marker = " " + consolePalette.cyan.Bold(true).Render(">") + " "
			}
			lines = append(lines, marker+input.View())
		}
		footer = "Enter apply (empty copybook removes the mapping)  Tab next  Esc back"
	} else if len(view.entries) == 0 {
		lines = append(lines, "   "+consolePalette.muted.Render("no mappings match "+view.target))
		footer = "a add mapping  Esc close"
	} else {
		patternWidth := 0
		copybookWidth := 0
		for _, entry := range view.entries {
			patternWidth = max(patternWidth, len(entry.Mapping.Pattern))
			copybookWidth = max(copybookWidth, len(sourceDisplay(mappingSource(entry.Mapping))))
		}
		patternWidth = min(max(patternWidth, 7), 36)
		copybookWidth = min(max(copybookWidth, 8), 44)
		lines = append(lines, "   "+consolePalette.muted.Render(fmt.Sprintf("%-*s  %-*s  %s", patternWidth, "PATTERN", copybookWidth, "COPYBOOK", "RECORD")))
		for i, entry := range view.entries {
			marker := "   "
			if i == view.selected {
				marker = " " + consolePalette.cyan.Bold(true).Render(">") + " "
			}
			row := fmt.Sprintf("%-*s  %-*s  %s",
				patternWidth, truncateCell(entry.Mapping.Pattern, patternWidth),
				copybookWidth, truncateCell(sourceDisplay(mappingSource(entry.Mapping)), copybookWidth),
				entry.Mapping.Record)
			style := consolePalette.plain
			if i == view.selected {
				style = consolePalette.bright
			}
			rendered := style.Render(row)
			if entry.Applied {
				rendered += "  " + consolePalette.green.Render("applied")
			}
			lines = append(lines, marker+rendered)
		}
		footer = "Enter apply  a add  e edit  x remove  Esc close"
	}
	lines = append(lines, "")
	if view.note != "" {
		lines = append(lines, "   "+consolePalette.cyan.Render("MAPPING  "+view.note))
	}
	if view.err != "" {
		lines = append(lines, "   "+consolePalette.danger.Render("ERROR  "+view.err))
	} else {
		lines = append(lines, "   "+consolePalette.muted.Render(footer))
	}
	available := max(1, m.height-4)
	body := lipgloss.Place(m.width, available, lipgloss.Left, lipgloss.Center, strings.Join(lines, "\n"), lipgloss.WithWhitespaceChars(" "))
	statusLine := m.statusLine()
	helpLine := m.helpLine()
	return fitHeight(strings.Join([]string{title, subtitle, body, statusLine, helpLine}, "\n"), m.width, m.height)
}

// truncateCell clamps a plain (unstyled) cell to width with an ellipsis.
func truncateCell(value string, width int) string {
	if len(value) <= width {
		return value
	}
	if width <= 1 {
		return value[:width]
	}
	return value[:width-1] + "…"
}

func (m *Model) titleLine() string {
	var screenName, body, chip string
	switch m.screen {
	case ScreenDataSets:
		screenName = "DATASETS"
		body = fmt.Sprintf("prefix %s", displayOr(m.prefix, "—"))
	case ScreenMembers:
		screenName = m.dataSet.Name
		body = fmt.Sprintf("members  filter %s", displayOr(m.memberPattern, "*"))
	case ScreenRecords:
		screenName = m.dataSet.Name
		if m.member != nil {
			screenName += "(" + m.member.Name + ")"
		}
		body = m.modeName()
	}
	if profile := m.activeProfile(); profile != "" {
		chip = profile
	} else if m.user != "" {
		chip = m.user
	}

	accent := consolePalette.cyan.Bold(true).Inherit(consolePalette.navy)
	plain := consolePalette.navy.Foreground(lipgloss.Color("#CBD5E1"))
	chipStyle := consolePalette.navy.Foreground(lipgloss.Color("#94A3B8")).Faint(true)

	plainMiddle := "  " + body
	chipText := ""
	if chip != "" {
		chipText = " " + chip
	}
	baseWidth := 1 + lipgloss.Width(screenName) + lipgloss.Width(plainMiddle) + lipgloss.Width(chipText)
	gap := 0
	if baseWidth < m.width {
		gap = m.width - baseWidth
	}

	line := plain.Render(" ") + accent.Render(screenName) + plain.Render(plainMiddle)
	if gap > 0 {
		line += plain.Render(strings.Repeat(" ", gap))
	}
	if chipText != "" && lipgloss.Width(line)+lipgloss.Width(chipStyle.Render(chipText)) <= m.width {
		line += chipStyle.Render(chipText)
	}
	return truncateStyled(line, m.width)
}

func (m *Model) searchLine() string {
	label := consolePalette.panel.Foreground(lipgloss.Color("#64748B"))
	value := consolePalette.panel.Foreground(lipgloss.Color("#F8FAFC"))

	var line string
	switch m.screen {
	case ScreenDataSets:
		if m.prefixInput.Focused() {
			line = m.prefixInput.View()
		} else {
			line = label.Render("PREFIX") + "  " + value.Render(displayOr(m.prefix, "press / to enter a prefix"))
		}
	case ScreenMembers:
		if m.memberInput.Focused() {
			line = m.memberInput.View()
		} else {
			line = label.Render("DATA SET") + "  " + value.Render(m.dataSet.Name) +
				"    " + label.Render("MEMBER FILTER") + "  " + value.Render(displayOr(m.memberPattern, "*"))
		}
	case ScreenRecords:
		if m.locateInput.Focused() {
			line = m.locateInput.View()
			break
		}
		overlayText := "none"
		if m.overlay != nil {
			overlayText = m.overlay.Source.label()
			if m.overlay.Record != nil {
				overlayText += " / " + m.overlay.Record.Name
			}
		}
		line = label.Render("CODEPAGE") + "  " + value.Render(m.codepageName) +
			"  " + label.Render("COPYBOOK") + "  " + value.Render(overlayText)
	}
	return consolePalette.panel.Width(m.width).Render(truncateStyled(line, m.width))
}

func (m *Model) dataView() string {
	if m.activeRowCount() == 0 {
		level, text := m.effectiveStatus()
		return m.statePanel(level, text)
	}
	switch m.screen {
	case ScreenDataSets:
		return m.dataSetTableView()
	case ScreenMembers:
		return m.memberTableView()
	case ScreenRecords:
		switch m.recordMode {
		case ModeTable:
			return m.recordTableView()
		case ModeJSON:
			return m.recordJSONView()
		default:
			return m.rawRecordView()
		}
	default:
		return m.statePanel(statusError, "unknown screen")
	}
}

func (m *Model) activeRowCount() int {
	switch m.screen {
	case ScreenDataSets:
		return len(m.datasets)
	case ScreenMembers:
		return len(m.members)
	case ScreenRecords:
		return len(m.records)
	default:
		return 0
	}
}

func (m *Model) statePanel(level statusLevel, text string) string {
	header := consolePalette.header.Render("  STATE    DETAIL")
	// The status chip is fixed-width (7) so DETAIL stays column-aligned for
	// every status word length.
	bodyLine := "  " + m.renderStatusLabel(level) + "  " + text
	lines := []string{header, truncateStyled(bodyLine, m.width)}
	for len(lines) < m.visible+1 {
		lines = append(lines, "")
	}
	return strings.Join(lines, "\n")
}

var denseTableStyles = func() table.Styles {
	styles := table.DefaultStyles()
	styles.Header = consolePalette.header.Padding(0, 1)
	styles.Cell = lipgloss.NewStyle().Padding(0, 1)
	styles.Selected = consolePalette.selected
	return styles
}()

func (m *Model) dataSetTableView() string {
	showVolume := m.width >= 76
	showReferenced := m.width >= 92
	showFavorites := m.deps.Favorites != nil
	columnCount := 5
	fixedWidths := 1 + 6 + 5 + 6
	if showFavorites {
		columnCount++
		fixedWidths++
	}
	if showVolume {
		columnCount++
		fixedWidths += 8
	}
	if showReferenced {
		columnCount++
		fixedWidths += 10
	}
	nameWidth := max(18, m.width-fixedWidths-2*columnCount)
	columns := []table.Column{{Title: "", Width: 1}}
	if showFavorites {
		columns = append(columns, table.Column{Title: "", Width: 1})
	}
	columns = append(columns,
		table.Column{Title: "DATA SET NAME", Width: nameWidth},
		table.Column{Title: "DSORG", Width: 6},
		table.Column{Title: "RECFM", Width: 5},
		table.Column{Title: "LRECL", Width: 6},
	)
	if showVolume {
		columns = append(columns, table.Column{Title: "VOLUME", Width: 8})
	}
	if showReferenced {
		columns = append(columns, table.Column{Title: "REFERENCED", Width: 10})
	}
	start, end, showEnd := endMarkerWindow(&m.datasetPage)
	rows := make([]table.Row, end-start)
	selected := m.datasetPage.selectedIndex()
	for i, dataSet := range m.datasets[start:end] {
		marker := " "
		if start+i == selected {
			marker = ">"
		}
		row := table.Row{marker}
		if showFavorites {
			row = append(row, m.favoriteMarker(dataSet.Name))
		}
		row = append(row, dataSet.Name, dataSet.Organization, dataSet.RecordFormat, dataSet.RecordLength)
		if showVolume {
			row = append(row, displayOr(dataSet.Volume, dataSet.Volumes))
		}
		if showReferenced {
			row = append(row, dataSet.ReferenceDate)
		}
		rows[i] = row
	}
	return renderTable(m.width, m.visible, columns, rows, selected-start, showEnd)
}

func (m *Model) memberTableView() string {
	columns := []table.Column{
		{Title: "", Width: 1},
		{Title: "MEMBER", Width: 10},
		{Title: "VER", Width: 4},
		{Title: "MOD", Width: 4},
		{Title: "RECORDS", Width: 8},
	}
	showUser := m.width >= 66
	showDate := m.width >= 82
	if showUser {
		columns = append(columns, table.Column{Title: "USER", Width: 10})
	}
	if showDate {
		columns = append(columns, table.Column{Title: "MODIFIED", Width: 12})
	}
	start, end, showEnd := endMarkerWindow(&m.memberPage)
	rows := make([]table.Row, end-start)
	selected := m.memberPage.selectedIndex()
	for i, member := range m.members[start:end] {
		marker := " "
		if start+i == selected {
			marker = ">"
		}
		row := table.Row{marker, member.Name, strconv.Itoa(member.Version), strconv.Itoa(member.Modification), strconv.Itoa(member.CurrentRecords)}
		if showUser {
			row = append(row, member.User)
		}
		if showDate {
			row = append(row, member.ModifiedDate+" "+member.ModifiedTime)
		}
		rows[i] = row
	}
	return renderTable(m.width, m.visible, columns, rows, selected-start, showEnd)
}

func endMarkerWindow[A comparable](pager *pager[A]) (start, end int, showEnd bool) {
	start, end = pager.windowRange()
	// A trailing blank row marks the known end of data, but only when the
	// window is otherwise full — a shorter list already has blank space below.
	showEnd = pager.visible > 1 && pager.atEnd() && end-start >= pager.visible
	if showEnd {
		start++
	}
	return start, end, showEnd
}

func renderTable(width, visible int, columns []table.Column, rows []table.Row, cursorOffset int, showEnd bool) string {
	height := visible + 1
	if showEnd {
		height--
	}
	model := table.New(
		table.WithColumns(columns),
		table.WithRows(rows),
		table.WithFocused(true),
		table.WithWidth(width),
		table.WithHeight(height),
		table.WithStyles(denseTableStyles),
	)
	if cursorOffset >= 0 {
		model.SetCursor(cursorOffset)
	}
	view := model.View()
	if showEnd {
		view += "\n" + endMarker(width)
	}
	return view
}

func endMarker(width int) string {
	return strings.Repeat(" ", max(0, width))
}

func (m *Model) rawRecordView() string {
	numberWidth := m.recordNumberWidth()
	header := consolePalette.header.Render(fmt.Sprintf("  %-*s │ RAW DATA", numberWidth, "RECORD"))
	start, end, showEnd := endMarkerWindow(&m.recordPage)
	lines := make([]string, end-start)
	for i, row := range m.records[start:end] {
		lines[i] = decode.DisplayBytes(row.Record.Data, m.charmap)
	}
	selected := m.recordPage.selectedIndex()
	height := m.visible
	if showEnd {
		height--
	}
	model := viewport.New(viewport.WithWidth(m.width), viewport.WithHeight(height))
	model.SoftWrap = false
	model.FillHeight = true
	model.SetHorizontalStep(hScrollStep)
	model.SetContentLines(lines)
	model.SetXOffset(m.horizontal * hScrollStep)
	model.LeftGutterFunc = func(context viewport.GutterContext) string {
		absolute := start + context.Index
		if context.Index < 0 || absolute >= end {
			return strings.Repeat(" ", numberWidth+5)
		}
		marker := " "
		if absolute == selected {
			marker = ">"
		}
		return fmt.Sprintf("%s %0*d │ ", marker, numberWidth, m.records[absolute].Record.Number)
	}
	model.StyleLineFunc = func(index int) lipgloss.Style {
		if start+index == selected {
			return consolePalette.selected
		}
		return consolePalette.plain
	}
	view := header + "\n" + model.View()
	if showEnd {
		view += "\n" + endMarker(m.width)
	}
	return view
}

func (m *Model) recordTableView() string {
	if m.overlay == nil {
		return m.rawRecordView()
	}
	colStart := m.horizontal
	if colStart >= len(m.overlay.Columns) {
		colStart = max(0, len(m.overlay.Columns)-1)
	}
	numberWidth := m.recordNumberWidth()
	gutterColumnWidth := numberWidth + 5
	columns := []table.Column{{Title: fmt.Sprintf("  %-*s │", numberWidth, "RECORD"), Width: gutterColumnWidth}}
	used := gutterColumnWidth + 2 // fixed gutter content plus table cell padding
	visibleColumns := m.overlay.Columns[colStart:colStart]
	for _, column := range m.overlay.Columns[colStart:] {
		width := column.Width
		if used+width+2 > m.width && len(visibleColumns) > 0 {
			break
		}
		visibleColumns = append(visibleColumns, column)
		columns = append(columns, table.Column{Title: column.Path, Width: width})
		used += width + 2
	}
	if len(visibleColumns) == 0 && len(m.overlay.Columns) > 0 {
		column := m.overlay.Columns[colStart]
		visibleColumns = append(visibleColumns, column)
		columns = append(columns, table.Column{Title: column.Path, Width: max(8, m.width-used-2)})
	}

	start, end, showEnd := endMarkerWindow(&m.recordPage)
	selected := m.recordPage.selectedIndex()
	rows := make([]table.Row, end-start)
	for i, row := range m.records[start:end] {
		marker := " "
		if start+i == selected {
			marker = ">"
		}
		tableRow := table.Row{fmt.Sprintf("%s %0*d │", marker, numberWidth, row.Record.Number)}
		if row.Err != nil || row.Decoded == nil {
			raw := decode.DisplayBytes(row.Record.Data, m.charmap)
			if row.Err != nil {
				raw = "! " + raw
			}
			tableRow = append(tableRow, raw)
			for len(tableRow) < len(columns) {
				tableRow = append(tableRow, "")
			}
			rows[i] = tableRow
			continue
		}
		for _, column := range visibleColumns {
			value, ok := valueAtPath(row.Decoded.Value, column.Parts)
			cell := ""
			if ok {
				cell = compactValue(value)
			}
			if _, bad := diagnosticForColumn(row.Decoded.Diagnostics, column.Path); bad {
				cell = "! " + cell
			}
			tableRow = append(tableRow, cell)
		}
		rows[i] = tableRow
	}
	return renderTable(m.width, m.visible, columns, rows, selected-start, showEnd)
}

func (m *Model) recordJSONView() string {
	selected := m.recordPage.selectedIndex()
	if selected < 0 || selected >= len(m.records) {
		return m.statePanel(statusEmpty, "no selected record")
	}
	row := m.records[selected]
	content, lineCount, _, _ := m.selectedJSONContent()
	numberWidth := m.recordNumberWidth()
	title := fmt.Sprintf("  %-*s │ PRETTY JSON", numberWidth, "RECORD")
	hint := "  j/k record  pgup/pgdn scroll"
	header := truncateStyled(consolePalette.header.Render(title)+consolePalette.panel.Faint(true).Render(hint), m.width)
	model := viewport.New(viewport.WithWidth(m.width), viewport.WithHeight(m.visible))
	model.SoftWrap = false
	model.FillHeight = true
	model.SetHorizontalStep(hScrollStep)
	model.SetContent(content)
	model.SetXOffset(m.horizontal * hScrollStep)
	maxVertical := max(0, lineCount-m.visible)
	model.SetYOffset(min(m.jsonVertical, maxVertical))
	model.LeftGutterFunc = func(context viewport.GutterContext) string {
		if context.Index == 0 {
			return fmt.Sprintf("> %0*d │ ", numberWidth, row.Record.Number)
		}
		return strings.Repeat(" ", numberWidth+3) + "│ "
	}
	return header + "\n" + model.View()
}

func (m *Model) recordJSONContent(row recordRow) string {
	content := decode.DisplayBytes(row.Record.Data, m.charmap)
	if row.Err != nil {
		return "STRUCTURAL DECODE ERROR: " + row.Err.Error() + "\nRAW FALLBACK:\n" + content
	}
	if row.Decoded != nil {
		return prettyRecordJSON(*row.Decoded)
	}
	return content
}

// jsonContentCache memoizes the selected record's pretty JSON so Update
// (scroll clamping) and View do not each re-marshal the same record. The key
// covers everything recordJSONContent reads: the raw bytes (by slice
// identity — fetches always allocate fresh slices), decode outcome, and
// charmap.
type jsonContentCache struct {
	valid   bool
	data    []byte
	decoded *record.DecodedRecord
	err     error
	charmap *decode.Charmap
	content string
	lines   int
	longest int
}

func sameSlice(a, b []byte) bool {
	return len(a) == len(b) && (len(a) == 0 || &a[0] == &b[0])
}

// selectedJSONContent returns the selected record's pretty JSON along with its
// line count and widest line width. ok is false when no record is selected.
func (m *Model) selectedJSONContent() (content string, lines, longest int, ok bool) {
	selected := m.recordPage.selectedIndex()
	if selected < 0 || selected >= len(m.records) {
		return "", 0, 0, false
	}
	row := m.records[selected]
	cache := &m.jsonCache
	if cache.valid && sameSlice(cache.data, row.Record.Data) && cache.decoded == row.Decoded && cache.err == row.Err && cache.charmap == m.charmap {
		return cache.content, cache.lines, cache.longest, true
	}
	content = m.recordJSONContent(row)
	for _, line := range strings.Split(content, "\n") {
		lines++
		longest = max(longest, lipgloss.Width(line))
	}
	*cache = jsonContentCache{
		valid: true, data: row.Record.Data, decoded: row.Decoded, err: row.Err, charmap: m.charmap,
		content: content, lines: lines, longest: longest,
	}
	return content, lines, longest, true
}

func (m *Model) maxJSONVertical() int {
	_, lines, _, ok := m.selectedJSONContent()
	if !ok {
		return 0
	}
	return max(0, lines-m.visible)
}

func (m *Model) statusLine() string {
	level, text := m.effectiveStatus()
	if level != statusLoading && level != statusError {
		if detail := m.diagnosticDetail(); detail != "" {
			level = statusWarn
			text = detail
		}
	}
	indicator := " "
	if level == statusLoading && len(m.spinner.Spinner.Frames) > 0 {
		indicator = consolePalette.amber.Render(m.spinner.View())
	}
	label := m.renderStatusLabel(level)
	position := m.windowStatus()

	const minWidthForPosition = 40
	if m.width < minWidthForPosition || position == "" {
		line := fmt.Sprintf(" %s %s  %s", indicator, label, text)
		return truncateStyled(line, m.width)
	}

	prefix := fmt.Sprintf(" %s %s  ", indicator, label)
	prefixWidth := lipgloss.Width(prefix)
	posWidth := lipgloss.Width(position)
	maxTextWidth := m.width - prefixWidth - posWidth
	if maxTextWidth < 10 {
		line := fmt.Sprintf(" %s %s  %s", indicator, label, text)
		return truncateStyled(line, m.width)
	}

	truncatedText := truncateStyled(text, maxTextWidth)
	used := prefixWidth + lipgloss.Width(truncatedText) + posWidth
	padding := m.width - used
	if padding < 1 {
		padding = 1
		truncatedText = truncateStyled(text, maxTextWidth-1)
		used = prefixWidth + lipgloss.Width(truncatedText) + posWidth
		padding = m.width - used
	}

	return prefix + truncatedText + strings.Repeat(" ", padding) + position
}

func (m *Model) effectiveStatus() (statusLevel, string) {
	if m.overlayPending {
		return statusLoading, "loading copybook overlay"
	}
	if m.browsePending != nil {
		return statusLoading, m.status.Text
	}
	if m.decodePending {
		return statusLoading, "decoding records"
	}
	if m.status.Level == statusError {
		return m.status.Level, m.status.Text
	}
	if m.overlayError != "" {
		return statusError, m.overlayError
	}
	return m.status.Level, m.status.Text
}

func (m *Model) renderStatusLabel(level statusLevel) string {
	label := string(level)
	chip := fmt.Sprintf("%-*s", 7, label)
	switch level {
	case statusReady:
		return consolePalette.green.Background(lipgloss.Color("#064E3B")).Bold(true).Render(chip)
	case statusWarn, statusLoading:
		return consolePalette.amber.Background(lipgloss.Color("#78350F")).Bold(true).Render(chip)
	case statusError:
		return consolePalette.danger.Background(lipgloss.Color("#7F1D1D")).Bold(true).Render(chip)
	case statusEmpty:
		return consolePalette.cyan.Background(lipgloss.Color("#164E63")).Bold(true).Render(chip)
	default:
		return chip
	}
}

func (m *Model) diagnosticDetail() string {
	if !m.showDiagnostics || m.screen != ScreenRecords {
		return ""
	}
	selected := m.recordPage.selectedIndex()
	if selected < 0 || selected >= len(m.records) {
		return "no selected record diagnostic"
	}
	row := m.records[selected]
	numberWidth := m.recordNumberWidth()
	if row.Err != nil {
		return fmt.Sprintf("record %0*d: %v", numberWidth, row.Record.Number, row.Err)
	}
	if row.Decoded == nil || len(row.Decoded.Diagnostics) == 0 {
		return fmt.Sprintf("record %0*d: no diagnostics", numberWidth, row.Record.Number)
	}
	diagnostic := selectedDiagnostic(row.Decoded.Diagnostics, m.overlay, m.horizontal)
	return fmt.Sprintf("record %0*d field %s offset %d length %d raw %s: %v", numberWidth, row.Record.Number, diagnostic.FieldPath, diagnostic.Offset, diagnostic.Length, diagnostic.RawHex, diagnostic.Err)
}

func selectedDiagnostic(diagnostics []record.Diagnostic, overlay *overlay, horizontal int) record.Diagnostic {
	if overlay != nil && horizontal >= 0 && horizontal < len(overlay.Columns) {
		if diagnostic, ok := diagnosticForColumn(diagnostics, overlay.Columns[horizontal].Path); ok {
			return diagnostic
		}
	}
	return diagnostics[0]
}

func (m *Model) windowStatus() string {
	switch m.screen {
	case ScreenDataSets:
		if len(m.datasets) == 0 {
			return ""
		}
		count := len(m.datasets)
		if m.datasetTotal != nil {
			count = *m.datasetTotal
		}
		return positionStatus("row", m.datasetPage.selectedIndex()+1, count, m.datasetPage.more)
	case ScreenMembers:
		if len(m.members) == 0 {
			return ""
		}
		count := len(m.members)
		if m.memberTotal != nil {
			count = *m.memberTotal
		}
		return positionStatus("row", m.memberPage.selectedIndex()+1, count, m.memberPage.more)
	case ScreenRecords:
		if len(m.records) == 0 {
			return ""
		}
		selected := m.recordPage.selectedIndex()
		var number int64
		if selected >= 0 && selected < len(m.records) {
			number = m.records[selected].Record.Number
		}
		return recordPositionStatus(number, len(m.records), m.recordPage.more, m.recordNumberWidth())
	default:
		return ""
	}
}

func positionStatus(label string, selected, count int, more bool) string {
	suffix := ""
	if more {
		suffix = "+"
	}
	return fmt.Sprintf("%s %d of %d%s", label, selected, count, suffix)
}

func recordPositionStatus(number int64, count int, more bool, numberWidth int) string {
	suffix := ""
	if more {
		suffix = "+"
	}
	return fmt.Sprintf("record %0*d of %d%s", numberWidth, number, count, suffix)
}

func (m *Model) helpLine() string {
	ctx := keyContext{
		Screen: m.screen, Mode: m.recordMode, InputFocused: m.inputFocused(),
		DialogOpen: m.mappingView != nil, DialogFormFocused: m.mappingView != nil && m.mappingView.form != nil,
		ShowHelp: m.showHelp, Tabs: m.hasTabs(),
		FavoritesOpen: m.favPopup != nil, FavoritesInput: m.favPopup != nil && m.favPopup.editing,
		EditorOpen: m.editor != nil, EditorConfirm: m.editor != nil && m.editor.confirmDiscard,
		QueryOpen: m.query != nil,
	}
	line := m.help.ShortHelpView(m.keys.shortHelp(ctx, m.overlay != nil))
	return consolePalette.muted.Width(m.width).Render(truncateStyled(" "+line, m.width))
}

func (m *Model) modeName() string {
	switch m.recordMode {
	case ModeTable:
		return "COPYBOOK TABLE"
	case ModeJSON:
		return "PRETTY JSON"
	default:
		return "RAW"
	}
}

func (m *Model) maxHorizontal() int {
	if m.screen != ScreenRecords {
		return 0
	}
	if m.recordMode == ModeTable && m.overlay != nil {
		return max(0, len(m.overlay.Columns)-1)
	}
	available := max(1, m.width-(m.recordNumberWidth()+5))
	longest := 0
	if m.recordMode == ModeJSON {
		_, _, longest, _ = m.selectedJSONContent()
	} else {
		longest = m.rawLongest
	}
	if longest <= available {
		return 0
	}
	return (longest - available + hScrollStep - 1) / hScrollStep
}

func longestRawDisplayWidth(records []recordRow, charmap *decode.Charmap) int {
	longest := 0
	for _, row := range records {
		longest = max(longest, lipgloss.Width(decode.DisplayBytes(row.Record.Data, charmap)))
	}
	return longest
}

func (m *Model) recordNumberWidth() int {
	// Records are cached in ascending number order (forward pages append past
	// the last cached record; anchored fetches replace the cache), so the last
	// row always carries the widest number.
	width := 8
	if len(m.records) > 0 {
		if digits := len(strconv.FormatInt(m.records[len(m.records)-1].Record.Number, 10)); digits > width {
			width = digits
		}
	}
	return width
}

func displayOr(value, fallback string) string {
	if strings.TrimSpace(value) == "" {
		return fallback
	}
	return value
}

func truncateStyled(value string, width int) string {
	if width <= 0 {
		return ""
	}
	if lipgloss.Width(value) <= width {
		return value
	}
	return ansi.Truncate(value, width, "…")
}

func fitHeight(content string, width, height int) string {
	if height <= 0 {
		return ""
	}
	lines := strings.Split(content, "\n")
	if len(lines) > height {
		lines = lines[:height]
	}
	for len(lines) < height {
		lines = append(lines, "")
	}
	for i, line := range lines {
		if width <= 0 {
			continue
		}
		lineWidth := lipgloss.Width(line)
		if lineWidth > width {
			line = ansi.Truncate(line, width, "…")
			lineWidth = lipgloss.Width(line)
		}
		if lineWidth < width {
			line += strings.Repeat(" ", width-lineWidth)
		}
		lines[i] = line
	}
	return strings.Join(lines, "\n")
}
