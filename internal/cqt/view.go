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

var consolePalette = struct {
	navy, panel, cyan, green, amber, danger, muted lipgloss.Style
}{
	navy:   lipgloss.NewStyle().Background(lipgloss.Color("#071827")).Foreground(lipgloss.Color("#E7F3F5")),
	panel:  lipgloss.NewStyle().Background(lipgloss.Color("#12344D")).Foreground(lipgloss.Color("#E7F3F5")),
	cyan:   lipgloss.NewStyle().Foreground(lipgloss.Color("#6FD7E5")),
	green:  lipgloss.NewStyle().Foreground(lipgloss.Color("#89D185")),
	amber:  lipgloss.NewStyle().Foreground(lipgloss.Color("#E8BD68")),
	danger: lipgloss.NewStyle().Foreground(lipgloss.Color("#FF7B72")),
	muted:  lipgloss.NewStyle().Faint(true),
}

func (m *Model) View() tea.View {
	var content string
	switch {
	case m.visible <= 0:
		content = m.tinyView()
	case m.dialog != nil:
		content = m.dialogView()
	default:
		content = m.mainView()
	}
	view := tea.NewView(content)
	view.AltScreen = true
	view.WindowTitle = "cqt — z/OSMF data sets"
	return view
}

func (m *Model) tinyView() string {
	width := max(1, m.width)
	height := max(1, m.height)
	title := consolePalette.navy.Bold(true).Width(width).Render(" CQT  z/OSMF DATA SET CONSOLE ")
	message := fmt.Sprintf("TERMINAL TOO SMALL\nresize to at least %d columns × %d rows\ncurrent %d × %d\nno row request dispatched", MinTerminalWidth, MinTerminalHeight, m.width, m.height)
	body := lipgloss.Place(width, max(1, height-1), lipgloss.Center, lipgloss.Center, consolePalette.amber.Render(message))
	return fitHeight(title+"\n"+body, width, height)
}

func (m *Model) mainView() string {
	title := m.titleLine()
	search := m.searchLine()
	data := m.dataView()
	if m.showHelp {
		// Full help replaces the data area so every binding stays readable;
		// the chrome row count (and therefore the row budget) is unchanged.
		data = m.helpPanel()
	}
	statusLine := m.statusLine()
	helpLine := m.helpLine()
	return fitHeight(strings.Join([]string{title, search, data, statusLine, helpLine}, "\n"), m.width, m.height)
}

// helpPanel renders the grouped key map as a dedicated panel in the data
// area, since a single flattened line cannot hold the full binding set.
func (m *Model) helpPanel() string {
	ctx := keyContext{Screen: m.screen, Mode: m.recordMode, InputFocused: m.inputFocused(), DialogOpen: m.dialog != nil}
	groups := m.keys.fullHelp(ctx, m.overlay != nil)
	names := []string{"NAVIGATION", "ACTIONS", "GENERAL"}
	lines := []string{consolePalette.panel.Bold(true).Width(m.width).Render("  HELP  press ? to close")}
	for i, group := range groups {
		name := "KEYS"
		if i < len(names) {
			name = names[i]
		}
		if i > 0 {
			lines = append(lines, "")
		}
		lines = append(lines, "  "+consolePalette.cyan.Bold(true).Render(name))
		for _, binding := range group {
			help := binding.Help()
			lines = append(lines, truncateStyled(fmt.Sprintf("    %-11s %s", help.Key, help.Desc), m.width))
		}
	}
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
		lines = append(lines, "   "+consolePalette.muted.Render("Enter apply  Tab next field  Esc cancel  previous valid overlay is retained on failure"))
	}
	available := max(1, m.height-4)
	body := lipgloss.Place(m.width, available, lipgloss.Left, lipgloss.Center, strings.Join(lines, "\n"), lipgloss.WithWhitespaceChars(" "))
	statusLine := m.statusLine()
	helpLine := m.helpLine()
	return fitHeight(strings.Join([]string{title, subtitle, body, statusLine, helpLine}, "\n"), m.width, m.height)
}

func (m *Model) titleLine() string {
	var title string
	switch m.screen {
	case ScreenDataSets:
		rangeText := nameRange(m.datasets, func(index int) string { return m.datasets[index].Name })
		title = fmt.Sprintf(" DATASETS  prefix %-24s  range %s", displayOr(m.prefix, "—"), rangeText)
	case ScreenMembers:
		rangeText := nameRange(m.members, func(index int) string { return m.members[index].Name })
		title = fmt.Sprintf(" %s  MEMBERS  pattern %-8s  range %s", m.dataSet.Name, displayOr(m.memberPattern, "*"), rangeText)
	case ScreenRecords:
		target := m.dataSet.Name
		if m.member != nil {
			target += "(" + m.member.Name + ")"
		}
		first, last := m.recordRange()
		title = fmt.Sprintf(" %s  records %s / cached %d  %s", target, formatRange(first, last), len(m.records), m.modeName())
	}
	return consolePalette.navy.Bold(true).Width(m.width).Render(truncatePlain(title, m.width))
}

func (m *Model) searchLine() string {
	var line string
	switch m.screen {
	case ScreenDataSets:
		if m.prefixInput.Focused() {
			line = m.prefixInput.View()
		} else {
			line = " PREFIX  " + displayOr(m.prefix, "press / to enter a prefix")
		}
	case ScreenMembers:
		if m.memberInput.Focused() {
			line = m.memberInput.View()
		} else {
			line = fmt.Sprintf(" DATA SET  %s    MEMBER FILTER  %s", m.dataSet.Name, displayOr(m.memberPattern, "*"))
		}
	case ScreenRecords:
		overlayText := "none"
		if m.overlay != nil {
			overlayText = m.overlay.Source.label()
			if m.overlay.Record != nil {
				overlayText += " / " + m.overlay.Record.Name
			}
		}
		line = fmt.Sprintf(" CODEPAGE  %-8s  COPYBOOK  %s", m.codepageName, overlayText)
	}
	return consolePalette.panel.Width(m.width).Render(truncatePlain(line, m.width))
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
	header := consolePalette.panel.Bold(true).Render("  STATE    DETAIL")
	// Pad the plain label before styling so DETAIL stays column-aligned for
	// every status word length.
	padding := strings.Repeat(" ", max(1, 9-len(string(level))))
	bodyLine := "  " + m.renderStatusLabel(level) + padding + text
	lines := []string{header, truncateStyled(bodyLine, m.width)}
	for len(lines) < m.visible+1 {
		lines = append(lines, "")
	}
	return strings.Join(lines, "\n")
}

func denseTableStyles() table.Styles {
	styles := table.DefaultStyles()
	styles.Header = consolePalette.panel.Bold(true).Padding(0, 1)
	styles.Cell = lipgloss.NewStyle().Padding(0, 1)
	styles.Selected = lipgloss.NewStyle().Background(lipgloss.Color("#12344D")).Foreground(lipgloss.Color("#6FD7E5")).Bold(true)
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
	rows := make([]table.Row, len(m.datasets))
	selected := m.datasetPage.selectedIndex()
	for i, dataSet := range m.datasets {
		marker := " "
		if i == selected {
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
	return renderTable(m.width, m.visible, columns, rows, selected)
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
	rows := make([]table.Row, len(m.members))
	selected := m.memberPage.selectedIndex()
	for i, member := range m.members {
		marker := " "
		if i == selected {
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
	return renderTable(m.width, m.visible, columns, rows, selected)
}

func renderTable(width, visible int, columns []table.Column, rows []table.Row, selected int) string {
	model := table.New(
		table.WithColumns(columns),
		table.WithRows(rows),
		table.WithFocused(true),
		table.WithWidth(width),
		table.WithHeight(visible+1),
		table.WithStyles(denseTableStyles()),
	)
	if selected > 0 {
		// MoveDown updates both the cursor and viewport offset. SetCursor alone can
		// leave a deep selection one row below the visible table viewport.
		model.MoveDown(selected)
	}
	return model.View()
}

func (m *Model) rawRecordView() string {
	numberWidth := m.recordNumberWidth()
	header := consolePalette.panel.Bold(true).Render(fmt.Sprintf("  %-*s │ RAW DATA", numberWidth, "RECORD"))
	lines := make([]string, len(m.records))
	for i, row := range m.records {
		lines[i] = decode.DisplayBytes(row.Record.Data, m.charmap)
	}
	selected := m.recordPage.selectedIndex()
	model := viewport.New(viewport.WithWidth(m.width), viewport.WithHeight(m.visible))
	model.SoftWrap = false
	model.FillHeight = true
	model.SetHorizontalStep(8)
	model.SetContentLines(lines)
	model.SetXOffset(m.horizontal * 8)
	model.LeftGutterFunc = func(context viewport.GutterContext) string {
		if context.Index < 0 || context.Index >= len(m.records) {
			return strings.Repeat(" ", numberWidth+5)
		}
		marker := " "
		if context.Index == selected {
			marker = ">"
		}
		return fmt.Sprintf("%s %0*d │ ", marker, numberWidth, m.records[context.Index].Record.Number)
	}
	model.StyleLineFunc = func(index int) lipgloss.Style {
		if index == selected {
			return lipgloss.NewStyle().Background(lipgloss.Color("#12344D")).Foreground(lipgloss.Color("#6FD7E5")).Bold(true)
		}
		return lipgloss.NewStyle()
	}
	ensureViewportSelection(&model, selected)
	return header + "\n" + model.View()
}

func (m *Model) recordTableView() string {
	if m.overlay == nil {
		return m.rawRecordView()
	}
	start := m.horizontal
	if start >= len(m.overlay.Columns) {
		start = max(0, len(m.overlay.Columns)-1)
	}
	numberWidth := m.recordNumberWidth()
	gutterColumnWidth := numberWidth + 5
	columns := []table.Column{{Title: fmt.Sprintf("  %-*s │", numberWidth, "RECORD"), Width: gutterColumnWidth}}
	used := gutterColumnWidth + 2 // fixed gutter content plus table cell padding
	visibleColumns := m.overlay.Columns[start:start]
	for _, column := range m.overlay.Columns[start:] {
		width := column.Width
		if used+width+2 > m.width && len(visibleColumns) > 0 {
			break
		}
		visibleColumns = append(visibleColumns, column)
		columns = append(columns, table.Column{Title: column.Path, Width: width})
		used += width + 2
	}
	if len(visibleColumns) == 0 && len(m.overlay.Columns) > 0 {
		column := m.overlay.Columns[start]
		visibleColumns = append(visibleColumns, column)
		columns = append(columns, table.Column{Title: column.Path, Width: max(8, m.width-used-2)})
	}

	selected := m.recordPage.selectedIndex()
	rows := make([]table.Row, len(m.records))
	for i, row := range m.records {
		marker := " "
		if i == selected {
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
	return renderTable(m.width, m.visible, columns, rows, selected)
}

func (m *Model) recordJSONView() string {
	selected := m.recordPage.selectedIndex()
	if selected < 0 || selected >= len(m.records) {
		return m.statePanel(statusEmpty, "no selected record")
	}
	row := m.records[selected]
	content := m.recordJSONContent(row)
	numberWidth := m.recordNumberWidth()
	header := consolePalette.panel.Bold(true).Render(fmt.Sprintf("  %-*s │ PRETTY JSON", numberWidth, "RECORD"))
	model := viewport.New(viewport.WithWidth(m.width), viewport.WithHeight(m.visible))
	model.SoftWrap = false
	model.FillHeight = true
	model.SetHorizontalStep(8)
	model.SetContent(content)
	model.SetXOffset(m.horizontal * 8)
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

func ensureViewportSelection(model *viewport.Model, selected int) {
	if selected < 0 {
		return
	}
	if selected < model.YOffset() {
		model.SetYOffset(selected)
		return
	}
	if selected >= model.YOffset()+model.Height() {
		model.SetYOffset(selected - model.Height() + 1)
	}
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
	switch level {
	case statusReady:
		return consolePalette.green.Bold(true).Render(label)
	case statusWarn, statusLoading:
		return consolePalette.amber.Bold(true).Render(label)
	case statusError:
		return consolePalette.danger.Bold(true).Render(label)
	case statusEmpty:
		return consolePalette.cyan.Bold(true).Render(label)
	default:
		return label
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
	ctx := keyContext{Screen: m.screen, Mode: m.recordMode, InputFocused: m.inputFocused(), DialogOpen: m.dialog != nil, ShowHelp: m.showHelp}
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
	return (longest - available + 7) / 8
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
