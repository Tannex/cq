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
	case m.visible <= 0 && !m.showHelp:
		content = m.tinyView()
	case m.dialog != nil:
		content = m.dialogView()
	default:
		content = m.mainView()
	}
	if m.showHelp && m.width >= MinTerminalWidth && m.visible > 0 && m.dialog == nil {
		content = m.overlayHelp(content)
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

// helpContent returns the grouped key bindings as styled text lines (no header or
// footer) so it can be consumed by both the full-area panel and the popup.
func (m *Model) helpContent() []string {
	ctx := keyContext{Screen: m.screen, Mode: m.recordMode, InputFocused: m.inputFocused(), DialogOpen: m.dialog != nil, Tabs: m.hasTabs()}
	groups := m.keys.fullHelp(ctx, m.overlay != nil)
	var lines []string
	for i, group := range groups {
		if i > 0 {
			lines = append(lines, "")
		}
		lines = append(lines, consolePalette.cyan.Bold(true).Render(group.Name))
		for _, binding := range group.Bindings {
			help := binding.Help()
			lines = append(lines, fmt.Sprintf("  %-11s %s", help.Key, help.Desc))
		}
	}
	return lines
}

// helpPanel renders the grouped key map as a dedicated panel in the data area.
// This is the fallback used when the terminal is too small for a popup.
func (m *Model) helpPanel() string {
	lines := []string{consolePalette.panel.Bold(true).Width(m.width).Render("  HELP  press ? to close")}
	lines = append(lines, m.helpContent()...)
	capacity := m.visible + 1
	maxOffset := max(0, len(lines)-capacity)
	offset := min(m.helpVertical, maxOffset)
	if offset > 0 {
		lines = lines[offset:]
	}
	if len(lines) > capacity {
		lines = lines[:capacity]
	}
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

	contentLines := m.helpContent()
	maxOffset := max(0, len(contentLines)-contentHeight)
	offset := min(m.helpVertical, maxOffset)
	if offset > 0 {
		contentLines = contentLines[offset:]
	}
	if len(contentLines) > contentHeight {
		contentLines = contentLines[:contentHeight]
	}

	innerLines := []string{
		centerOrPad(consolePalette.cyan.Bold(true).Render("HELP"), innerWidth),
	}
	for _, line := range contentLines {
		innerLines = append(innerLines, truncateStyled(line, innerWidth))
	}
	for len(innerLines) < contentHeight+1 {
		innerLines = append(innerLines, strings.Repeat(" ", innerWidth))
	}
	innerLines = append(innerLines, centerOrPad(consolePalette.muted.Render("? or esc to close"), innerWidth))

	popupStyle := consolePalette.popup.
		Border(lipgloss.RoundedBorder()).
		BorderForeground(consolePalette.popupBorder.GetForeground()).
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

func centerOrPad(s string, width int) string {
	w := lipgloss.Width(s)
	if w >= width {
		return truncateStyled(s, width)
	}
	pad := width - w
	left := pad / 2
	right := pad - left
	return strings.Repeat(" ", left) + s + strings.Repeat(" ", right)
}

func (m *Model) dialogView() string {
	title := consolePalette.navy.Bold(true).Width(m.width).Render(" CQT  COPYBOOK OVERLAY ")
	subtitle := consolePalette.panel.Width(m.width).Render(" READ-ONLY DISPLAY DEFINITION  choose local file or DSN; COPY uses cq/config.json search order ")
	fields := []string{
		m.dialog.local.View(),
		m.dialog.dsn.View(),
		m.dialog.format.View(),
		m.dialog.record.View(),
	}
	lines := make([]string, 0, len(fields)+2)
	for i, field := range fields {
		marker := "   "
		if i == m.dialog.focus {
			marker = " " + consolePalette.cyan.Bold(true).Render(">") + " "
		}
		lines = append(lines, marker+field)
	}
	lines = append(lines, "")
	if m.dialog.err != "" {
		lines = append(lines, "   "+consolePalette.danger.Render("ERROR  "+m.dialog.err))
	} else {
		lines = append(lines, "   "+consolePalette.muted.Render("Enter apply (empty clears overlay)  Tab next  Esc cancel  errors keep prior overlay"))
	}
	available := max(1, m.height-4)
	body := lipgloss.Place(m.width, available, lipgloss.Left, lipgloss.Center, strings.Join(lines, "\n"), lipgloss.WithWhitespaceChars(" "))
	statusLine := m.statusLine()
	helpLine := m.helpLine()
	return fitHeight(strings.Join([]string{title, subtitle, body, statusLine, helpLine}, "\n"), m.width, m.height)
}

func (m *Model) titleLine() string {
	var screenName, body, chip string
	switch m.screen {
	case ScreenDataSets:
		screenName = "DATASETS"
		rangeText := nameRange(m.datasets, func(index int) string { return m.datasets[index].Name })
		body = fmt.Sprintf("prefix %-24s  range %s", displayOr(m.prefix, "—"), rangeText)
	case ScreenMembers:
		screenName = "MEMBERS"
		rangeText := nameRange(m.members, func(index int) string { return m.members[index].Name })
		body = fmt.Sprintf("%s  pattern %-8s  range %s", m.dataSet.Name, displayOr(m.memberPattern, "*"), rangeText)
	case ScreenRecords:
		screenName = m.dataSet.Name
		if m.member != nil {
			screenName += "(" + m.member.Name + ")"
		}
		first, last := m.recordRange()
		body = fmt.Sprintf("records %s / cached %d  %s", formatRange(first, last), len(m.records), m.modeName())
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

func denseTableStyles() table.Styles {
	styles := table.DefaultStyles()
	styles.Header = consolePalette.header.Padding(0, 1)
	styles.Cell = lipgloss.NewStyle().Padding(0, 1)
	styles.Selected = consolePalette.selected
	return styles
}

func (m *Model) dataSetTableView() string {
	showVolume := m.width >= 76
	showReferenced := m.width >= 92
	columnCount := 5
	fixedWidths := 1 + 6 + 5 + 6
	if showVolume {
		columnCount++
		fixedWidths += 8
	}
	if showReferenced {
		columnCount++
		fixedWidths += 10
	}
	nameWidth := max(18, m.width-fixedWidths-2*columnCount)
	columns := []table.Column{
		{Title: "", Width: 1},
		{Title: "DATA SET NAME", Width: nameWidth},
		{Title: "DSORG", Width: 6},
		{Title: "RECFM", Width: 5},
		{Title: "LRECL", Width: 6},
	}
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
		row := table.Row{marker, dataSet.Name, dataSet.Organization, dataSet.RecordFormat, dataSet.RecordLength}
		if showVolume {
			row = append(row, firstNonEmpty(dataSet.Volume, dataSet.Volumes))
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
		table.WithStyles(denseTableStyles()),
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
	content := m.recordJSONContent(row)
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
	model.SetYOffset(min(m.jsonVertical, m.maxJSONVertical()))
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

func (m *Model) maxJSONVertical() int {
	selected := m.recordPage.selectedIndex()
	if selected < 0 || selected >= len(m.records) {
		return 0
	}
	return max(0, len(strings.Split(m.recordJSONContent(m.records[selected]), "\n"))-m.visible)
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
	window := m.windowStatus()
	line := fmt.Sprintf(" %s %s  %s%s", indicator, m.renderStatusLabel(level), text, window)
	return truncateStyled(line, m.width)
}

func (m *Model) effectiveStatus() (statusLevel, string) {
	if m.overlayPending {
		return statusLoading, "loading copybook overlay"
	}
	if m.browsePending != nil {
		return statusLoading, m.status.Text
	}
	if m.decodePending {
		return statusLoading, "decoding cached records"
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
		total := ""
		if m.datasetTotal != nil {
			total = fmt.Sprintf(" / total %d", *m.datasetTotal)
		}
		return fmt.Sprintf("    cached %s%s / fetch %d", windowRowRange(len(m.datasets)), total, m.budget)
	case ScreenMembers:
		total := ""
		if m.memberTotal != nil {
			total = fmt.Sprintf(" / total %d", *m.memberTotal)
		}
		return fmt.Sprintf("    cached %s%s / fetch %d", windowRowRange(len(m.members)), total, m.budget)
	case ScreenRecords:
		first, last := m.recordRange()
		return fmt.Sprintf("    cached %d (%s) / fetch %d", len(m.records), formatRange(first, last), m.budget)
	default:
		return ""
	}
}

func (m *Model) helpLine() string {
	ctx := keyContext{Screen: m.screen, Mode: m.recordMode, InputFocused: m.inputFocused(), DialogOpen: m.dialog != nil, ShowHelp: m.showHelp, Tabs: m.hasTabs()}
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
		selected := m.recordPage.selectedIndex()
		if selected >= 0 && selected < len(m.records) {
			content := m.recordJSONContent(m.records[selected])
			for _, line := range strings.Split(content, "\n") {
				longest = max(longest, lipgloss.Width(line))
			}
		}
	} else {
		for _, row := range m.records {
			longest = max(longest, lipgloss.Width(decode.DisplayBytes(row.Record.Data, m.charmap)))
		}
	}
	if longest <= available {
		return 0
	}
	return (longest - available + hScrollStep - 1) / hScrollStep
}

func (m *Model) recordNumberWidth() int {
	width := 8
	for _, row := range m.records {
		if digits := len(strconv.FormatInt(row.Record.Number, 10)); digits > width {
			width = digits
		}
	}
	return width
}

func (m *Model) recordRange() (int64, int64) {
	if len(m.records) == 0 {
		return 0, 0
	}
	return m.records[0].Record.Number, m.records[len(m.records)-1].Record.Number
}

func nameRange[T any](items []T, name func(int) string) string {
	if len(items) == 0 {
		return "—"
	}
	return name(0) + "–" + name(len(items)-1)
}

func windowRowRange(count int) string {
	if count == 0 {
		return "0"
	}
	return fmt.Sprintf("1–%d", count)
}

func formatRange(first, last int64) string {
	if first == 0 && last == 0 {
		return "—"
	}
	return fmt.Sprintf("%d–%d", first, last)
}

func displayOr(value, fallback string) string {
	if strings.TrimSpace(value) == "" {
		return fallback
	}
	return value
}

func truncatePlain(value string, width int) string {
	if width <= 0 {
		return ""
	}
	runes := []rune(value)
	if len(runes) <= width {
		return value
	}
	if width == 1 {
		return "…"
	}
	return string(runes[:width-1]) + "…"
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
