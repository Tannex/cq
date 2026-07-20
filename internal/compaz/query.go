package compaz

import (
	"context"
	"errors"
	"fmt"
	"math/rand/v2"
	"regexp"
	"strings"
	"time"

	"charm.land/bubbles/v2/spinner"
	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/Tannex/cq/internal/query"
	"github.com/Tannex/cq/internal/zosmf"
)

// queryResultCap bounds retained result lines; the result count keeps
// running so a capped query still reports how much it produced.
const queryResultCap = 500

// queryPopup is the composited jq console opened from the records screen: a
// single-line expression input over a scrollable results pane. A query always
// runs over the whole data set as one JSON array: the records are downloaded
// once into the workspace's bulk cache (reused across runs while the same
// records are browsed) and the expression is evaluated in a single pass.
type queryPopup struct {
	input    textinput.Model
	compiled *query.Query

	// fields are the overlay's flattened field paths offered by the
	// ctrl+space completion; comp is the open completion list, nil otherwise.
	fields []completionField
	comp   *queryCompletion

	lines    []string // retained result lines, capped
	matches  int      // all outputs emitted, including past the cap
	errored  int      // records skipped for decode errors
	searched int      // records decoded into the array input

	scroll    int
	running   bool
	done      bool
	cancelled bool
	err       string // compile/setup/download error shown instead of the footer
	lastErr   string // most recent runtime error
	notice    string // copy feedback appended to the footer state

	generation uint64
	ctx        context.Context
	cancel     context.CancelFunc

	spin    spinner.Model
	started time.Time
	elapsed time.Duration
}

type queryEvalMsg struct {
	Profile    string
	Generation uint64
	Lines      []string
	Matches    int
	Errors     int
	Evaluated  int
	Canceled   bool
	LastError  string
}

// queryPlaceholderProvider produces the example expression shown as the input
// placeholder, built from the overlay's flattened field paths. A variable so a
// smarter generator can replace the static template set later.
type queryPlaceholderProvider func(fields []completionField) string

var queryPlaceholder queryPlaceholderProvider = examplePlaceholder

// queryExampleTemplates double as discovery for the console's syntax: filters,
// projections, and the whole-dataset array-mode aggregates. {f1}/{f2} are
// replaced with jq references to actual overlay fields.
var queryExampleTemplates = []string{
	`select({f1} == "VALUE") | {f2}`,
	`select({f1} != null) | [{f1}, {f2}]`,
	`{f1}`,
	`[{f1}, {f2}]`,
	`select({f1} | test("^A"))`,
	`map({f1}) | unique`,
	`map({f1}) | unique | length`,
	`group_by({f1}) | map({key: (.[0] | {f1}), count: length})`,
	`length`,
}

func examplePlaceholder(fields []completionField) string {
	if len(fields) == 0 {
		return `select(.FIELD == "VALUE") | .FIELD`
	}
	template := queryExampleTemplates[rand.IntN(len(queryExampleTemplates))]
	return strings.NewReplacer(
		"{f1}", jqFieldRef(fields[rand.IntN(len(fields))].parts),
		"{f2}", jqFieldRef(fields[rand.IntN(len(fields))].parts),
	).Replace(template)
}

func newQueryPopup(width int) *queryPopup {
	input := textinput.New()
	input.Prompt = "jq  "
	input.Placeholder = `.[] | select(.FIELD == "VALUE") | .FIELD`
	input.CharLimit = 512
	styles := input.Styles()
	styles.Cursor.Blink = false
	input.SetStyles(styles)
	popup := &queryPopup{
		input: input,
		spin:  newStatusSpinner(),
	}
	popup.setWidth(width)
	return popup
}

func (p *queryPopup) setWidth(width int) {
	p.input.SetWidth(max(8, min(68, width-12)))
}

// stopSearch cancels any in-flight evaluation, freezes the elapsed clock, and
// marks the query idle. The bumped state (done/cancelled/err) is the caller's
// responsibility.
func (p *queryPopup) stopSearch() {
	if p.cancel != nil {
		p.cancel()
	}
	p.cancel = nil
	p.ctx = nil
	if p.running {
		p.elapsed = time.Since(p.started)
	}
	p.running = false
}

// clock is the live or frozen elapsed search time.
func (p *queryPopup) clock() time.Duration {
	if p.running {
		return time.Since(p.started)
	}
	return p.elapsed
}

// formatElapsed keeps sub-second searches meaningful: most complete in
// milliseconds, where a m:ss clock would always read 0:00.
func formatElapsed(d time.Duration) string {
	switch {
	case d < time.Second:
		return fmt.Sprintf("%dms", d.Milliseconds())
	case d < 10*time.Second:
		return fmt.Sprintf("%.2fs", d.Seconds())
	case d < time.Minute:
		return fmt.Sprintf("%.1fs", d.Seconds())
	default:
		return fmt.Sprintf("%d:%02d", int(d.Minutes()), int(d.Seconds())%60)
	}
}

func (p *queryPopup) scrollBy(delta int) {
	p.scroll = max(0, min(p.scroll+delta, max(0, len(p.lines)-1)))
}

// completionField is one insertable field path: label is the display form
// from the table header, parts the path segments used to build the jq
// reference with per-segment quoting.
type completionField struct {
	label string
	parts []string
}

type queryCompletion struct {
	filtered []completionField
	selected int
}

// jqPlainIdent matches field names jq accepts after a bare dot; anything else
// (COBOL names with dashes, above all) needs the quoted ."NAME" form.
var jqPlainIdent = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

func jqFieldRef(parts []string) string {
	var sb strings.Builder
	for _, part := range parts {
		if jqPlainIdent.MatchString(part) {
			sb.WriteString("." + part)
		} else {
			sb.WriteString(`."` + part + `"`)
		}
	}
	return sb.String()
}

// fieldTokenAt finds the partial field reference ending at the cursor: the
// name characters scanned left from the cursor plus the `."` or `.` opener in
// front of them. start is the rune index where an inserted reference should
// replace from; token is the partial name used as the completion filter.
func fieldTokenAt(value []rune, cursor int) (start int, token string) {
	cursor = max(0, min(cursor, len(value)))
	nameStart := cursor
	for nameStart > 0 {
		r := value[nameStart-1]
		if r == '-' || r == '_' ||
			'a' <= r && r <= 'z' || 'A' <= r && r <= 'Z' || '0' <= r && r <= '9' {
			nameStart--
			continue
		}
		break
	}
	start = nameStart
	if nameStart > 1 && value[nameStart-1] == '"' && value[nameStart-2] == '.' {
		start = nameStart - 2
	} else if nameStart > 0 && value[nameStart-1] == '.' {
		start = nameStart - 1
	}
	return start, string(value[nameStart:cursor])
}

// completionCandidates filters the overlay fields by a case-insensitive
// substring match on the partial name.
func (p *queryPopup) completionCandidates(token string) []completionField {
	token = strings.ToUpper(token)
	var matched []completionField
	for _, field := range p.fields {
		if token == "" || strings.Contains(strings.ToUpper(field.label), token) {
			matched = append(matched, field)
		}
	}
	return matched
}

// openCompletion opens the field list for the token under the cursor. A
// single candidate is inserted immediately instead of opening a one-row list.
func (p *queryPopup) openCompletion() {
	_, token := fieldTokenAt([]rune(p.input.Value()), p.input.Position())
	matched := p.completionCandidates(token)
	switch len(matched) {
	case 0:
		return
	case 1:
		p.comp = nil
		p.insertCompletion(matched[0])
		return
	}
	p.comp = &queryCompletion{filtered: matched}
}

// refreshCompletion re-filters an open list after the input changed; the list
// closes when nothing matches any more.
func (p *queryPopup) refreshCompletion() {
	if p.comp == nil {
		return
	}
	matched := p.completionCandidates(func() string {
		_, token := fieldTokenAt([]rune(p.input.Value()), p.input.Position())
		return token
	}())
	if len(matched) == 0 {
		p.comp = nil
		return
	}
	p.comp.filtered = matched
	p.comp.selected = min(p.comp.selected, len(matched)-1)
}

func (p *queryPopup) moveCompletion(delta int) {
	comp := p.comp
	if comp == nil || len(comp.filtered) == 0 {
		return
	}
	comp.selected = (comp.selected + delta + len(comp.filtered)) % len(comp.filtered)
}

// insertCompletion replaces the partial field reference under the cursor with
// the selected field's jq form, quoting segments jq cannot take after a bare
// dot (dashes in COBOL names).
func (p *queryPopup) insertCompletion(field completionField) {
	value := []rune(p.input.Value())
	cursor := max(0, min(p.input.Position(), len(value)))
	start, _ := fieldTokenAt(value, cursor)
	ref := []rune(jqFieldRef(field.parts))
	updated := make([]rune, 0, len(value)+len(ref))
	updated = append(updated, value[:start]...)
	updated = append(updated, ref...)
	updated = append(updated, value[cursor:]...)
	p.input.SetValue(string(updated))
	p.input.SetCursor(start + len(ref))
	p.comp = nil
}

const queryNeedsOverlay = "queries run over decoded records — load a copybook with c first"

// openQueryPopup opens the jq console. Queries need a copybook overlay: they
// run over each record's decoded value, so raw browsing gets an explanation
// instead of an input that cannot work.
func (m *Model) openQueryPopup() tea.Cmd {
	popup := newQueryPopup(m.width)
	if overlay := m.ws().overlay; overlay == nil {
		popup.err = queryNeedsOverlay
	} else {
		popup.fields = make([]completionField, 0, len(overlay.Columns))
		for _, column := range overlay.Columns {
			popup.fields = append(popup.fields, completionField{label: column.Path, parts: column.Parts})
		}
		popup.input.Placeholder = queryPlaceholder(popup.fields)
	}
	m.query = popup
	return popup.input.Focus()
}

// handleQueryKey routes keys while the query popup is open. An open
// completion list captures navigation and accept/cancel; everything else
// falls through to the expression input, re-filtering the list as the token
// under the cursor changes.
func (m *Model) handleQueryKey(msg tea.KeyPressMsg, selected action) tea.Cmd {
	popup := m.query
	switch selected {
	case actionQueryComplete:
		popup.openCompletion()
		return nil
	case actionQueryCopy:
		return m.copyQueryResults()
	case actionAccept, actionNextField:
		if comp := popup.comp; comp != nil {
			popup.insertCompletion(comp.filtered[comp.selected])
			return nil
		}
		if selected == actionAccept {
			return m.runQuery()
		}
		return nil
	case actionCancel:
		if popup.comp != nil {
			popup.comp = nil
			return nil
		}
		if popup.running {
			popup.stopSearch()
			popup.cancelled = true
			return nil
		}
		popup.stopSearch()
		m.query = nil
		return nil
	case actionUp:
		if popup.comp != nil {
			popup.moveCompletion(-1)
		} else {
			popup.scrollBy(-1)
		}
		return nil
	case actionDown:
		if popup.comp != nil {
			popup.moveCompletion(1)
		} else {
			popup.scrollBy(1)
		}
		return nil
	case actionPageUp:
		popup.scrollBy(-max(1, m.visible))
		return nil
	case actionPageDown:
		popup.scrollBy(max(1, m.visible))
		return nil
	default:
		updated, cmd := popup.input.Update(msg)
		popup.input = updated
		popup.refreshCompletion()
		return cmd
	}
}

// normalizeQueryExpression rewrites single-quoted strings to the double-quoted
// form gojq understands. jq's grammar gives ' no other meaning, so the rewrite
// is safe; content is taken literally with " and \ escaped. An unbalanced
// quote is an error rather than a guess.
func normalizeQueryExpression(expr string) (string, error) {
	if !strings.ContainsRune(expr, '\'') {
		return expr, nil
	}
	var sb strings.Builder
	inDouble, inSingle, escaped := false, false, false
	for _, r := range expr {
		switch {
		case inDouble:
			sb.WriteRune(r)
			if escaped {
				escaped = false
			} else if r == '\\' {
				escaped = true
			} else if r == '"' {
				inDouble = false
			}
		case inSingle:
			switch r {
			case '\'':
				sb.WriteByte('"')
				inSingle = false
			case '"', '\\':
				sb.WriteByte('\\')
				sb.WriteRune(r)
			default:
				sb.WriteRune(r)
			}
		case r == '\'':
			sb.WriteByte('"')
			inSingle = true
		case r == '"':
			sb.WriteRune(r)
			inDouble = true
		default:
			sb.WriteRune(r)
		}
	}
	if inSingle {
		return "", errors.New("unbalanced single quote in expression")
	}
	return sb.String(), nil
}

// runQuery compiles the expression once and starts a fresh whole-data-set
// evaluation, downloading the records first when no cached copy exists.
func (m *Model) runQuery() tea.Cmd {
	ws := m.ws()
	popup := m.query
	if ws.overlay == nil {
		popup.err = queryNeedsOverlay
		return nil
	}
	typed := strings.TrimSpace(popup.input.Value())
	if typed == "" {
		popup.err = "enter a jq expression"
		return nil
	}
	expr, err := normalizeQueryExpression(typed)
	if err != nil {
		// A rejected expression supersedes any search still running; without
		// this the old run keeps mutating results behind the error banner.
		popup.stopSearch()
		popup.err = err.Error()
		m.recordQuery(typed, false)
		return nil
	}
	compiled, err := query.Compile(expr)
	if err != nil {
		popup.stopSearch()
		popup.err = err.Error()
		m.recordQuery(typed, false)
		return nil
	}
	m.recordQuery(typed, true)
	popup.stopSearch()
	popup.comp = nil
	popup.generation++
	popup.compiled = compiled
	popup.lines = nil
	popup.matches, popup.errored, popup.searched = 0, 0, 0
	popup.scroll = 0
	popup.err, popup.lastErr, popup.notice = "", "", ""
	popup.done, popup.cancelled = false, false
	popup.running = true
	popup.started = time.Now()
	popup.elapsed = 0
	popup.ctx, popup.cancel = context.WithCancel(context.Background())
	if path := ws.bulkRecords(); path != "" {
		return tea.Batch(popup.spin.Tick, evalQueryArrayFile(popup.ctx, compiled, ws.overlay, path, ws.profile, popup.generation))
	}
	streamer, ok := ws.browser.(zosmf.RecordStreamer)
	if !ok {
		popup.stopSearch()
		popup.err = "this session cannot download records for queries"
		return nil
	}
	return tea.Batch(popup.spin.Tick, m.startQueryBulkDownload(ws, streamer))
}

// recordQuery tracks an executed expression — as typed, before quote
// normalization — with its compile outcome for the local usage log.
func (m *Model) recordQuery(expr string, ok bool) {
	if m.deps.Events != nil {
		m.deps.Events.RecordQuery(expr, ok)
	}
}

// decodeQueryValue turns raw record bytes into the jq input value, counting
// decode failures on the message instead of failing the search.
func decodeQueryValue(ov *overlay, data []byte, msg *queryEvalMsg) (any, bool) {
	decoded, err := ov.Decoder.DecodeDisplay(data)
	if err != nil {
		msg.Errors++
		return nil, false
	}
	encoded, err := decoded.JSON()
	if err != nil {
		msg.Errors++
		msg.LastError = err.Error()
		return nil, false
	}
	value, err := query.FromJSON(encoded)
	if err != nil {
		msg.Errors++
		msg.LastError = err.Error()
		return nil, false
	}
	return value, true
}

// runQueryArray runs the expression once over the collected values, mapping
// halt/cancel/errors onto the message. Evaluation is local work off the UI
// loop and deliberately has no time budget: the popup context cancels it on
// esc, close, and quit, which is the only bound a long-running expression
// needs.
func runQueryArray(ctx context.Context, compiled *query.Query, values []any, msg *queryEvalMsg) {
	err := compiled.RunContext(ctx, values, func(out any) error {
		msg.Matches++
		if len(msg.Lines) < queryResultCap {
			msg.Lines = append(msg.Lines, query.Marshal(out))
		}
		return nil
	})
	if err != nil {
		var halt *query.Halt
		switch {
		case errors.As(err, &halt):
		case ctx.Err() != nil:
			msg.Canceled = true
		default:
			msg.LastError = err.Error()
		}
	}
}

func (m *Model) handleQueryEval(msg queryEvalMsg) tea.Cmd {
	popup := m.query
	ws := m.ws()
	if popup == nil || !popup.running || msg.Generation != popup.generation || ws.profile != msg.Profile {
		return nil
	}
	popup.searched += msg.Evaluated
	popup.matches += msg.Matches
	popup.errored += msg.Errors
	if msg.LastError != "" {
		popup.lastErr = msg.LastError
	}
	if room := queryResultCap - len(popup.lines); room > 0 {
		popup.lines = append(popup.lines, msg.Lines[:min(room, len(msg.Lines))]...)
	}
	popup.stopSearch()
	if msg.Canceled {
		popup.cancelled = true
	} else {
		popup.done = true
	}
	return nil
}

// queryFooter is the progress/result summary under the results pane, e.g.
// "⠸ 1.42s downloading the whole data set…" while running and just
// "1.42s — 12 results" once done.
func (m *Model) queryFooter() string {
	popup := m.query
	ws := m.ws()
	clock := formatElapsed(popup.clock())
	var state string
	switch {
	case popup.running && ws.bulkRecordsPath == "":
		state = fmt.Sprintf("%s %s downloading the whole data set…", popup.spin.View(), clock)
	case popup.running:
		state = fmt.Sprintf("%s %s evaluating the downloaded records…", popup.spin.View(), clock)
	case popup.cancelled:
		state = fmt.Sprintf("%s cancelled", clock)
	case popup.done:
		state = fmt.Sprintf("%s — %d results", clock, popup.matches)
		if popup.matches > len(popup.lines) {
			state += fmt.Sprintf(", first %d shown", len(popup.lines))
		}
	default:
		return "enter runs the query over the whole data set as one array  esc closes"
	}
	if popup.errored > 0 {
		state += fmt.Sprintf(", %d records skipped", popup.errored)
	}
	if popup.notice != "" {
		state += " — " + popup.notice
	}
	return state
}

// copyQueryResults puts the retained result lines on the system clipboard via
// OSC 52, so the copy works over SSH too. tea.SetClipboard is fire-and-forget;
// the footer notice is the only feedback.
func (m *Model) copyQueryResults() tea.Cmd {
	popup := m.query
	if len(popup.lines) == 0 {
		return nil
	}
	if popup.matches > len(popup.lines) {
		popup.notice = fmt.Sprintf("copied the %d retained lines (result cap)", len(popup.lines))
	} else {
		popup.notice = fmt.Sprintf("copied %d lines", len(popup.lines))
	}
	return tea.SetClipboard(strings.Join(popup.lines, "\n"))
}

// wrapPlain hard-wraps text at width, returning at most maxLines lines with
// the final line truncated if the text is longer.
func wrapPlain(text string, width, maxLines int) []string {
	if width < 1 || maxLines < 1 {
		return nil
	}
	runes := []rune(text)
	var lines []string
	for len(runes) > 0 && len(lines) < maxLines {
		end := min(width, len(runes))
		lines = append(lines, string(runes[:end]))
		runes = runes[end:]
	}
	if len(runes) > 0 && len(lines) > 0 {
		lines[len(lines)-1] = truncateStyled(lines[len(lines)-1]+"…", width)
	}
	return lines
}

// queryFooterLines renders the footer block: the state line plus the full
// (wrapped, not truncated) text of the most recent error, per review feedback
// that "last error: …" was unreadable when truncated to one line.
func (m *Model) queryFooterLines(muted, danger lipgloss.Style, width int) []string {
	popup := m.query
	if popup.err != "" {
		lines := make([]string, 0, 3)
		for _, line := range wrapPlain("ERROR  "+popup.err, width, 3) {
			lines = append(lines, danger.Render(truncateStyled(line, width)))
		}
		return lines
	}
	lines := []string{muted.Render(truncateStyled(m.queryFooter(), width))}
	if popup.lastErr != "" && !popup.running {
		for _, line := range wrapPlain("last error: "+popup.lastErr, width, 2) {
			lines = append(lines, danger.Render(truncateStyled(line, width)))
		}
	}
	return lines
}

// queryResultLines renders the scrolled results window at exactly rows lines.
func (m *Model) queryResultLines(body, muted lipgloss.Style, width, rows int) []string {
	popup := m.query
	lines := make([]string, 0, rows)
	if len(popup.lines) == 0 {
		text := "results appear here once the data set is evaluated"
		if popup.running {
			text = "searching…"
		}
		lines = append(lines, muted.Render(truncateStyled(text, width)))
	} else {
		popup.scroll = min(popup.scroll, max(0, len(popup.lines)-rows))
		for _, line := range scrollWindow(popup.lines, popup.scroll, rows) {
			lines = append(lines, body.Render(truncateStyled(line, width)))
		}
	}
	for len(lines) < rows {
		lines = append(lines, "")
	}
	return lines
}

// queryCompletionLines renders the open ctrl+space field list, at most
// maxRows entries with the selection kept in view.
func (m *Model) queryCompletionLines(width, maxRows int) []string {
	comp := m.query.comp
	if comp == nil || maxRows < 1 {
		return nil
	}
	first := max(0, min(comp.selected-maxRows+1, len(comp.filtered)-maxRows))
	lines := make([]string, 0, maxRows)
	for i := first; i < len(comp.filtered) && len(lines) < maxRows; i++ {
		line := truncateStyled("  "+comp.filtered[i].label, width)
		if i == comp.selected {
			line = consolePalette.selected.Render(truncateStyled("> "+comp.filtered[i].label, width))
		}
		lines = append(lines, line)
	}
	return lines
}

// queryPanel is the tiny-terminal fallback: a full-area panel in the data
// region, mirroring helpPanel.
func (m *Model) queryPanel() string {
	footer := m.queryFooterLines(consolePalette.muted, consolePalette.danger, m.width)
	lines := []string{consolePalette.panel.Bold(true).Width(m.width).Render("  JQ QUERY  enter run  esc close")}
	lines = append(lines, truncateStyled(m.query.input.View(), m.width))
	completion := m.queryCompletionLines(m.width, 6)
	lines = append(lines, completion...)
	lines = append(lines, m.queryResultLines(consolePalette.plain, consolePalette.muted, m.width, max(1, m.visible-1-len(completion)-len(footer)))...)
	lines = append(lines, footer...)
	capacity := m.visible + 1
	lines = scrollWindow(lines, 0, capacity)
	for len(lines) < capacity {
		lines = append(lines, "")
	}
	return strings.Join(lines, "\n")
}

// overlayQuery composites the query popup over the live view with the same
// Canvas/Layer mechanism and styling as the help popup. Unlike help it
// anchors below the table header row, so the column names the expression
// refers to stay readable while typing.
func (m *Model) overlayQuery(background string) string {
	popupWidth := min(76, m.width-4)
	// Rows above the popup: title, search, rule, and the table header line;
	// the status and help lines stay visible below.
	top := 4
	popupHeight := m.height - top - 2
	if popupHeight < 8 {
		// Terminal too short to spare the chrome: fall back to centering.
		popupHeight = min(max(12, int(float64(m.height)*0.8)), m.height-2)
		top = (m.height - popupHeight) / 2
	}
	if popupWidth < 24 || popupHeight < 8 {
		return background
	}

	innerWidth := popupWidth - 4
	contentHeight := popupHeight - 4

	fill := consolePalette.popup.Width(innerWidth)
	accent := consolePalette.cyan.Bold(true).Inherit(consolePalette.popup)
	body := consolePalette.popup
	muted := consolePalette.muted.Inherit(consolePalette.popup)
	danger := consolePalette.danger.Inherit(consolePalette.popup)

	title := accent.Render("JQ QUERY")
	footer := m.queryFooterLines(muted, danger, innerWidth)
	innerLines := []string{
		fill.Render(strings.Repeat(" ", max(0, (innerWidth-lipgloss.Width(title))/2)) + title),
		fill.Render(truncateStyled(m.query.input.View(), innerWidth)),
		fill.Render(muted.Render(strings.Repeat("─", max(0, innerWidth)))),
	}
	for _, line := range m.queryResultLines(body, muted, innerWidth, max(1, contentHeight-1-len(footer))) {
		innerLines = append(innerLines, fill.Render(line))
	}
	for len(innerLines) < contentHeight+2-len(footer) {
		innerLines = append(innerLines, fill.Render(""))
	}
	for _, line := range footer {
		innerLines = append(innerLines, fill.Render(line))
	}

	popupStyle := consolePalette.popup.
		Border(lipgloss.RoundedBorder()).
		BorderForeground(consolePalette.popupBorder.GetForeground()).
		BorderBackground(consolePalette.popup.GetBackground()).
		Padding(0, 1).
		Width(popupWidth).
		Height(popupHeight)
	popup := popupStyle.Render(strings.Join(innerLines, "\n"))

	x := (m.width - popupWidth) / 2

	canvas := lipgloss.NewCanvas(m.width, m.height)
	layers := []*lipgloss.Layer{
		lipgloss.NewLayer(background).X(0).Y(0).Z(0),
		lipgloss.NewLayer(popup).X(x).Y(top).Z(1),
	}
	// The ctrl+space field list is its own pop-over so it overlaps the
	// results instead of displacing them. The input renders at top+2 (border
	// plus title row); the list hangs directly under it, anchored to the
	// token being completed.
	// Row budget: the box (rows + 2 border lines) starts at top+3 and must
	// end above the popup's bottom border at m.height-3, so rows are capped
	// at m.height-top-7.
	if box, offset := m.queryCompletionBox(innerWidth, max(1, min(6, m.height-top-7))); box != "" {
		// offset is the token's rune index, which drifts from the rendered
		// column once the input scrolls horizontally; clamping to the input
		// area keeps the box anchored to the field text in that case.
		offset = min(offset, 4+m.query.input.Width())
		compX := min(x+2+offset, x+popupWidth-lipgloss.Width(box)-1)
		layers = append(layers, lipgloss.NewLayer(box).X(max(x+1, compX)).Y(top+3).Z(2))
	}
	compositor := lipgloss.NewCompositor(layers...)
	return canvas.Compose(compositor).Render()
}

// queryCompletionBox renders the open completion list as a bordered pop-over.
// The returned offset is the column of the partial token inside the popup's
// inner width, so the caller can anchor the box under the text it replaces.
func (m *Model) queryCompletionBox(innerWidth, maxRows int) (string, int) {
	comp := m.query.comp
	if comp == nil || maxRows < 1 {
		return "", 0
	}
	width := 0
	for _, field := range comp.filtered {
		width = max(width, lipgloss.Width(field.label))
	}
	width = min(width+2, innerWidth-2)
	if width < 4 {
		return "", 0
	}
	rows := m.queryCompletionLines(width, maxRows)
	// Width is the full block including the border, so the row width gets +2.
	box := consolePalette.popup.
		Border(lipgloss.RoundedBorder()).
		BorderForeground(consolePalette.popupBorder.GetForeground()).
		Width(width + 2).
		Render(strings.Join(rows, "\n"))
	start, _ := fieldTokenAt([]rune(m.query.input.Value()), m.query.input.Position())
	return box, lipgloss.Width(m.query.input.Prompt) + start
}
