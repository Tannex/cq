package compaz

import (
	"context"
	"errors"
	"fmt"
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

const (
	// queryResultCap bounds retained result lines; the match count keeps
	// running so a capped search still reports how much it found.
	queryResultCap = 500
	// queryChunkSize is how many cached records one evaluation command covers,
	// keeping the UI responsive between chunks.
	queryChunkSize = 200
	// queryRecordBudget bounds a single record's evaluation so a pathological
	// expression cannot freeze the search.
	queryRecordBudget = 250 * time.Millisecond
	// queryArrayBudget bounds the one-shot whole-dataset evaluation used by
	// array-mode fallback.
	queryArrayBudget = 10 * time.Second
)

// queryPopup is the composited jq console opened from the records screen: a
// single-line expression input over a scrollable results pane. Evaluation is
// lazy — cached records first, then forward pages through the normal browse
// machinery until end-of-records, cancel, or the result cap.
type queryPopup struct {
	input    textinput.Model
	compiled *query.Query

	// fields are the overlay's flattened field paths offered by the
	// ctrl+space completion; comp is the open completion list, nil otherwise.
	fields []completionField
	comp   *queryCompletion

	lines    []string // retained "number │ output" result lines, capped
	matches  int      // all outputs emitted, including past the cap
	errored  int      // records skipped for decode or expression errors
	searched int      // records evaluated so far
	next     int      // index of the next workspace record to evaluate

	scroll    int
	running   bool
	done      bool
	capped    bool
	cancelled bool
	arrayMode bool   // whole-dataset fallback ran after zero streaming matches
	err       string // compile/setup error shown instead of the footer
	lastErr   string // most recent per-record runtime error

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
	Halted     bool
	Canceled   bool
	Array      bool // result of the whole-dataset array fallback
	LastError  string
}

func newQueryPopup(width int) *queryPopup {
	input := textinput.New()
	input.Prompt = "jq  "
	input.Placeholder = `select(.FIELD == "VALUE") | .FIELD`
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
// marks the search idle. The bumped state (done/capped/cancelled/err) is the
// caller's responsibility.
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

// runQuery compiles the expression once and starts a fresh streaming search
// over the workspace record cache.
func (m *Model) runQuery() tea.Cmd {
	ws := m.ws()
	popup := m.query
	if ws.overlay == nil {
		popup.err = queryNeedsOverlay
		return nil
	}
	expr := strings.TrimSpace(popup.input.Value())
	if expr == "" {
		popup.err = "enter a jq expression"
		return nil
	}
	expr, err := normalizeQueryExpression(expr)
	if err != nil {
		popup.err = err.Error()
		return nil
	}
	compiled, err := query.Compile(expr)
	if err != nil {
		popup.err = err.Error()
		return nil
	}
	popup.stopSearch()
	popup.comp = nil
	popup.generation++
	popup.compiled = compiled
	popup.lines = nil
	popup.matches, popup.errored, popup.searched, popup.next = 0, 0, 0, 0
	popup.scroll = 0
	popup.err, popup.lastErr = "", ""
	popup.done, popup.capped, popup.cancelled, popup.arrayMode = false, false, false, false
	popup.running = true
	popup.started = time.Now()
	popup.elapsed = 0
	popup.ctx, popup.cancel = context.WithCancel(context.Background())
	return tea.Batch(popup.spin.Tick, m.queryStep(ws))
}

// queryStep advances the search: evaluate the next cached chunk in a command,
// fetch the next forward page through the normal browse machinery (sharing the
// workspace cache the pager already tolerates growing), or finish.
func (m *Model) queryStep(ws *workspace) tea.Cmd {
	popup := m.query
	if popup == nil || !popup.running || ws != &m.workspace {
		return nil
	}
	if popup.next < len(ws.records) {
		end := min(len(ws.records), popup.next+queryChunkSize)
		rows := make([]zosmf.Record, 0, end-popup.next)
		for _, row := range ws.records[popup.next:end] {
			rows = append(rows, row.Record)
		}
		return evalQueryChunk(popup.ctx, popup.compiled, ws.overlay, rows, ws.profile, popup.generation, queryResultCap-len(popup.lines))
	}
	if ws.recordPage.more {
		if ws.browsePending != nil {
			return nil // queryAfterFetch continues when the in-flight page lands
		}
		var anchor int64
		if len(ws.records) > 0 {
			anchor = ws.records[len(ws.records)-1].Record.Number
		}
		return m.startRecords(ws, pagePlan[int64]{Anchor: anchor, Direction: pageForward})
	}
	// Streaming pass exhausted every record. When it produced nothing but
	// per-record errors, the expression likely wants the whole data set as one
	// value (map, unique, group_by, ...) — fall back to array mode.
	if popup.matches == 0 && popup.errored > 0 && !popup.arrayMode {
		popup.arrayMode = true
		popup.errored = 0
		popup.lastErr = ""
		return evalQueryArray(popup.ctx, popup.compiled, ws.overlay, ws.records, ws.profile, popup.generation)
	}
	popup.stopSearch()
	popup.done = true
	return nil
}

// evalQueryArray runs the expression once over every record's decoded value
// collected into a single JSON array. It only runs after the streaming pass
// has pulled the whole data set into the cache.
func evalQueryArray(ctx context.Context, compiled *query.Query, ov *overlay, rows []recordRow, profile string, generation uint64) tea.Cmd {
	return func() tea.Msg {
		msg := queryEvalMsg{Profile: profile, Generation: generation, Array: true}
		values := make([]any, 0, len(rows))
		for _, row := range rows {
			if ctx.Err() != nil {
				msg.Canceled = true
				return msg
			}
			decoded, err := ov.Decoder.DecodeDisplay(row.Record.Data)
			if err != nil {
				msg.Errors++
				continue
			}
			encoded, err := decoded.JSON()
			if err != nil {
				msg.Errors++
				msg.LastError = err.Error()
				continue
			}
			value, err := query.FromJSON(encoded)
			if err != nil {
				msg.Errors++
				msg.LastError = err.Error()
				continue
			}
			values = append(values, value)
		}
		arrayCtx, cancelArray := context.WithTimeout(ctx, queryArrayBudget)
		defer cancelArray()
		err := compiled.RunContext(arrayCtx, values, func(out any) error {
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
				msg.Halted = true
			case ctx.Err() != nil:
				msg.Canceled = true
			default:
				msg.LastError = err.Error()
			}
		}
		return msg
	}
}

// evalQueryChunk decodes and evaluates one chunk of records off the UI loop.
// Records that fail structural decode or raise expression errors are counted,
// not fatal; halt/halt_error ends the whole search.
func evalQueryChunk(ctx context.Context, compiled *query.Query, ov *overlay, rows []zosmf.Record, profile string, generation uint64, lineRoom int) tea.Cmd {
	return func() tea.Msg {
		msg := queryEvalMsg{Profile: profile, Generation: generation}
		for _, raw := range rows {
			if ctx.Err() != nil {
				msg.Canceled = true
				return msg
			}
			msg.Evaluated++
			decoded, err := ov.Decoder.DecodeDisplay(raw.Data)
			if err != nil {
				msg.Errors++
				continue
			}
			encoded, err := decoded.JSON()
			if err != nil {
				msg.Errors++
				msg.LastError = err.Error()
				continue
			}
			value, err := query.FromJSON(encoded)
			if err != nil {
				msg.Errors++
				msg.LastError = err.Error()
				continue
			}
			recordCtx, cancelRecord := context.WithTimeout(ctx, queryRecordBudget)
			err = compiled.RunContext(recordCtx, value, func(out any) error {
				msg.Matches++
				if len(msg.Lines) < lineRoom {
					msg.Lines = append(msg.Lines, fmt.Sprintf("%5d │ %s", raw.Number, query.Marshal(out)))
				}
				return nil
			})
			cancelRecord()
			if err != nil {
				var halt *query.Halt
				if errors.As(err, &halt) {
					msg.Halted = true
					return msg
				}
				if ctx.Err() != nil {
					msg.Canceled = true
					return msg
				}
				msg.Errors++
				msg.LastError = err.Error()
			}
			if len(msg.Lines) >= lineRoom {
				// Result cap reached: stop mid-chunk so a match-heavy search
				// does not keep evaluating records nobody will see.
				return msg
			}
		}
		return msg
	}
}

func (m *Model) handleQueryEval(msg queryEvalMsg) tea.Cmd {
	popup := m.query
	ws := m.ws()
	if popup == nil || !popup.running || msg.Generation != popup.generation || ws.profile != msg.Profile {
		return nil
	}
	popup.next += msg.Evaluated
	popup.searched += msg.Evaluated
	popup.matches += msg.Matches
	popup.errored += msg.Errors
	if msg.LastError != "" {
		popup.lastErr = msg.LastError
	}
	if room := queryResultCap - len(popup.lines); room > 0 {
		popup.lines = append(popup.lines, msg.Lines[:min(room, len(msg.Lines))]...)
	}
	switch {
	case msg.Canceled:
		popup.stopSearch()
		popup.cancelled = true
		return nil
	case msg.Array, msg.Halted:
		popup.stopSearch()
		popup.done = true
		return nil
	case len(popup.lines) >= queryResultCap:
		popup.stopSearch()
		popup.capped = true
		return nil
	}
	return m.queryStep(ws)
}

// queryAfterFetch continues a running search after a records page was accepted
// (or stops it on a fetch error, so a dead connection cannot retry forever).
func (m *Model) queryAfterFetch(ws *workspace, fetchErr error) tea.Cmd {
	popup := m.query
	if popup == nil || !popup.running || ws != &m.workspace {
		return nil
	}
	if fetchErr != nil {
		popup.stopSearch()
		popup.err = "search stopped: " + fetchErr.Error()
		return nil
	}
	return m.queryStep(ws)
}

// queryFooter is the progress/result summary under the results pane, e.g.
// "⠸ 1.42s searched 400 of 118+ records… — 12 matches" while running and
// just "1.42s — 12 matches" once done.
func (m *Model) queryFooter() string {
	popup := m.query
	ws := m.ws()
	total := len(ws.records)
	suffix := ""
	if ws.recordPage.more {
		suffix = "+"
	}
	clock := formatElapsed(popup.clock())
	var state string
	switch {
	case popup.running && popup.arrayMode:
		state = fmt.Sprintf("%s %s evaluating…", popup.spin.View(), clock)
	case popup.running:
		state = fmt.Sprintf("%s %s searched %d of %d%s records…", popup.spin.View(), clock, popup.searched, total, suffix)
	case popup.cancelled:
		state = fmt.Sprintf("%s cancelled after %d of %d%s records", clock, popup.searched, total, suffix)
	case popup.capped:
		state = fmt.Sprintf("%s stopped at the %d-result cap after %d of %d%s records", clock, queryResultCap, popup.searched, total, suffix)
	case popup.done:
		state = clock
	default:
		return "enter runs the query over every record  esc closes"
	}
	if popup.arrayMode && !popup.running {
		state += fmt.Sprintf(" — %d results", popup.matches)
	} else {
		state += fmt.Sprintf(" — %d matches", popup.matches)
	}
	if popup.errored > 0 {
		state += fmt.Sprintf(", %d records skipped", popup.errored)
	}
	return state
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
		text := "results stream here, prefixed with the record number"
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
	completion := m.queryCompletionLines(innerWidth, max(1, contentHeight-2-len(footer)))
	for _, line := range completion {
		innerLines = append(innerLines, fill.Render(line))
	}
	for _, line := range m.queryResultLines(body, muted, innerWidth, max(1, contentHeight-1-len(completion)-len(footer))) {
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
	mainLayer := lipgloss.NewLayer(background).X(0).Y(0).Z(0)
	popupLayer := lipgloss.NewLayer(popup).X(x).Y(top).Z(1)
	compositor := lipgloss.NewCompositor(mainLayer, popupLayer)
	return canvas.Compose(compositor).Render()
}
