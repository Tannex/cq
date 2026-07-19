package cqt

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"charm.land/bubbles/v2/spinner"
	"charm.land/bubbles/v2/stopwatch"
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
)

// queryPopup is the composited jq console opened from the records screen: a
// single-line expression input over a scrollable results pane. Evaluation is
// lazy — cached records first, then forward pages through the normal browse
// machinery until end-of-records, cancel, or the result cap.
type queryPopup struct {
	input    textinput.Model
	compiled *query.Query

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
	err       string // compile/setup error shown instead of the footer
	lastErr   string // most recent per-record runtime error

	generation uint64
	ctx        context.Context
	cancel     context.CancelFunc

	spin  spinner.Model
	watch stopwatch.Model
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
		watch: stopwatch.New(),
	}
	popup.setWidth(width)
	return popup
}

func (p *queryPopup) setWidth(width int) {
	p.input.SetWidth(max(8, min(68, width-12)))
}

// stopSearch cancels any in-flight evaluation and marks the search idle. The
// bumped state (done/capped/cancelled/err) is the caller's responsibility.
func (p *queryPopup) stopSearch() {
	if p.cancel != nil {
		p.cancel()
	}
	p.cancel = nil
	p.ctx = nil
	p.running = false
}

func (p *queryPopup) scrollBy(delta int) {
	p.scroll = max(0, min(p.scroll+delta, max(0, len(p.lines)-1)))
}

const queryNeedsOverlay = "queries run over decoded records — load a copybook with c first"

// openQueryPopup opens the jq console. Queries need a copybook overlay: they
// run over each record's decoded value, so raw browsing gets an explanation
// instead of an input that cannot work.
func (m *Model) openQueryPopup() tea.Cmd {
	popup := newQueryPopup(m.width)
	if m.ws().overlay == nil {
		popup.err = queryNeedsOverlay
	}
	m.query = popup
	return popup.input.Focus()
}

// handleQueryKey routes keys while the query popup is open.
func (m *Model) handleQueryKey(msg tea.KeyPressMsg, selected action) tea.Cmd {
	popup := m.query
	switch selected {
	case actionAccept:
		return m.runQuery()
	case actionCancel:
		if popup.running {
			popup.stopSearch()
			popup.cancelled = true
			return popup.watch.Stop()
		}
		popup.stopSearch()
		m.query = nil
		return nil
	case actionPageUp:
		popup.scrollBy(-max(1, m.visible))
		return nil
	case actionPageDown:
		popup.scrollBy(max(1, m.visible))
		return nil
	case actionQuit:
		m.cancelAll()
		return tea.Quit
	default:
		updated, cmd := popup.input.Update(msg)
		popup.input = updated
		return cmd
	}
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
	compiled, err := query.Compile(expr)
	if err != nil {
		popup.err = err.Error()
		return nil
	}
	popup.stopSearch()
	popup.generation++
	popup.compiled = compiled
	popup.lines = nil
	popup.matches, popup.errored, popup.searched, popup.next = 0, 0, 0, 0
	popup.scroll = 0
	popup.err, popup.lastErr = "", ""
	popup.done, popup.capped, popup.cancelled = false, false, false
	popup.running = true
	popup.ctx, popup.cancel = context.WithCancel(context.Background())
	return tea.Batch(popup.watch.Reset(), popup.watch.Start(), popup.spin.Tick, m.queryStep(ws))
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
	popup.stopSearch()
	popup.done = true
	return popup.watch.Stop()
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
					msg.Lines = append(msg.Lines, fmt.Sprintf("%d │ %s", raw.Number, query.Marshal(out)))
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
		return popup.watch.Stop()
	case msg.Halted:
		popup.stopSearch()
		popup.done = true
		return popup.watch.Stop()
	case len(popup.lines) >= queryResultCap:
		popup.stopSearch()
		popup.capped = true
		return popup.watch.Stop()
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
		return popup.watch.Stop()
	}
	return m.queryStep(ws)
}

// queryFooter is the progress/result summary under the results pane, e.g.
// "⠸ 0:07 searched 400 of 118+ records… — 12 matches".
func (m *Model) queryFooter() string {
	popup := m.query
	ws := m.ws()
	total := len(ws.records)
	suffix := ""
	if ws.recordPage.more {
		suffix = "+"
	}
	elapsed := popup.watch.Elapsed()
	clock := fmt.Sprintf("%d:%02d", int(elapsed.Minutes()), int(elapsed.Seconds())%60)
	var state string
	switch {
	case popup.running:
		state = fmt.Sprintf("%s %s searched %d of %d%s records…", popup.spin.View(), clock, popup.searched, total, suffix)
	case popup.cancelled:
		state = fmt.Sprintf("%s cancelled after %d of %d%s records", clock, popup.searched, total, suffix)
	case popup.capped:
		state = fmt.Sprintf("%s stopped at the %d-result cap after %d of %d%s records", clock, queryResultCap, popup.searched, total, suffix)
	case popup.done:
		state = fmt.Sprintf("%s searched %d records", clock, popup.searched)
	default:
		return "enter runs the query over every record  esc closes"
	}
	state += fmt.Sprintf(" — %d matches", popup.matches)
	if popup.errored > 0 {
		state += fmt.Sprintf(", %d records skipped", popup.errored)
	}
	if popup.lastErr != "" {
		state += "  last error: " + popup.lastErr
	}
	return state
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

func (m *Model) queryFooterLine(muted, danger lipgloss.Style, width int) string {
	popup := m.query
	if popup.err != "" {
		return danger.Render(truncateStyled("ERROR  "+popup.err, width))
	}
	return muted.Render(truncateStyled(m.queryFooter(), width))
}

// queryPanel is the tiny-terminal fallback: a full-area panel in the data
// region, mirroring helpPanel.
func (m *Model) queryPanel() string {
	lines := []string{consolePalette.panel.Bold(true).Width(m.width).Render("  JQ QUERY  enter run  esc close")}
	lines = append(lines, truncateStyled(m.query.input.View(), m.width))
	lines = append(lines, m.queryResultLines(consolePalette.plain, consolePalette.muted, m.width, max(1, m.visible-2))...)
	lines = append(lines, m.queryFooterLine(consolePalette.muted, consolePalette.danger, m.width))
	capacity := m.visible + 1
	lines = scrollWindow(lines, 0, capacity)
	for len(lines) < capacity {
		lines = append(lines, "")
	}
	return strings.Join(lines, "\n")
}

// overlayQuery composites the query popup over the live view with the same
// Canvas/Layer mechanism and sizing rules as the help popup.
func (m *Model) overlayQuery(background string) string {
	popupWidth := min(76, m.width-4)
	popupHeight := min(max(12, int(float64(m.height)*0.8)), m.height-2)
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
	innerLines := []string{
		fill.Render(strings.Repeat(" ", max(0, (innerWidth-lipgloss.Width(title))/2)) + title),
		fill.Render(truncateStyled(m.query.input.View(), innerWidth)),
		fill.Render(muted.Render(strings.Repeat("─", max(0, innerWidth)))),
	}
	for _, line := range m.queryResultLines(body, muted, innerWidth, max(1, contentHeight-2)) {
		innerLines = append(innerLines, fill.Render(line))
	}
	for len(innerLines) < contentHeight+1 {
		innerLines = append(innerLines, fill.Render(""))
	}
	innerLines = append(innerLines, fill.Render(m.queryFooterLine(muted, danger, innerWidth)))

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
