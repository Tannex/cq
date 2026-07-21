package compaz

import (
	"bytes"
	"context"
	"encoding/binary"
	"io"
	"slices"
	"strings"
	"testing"
	"time"

	"charm.land/bubbles/v2/spinner"
	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/Tannex/cq/internal/zosmf"
)

// executeQuery drains a command tree like executeCommand but drops the
// spinner/stopwatch tick loops, which otherwise re-arm on every tick while a
// search is running and sleep real time between frames.
func executeQuery(t *testing.T, model *Model, command tea.Cmd) {
	t.Helper()
	queue := []tea.Cmd{command}
	for len(queue) > 0 {
		current := queue[0]
		queue = queue[1:]
		if current == nil {
			continue
		}
		message := current()
		if batch, ok := message.(tea.BatchMsg); ok {
			queue = append(queue, batch...)
			continue
		}
		switch message.(type) {
		case spinner.TickMsg, nil:
			continue
		}
		if next := applyMessage(t, model, message); next != nil {
			queue = append(queue, next)
		}
	}
}

// queryModel builds a model on the records screen of a PS data set with a
// copybook overlay applied, backed by the supplied record pages. The bulk
// stream serves the same records as frames, page order preserved.
func queryModel(t *testing.T, pages map[int64]zosmf.RecordPage) (*Model, *fakeStreamBrowser) {
	t.Helper()
	return queryModelWithCopybook(t, "01 REC.\n 05 NAME PIC X(3).\n", pages)
}

func queryModelWithCopybook(t *testing.T, copybook string, pages map[int64]zosmf.RecordPage) (*Model, *fakeStreamBrowser) {
	t.Helper()
	browser := &fakeStreamBrowser{}
	browser.listDataSets = func(context.Context, zosmf.ListDataSetsRequest) (zosmf.DataSetPage, error) {
		return zosmf.DataSetPage{Items: []zosmf.DataSet{{Name: "A.CUSTOMER.DATA", Organization: "PS"}}}, nil
	}
	browser.readRecords = func(_ context.Context, request zosmf.ReadRecordsRequest) (zosmf.RecordPage, error) {
		return pages[request.Start], nil
	}
	browser.stream = func(context.Context, string) (io.ReadCloser, error) {
		starts := make([]int64, 0, len(pages))
		for start := range pages {
			starts = append(starts, start)
		}
		slices.Sort(starts)
		var buffer bytes.Buffer
		for _, start := range starts {
			for _, record := range pages[start].Records {
				var header [4]byte
				binary.BigEndian.PutUint32(header[:], uint32(len(record.Data)))
				buffer.Write(header[:])
				buffer.Write(record.Data)
			}
		}
		return io.NopCloser(&buffer), nil
	}
	model, err := NewModel(Options{Prefix: "A*", Codepage: "latin1", Copybook: "cust.cpy"}, Dependencies{
		LoadSession: func(context.Context, string) (Session, error) {
			return Session{Browser: browser, User: "A", Encoding: "latin1"}, nil
		},
		LoadFile: func(context.Context, string) ([]byte, error) {
			return []byte(copybook), nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	applyMessage(t, model, tea.WindowSizeMsg{Width: 90, Height: 13})
	executeCommand(t, model, model.Init())
	executeCommand(t, model, model.openSelection())
	if model.screen != ScreenRecords {
		t.Fatalf("screen = %d, want records", model.screen)
	}
	if model.overlay == nil {
		t.Fatalf("overlay not applied: %q", model.overlayError)
	}
	return model, browser
}

func singleRecordPage(records ...zosmf.Record) map[int64]zosmf.RecordPage {
	return map[int64]zosmf.RecordPage{0: {Records: records}}
}

func openQuery(t *testing.T, model *Model) {
	t.Helper()
	executeQuery(t, model, applyMessage(t, model, keyPress(':', ":")))
	if model.query == nil {
		t.Fatal("query popup did not open")
	}
}

func runQueryExpr(t *testing.T, model *Model, expr string) {
	t.Helper()
	model.query.input.SetValue(expr)
	executeQuery(t, model, applyMessage(t, model, keyPress(tea.KeyEnter, "")))
}

func TestQueryEvaluatesWholeDataSetAsOneArray(t *testing.T) {
	model, _ := queryModel(t, singleRecordPage(
		zosmf.Record{Number: 1, Data: []byte("ABC")},
		zosmf.Record{Number: 2, Data: []byte("XYZ")},
	))
	openQuery(t, model)
	runQueryExpr(t, model, ".[].NAME")

	popup := model.query
	if !popup.done || popup.running {
		t.Fatalf("query not finished: done=%v running=%v err=%q", popup.done, popup.running, popup.err)
	}
	if popup.searched != 2 || popup.matches != 2 {
		t.Fatalf("searched=%d matches=%d, want 2/2", popup.searched, popup.matches)
	}
	if len(popup.lines) != 2 || popup.lines[0] != `"ABC"` || popup.lines[1] != `"XYZ"` {
		t.Fatalf("lines = %#v", popup.lines)
	}
}

func TestQueryCompileErrorRendersInPopup(t *testing.T) {
	model, _ := queryModel(t, singleRecordPage(zosmf.Record{Number: 1, Data: []byte("ABC")}))
	openQuery(t, model)
	runQueryExpr(t, model, ".NAME | select(")

	popup := model.query
	if popup == nil {
		t.Fatal("popup closed on compile error")
	}
	if popup.err == "" || popup.running {
		t.Fatalf("compile error not surfaced: err=%q running=%v", popup.err, popup.running)
	}
}

func TestQueryRequiresOverlay(t *testing.T) {
	model, _ := queryModel(t, singleRecordPage(zosmf.Record{Number: 1, Data: []byte("ABC")}))
	executeQuery(t, model, applyMessage(t, model, keyPress('x', "x"))) // clear overlay
	openQuery(t, model)
	if !strings.Contains(model.query.err, "copybook") {
		t.Fatalf("missing overlay explanation, err = %q", model.query.err)
	}
	runQueryExpr(t, model, ".NAME")
	if model.query.running || model.query.searched != 0 {
		t.Fatalf("search ran without an overlay: %+v", model.query)
	}
}

func TestQueryCoversRecordsBeyondTheBrowseCache(t *testing.T) {
	// The first page is deep enough (14 = the row budget at 90×12) that the
	// browse prefetch is not triggered; the query still sees records 15-16
	// because the bulk download always covers the whole data set.
	first := make([]zosmf.Record, 14)
	for i := range first {
		first[i] = zosmf.Record{Number: int64(i + 1), Data: []byte("AAA")}
	}
	pages := map[int64]zosmf.RecordPage{
		0:  {Records: first, MoreRows: true},
		14: {Records: []zosmf.Record{{Number: 15, Data: []byte("CCC")}, {Number: 16, Data: []byte("ABC")}}},
	}
	model, browser := queryModel(t, pages)
	if len(model.records) != 14 || !model.recordPage.more {
		t.Fatalf("precondition: cached=%d more=%v", len(model.records), model.recordPage.more)
	}
	fetchesBefore := len(browser.recordRequests)

	openQuery(t, model)
	runQueryExpr(t, model, `.[] | select(.NAME == "ABC") | .NAME`)

	popup := model.query
	if !popup.done {
		t.Fatalf("query did not finish: %+v err=%q", popup, popup.err)
	}
	if popup.searched != 16 {
		t.Fatalf("searched = %d, want 16 (whole data set)", popup.searched)
	}
	if len(browser.recordRequests) != fetchesBefore {
		t.Fatal("query paged through browse fetches instead of the download")
	}
	if len(browser.streamRequests) != 1 {
		t.Fatalf("stream requests = %v, want one bulk download", browser.streamRequests)
	}
	if len(popup.lines) != 1 || popup.lines[0] != `"ABC"` {
		t.Fatalf("lines = %#v", popup.lines)
	}
}

func TestQueryCancelStopsSearchAndIgnoresStaleResults(t *testing.T) {
	model, _ := queryModel(t, singleRecordPage(zosmf.Record{Number: 1, Data: []byte("ABC")}))
	openQuery(t, model)
	model.query.input.SetValue(".NAME")
	pending := applyMessage(t, model, keyPress(tea.KeyEnter, ""))
	if !model.query.running {
		t.Fatal("search not running after enter")
	}

	// esc cancels the running search before the evaluation command resolves.
	executeQuery(t, model, applyMessage(t, model, keyPress(tea.KeyEscape, "")))
	popup := model.query
	if popup == nil || popup.running || !popup.cancelled {
		t.Fatalf("cancel did not stop search: %+v", popup)
	}

	// The stale in-flight evaluation result must not resurrect the search.
	executeQuery(t, model, pending)
	if popup.searched != 0 || popup.running {
		t.Fatalf("stale result applied: searched=%d running=%v", popup.searched, popup.running)
	}

	// A second esc closes the popup.
	executeQuery(t, model, applyMessage(t, model, keyPress(tea.KeyEscape, "")))
	if model.query != nil {
		t.Fatal("popup still open after esc")
	}
}

func TestQueryResultCapStopsSearch(t *testing.T) {
	original := queryResultCap
	queryResultCap = 500
	defer func() { queryResultCap = original }()

	model, _ := queryModel(t, singleRecordPage(
		zosmf.Record{Number: 1, Data: []byte("ABC")},
		zosmf.Record{Number: 2, Data: []byte("XYZ")},
	))
	openQuery(t, model)
	runQueryExpr(t, model, "range(600) | tostring")

	popup := model.query
	if !popup.done || popup.running {
		t.Fatalf("query not finished: done=%v running=%v", popup.done, popup.running)
	}
	if len(popup.lines) != queryResultCap {
		t.Fatalf("retained lines = %d, want cap %d", len(popup.lines), queryResultCap)
	}
	if popup.matches != 600 {
		t.Fatalf("matches = %d, want 600 (counted past the cap)", popup.matches)
	}
	if footer := model.queryFooter(); !strings.Contains(footer, "first 500 shown") {
		t.Fatalf("footer missing cap note: %q", footer)
	}
}

func TestQueryCopyIncludesAllResultsPastThePreviousDefaultCap(t *testing.T) {
	model, _ := queryModel(t, singleRecordPage(
		zosmf.Record{Number: 1, Data: []byte("ABC")},
		zosmf.Record{Number: 2, Data: []byte("XYZ")},
	))
	openQuery(t, model)
	runQueryExpr(t, model, "range(600) | tostring")

	popup := model.query
	if popup.matches != 600 || len(popup.lines) != 600 {
		t.Fatalf("matches=%d retained=%d, want all 600 retained under the default cap", popup.matches, len(popup.lines))
	}
	if footer := model.queryFooter(); strings.Contains(footer, "shown") {
		t.Fatalf("footer reported a cap that should not apply: %q", footer)
	}

	if cmd := applyMessage(t, model, tea.KeyPressMsg(tea.Key{Code: 'y', Mod: tea.ModCtrl})); cmd == nil {
		t.Fatal("copy dispatched no clipboard command")
	}
	if !strings.Contains(popup.notice, "copied 600 lines") {
		t.Fatalf("copy notice = %q, want all 600 lines copied", popup.notice)
	}
}

func TestQuerySkipsDecodeErrorRecords(t *testing.T) {
	model, _ := queryModel(t, singleRecordPage(
		zosmf.Record{Number: 1, Data: []byte("AB")}, // too short for PIC X(3)
		zosmf.Record{Number: 2, Data: []byte("ABC")},
	))
	openQuery(t, model)
	runQueryExpr(t, model, ".[].NAME")

	popup := model.query
	if !popup.done {
		t.Fatalf("query did not finish: %+v", popup)
	}
	if popup.errored != 1 || popup.matches != 1 {
		t.Fatalf("errored=%d matches=%d, want 1/1", popup.errored, popup.matches)
	}
	if len(popup.lines) != 1 || popup.lines[0] != `"ABC"` {
		t.Fatalf("lines = %#v", popup.lines)
	}
	if footer := model.queryFooter(); !strings.Contains(footer, "1 records skipped") {
		t.Fatalf("footer missing skip count: %q", footer)
	}
}

func TestQueryKeyRouting(t *testing.T) {
	model, _ := queryModel(t, singleRecordPage(zosmf.Record{Number: 1, Data: []byte("ABC")}))

	// ':' does nothing outside the records screen.
	executeQuery(t, model, applyMessage(t, model, keyPress(tea.KeyEscape, "")))
	if model.screen != ScreenDataSets {
		t.Fatalf("screen = %d, want data sets", model.screen)
	}
	applyMessage(t, model, keyPress(':', ":"))
	if model.query != nil {
		t.Fatal("popup opened outside the records screen")
	}
	executeCommand(t, model, model.openSelection())

	openQuery(t, model)
	// Typing lands in the input, including keys bound elsewhere.
	for _, press := range []tea.KeyPressMsg{keyPress('q', "q"), keyPress('?', "?"), keyPress('/', "/")} {
		executeQuery(t, model, applyMessage(t, model, press))
	}
	if got := model.query.input.Value(); got != "q?/" {
		t.Fatalf("input = %q, want typed characters", got)
	}
	if model.showHelp {
		t.Fatal("? toggled help while popup open")
	}

	// pgup/pgdn scroll results instead of paging records.
	model.query.lines = make([]string, 40)
	applyMessage(t, model, keyPress(tea.KeyPgDown, ""))
	if model.query.scroll == 0 {
		t.Fatal("pgdn did not scroll results")
	}
	applyMessage(t, model, keyPress(tea.KeyPgUp, ""))
	if model.query.scroll != 0 {
		t.Fatalf("pgup scroll = %d, want 0", model.query.scroll)
	}
}

func TestQueryAggregatesOverTheWholeArray(t *testing.T) {
	model, _ := queryModel(t, singleRecordPage(
		zosmf.Record{Number: 1, Data: []byte("XYZ")},
		zosmf.Record{Number: 2, Data: []byte("ABC")},
		zosmf.Record{Number: 3, Data: []byte("ABC")},
	))
	openQuery(t, model)
	// map/unique see the whole data set as one array directly.
	runQueryExpr(t, model, "map(.NAME) | unique")

	popup := model.query
	if !popup.done || popup.running {
		t.Fatalf("query not finished: done=%v running=%v err=%q lastErr=%q", popup.done, popup.running, popup.err, popup.lastErr)
	}
	if len(popup.lines) != 1 || popup.lines[0] != `["ABC","XYZ"]` {
		t.Fatalf("lines = %#v", popup.lines)
	}
	if popup.errored != 0 || popup.lastErr != "" {
		t.Fatalf("errors left visible: errored=%d lastErr=%q", popup.errored, popup.lastErr)
	}
}

func TestNormalizeQueryExpressionSingleQuotes(t *testing.T) {
	tests := []struct{ in, want string }{
		{`.NAME == 'ABC'`, `.NAME == "ABC"`},
		{`select(.A=='x "y" \z')`, `select(.A=="x \"y\" \\z")`},
		{`."CUST-NAME" | test("'")`, `."CUST-NAME" | test("'")`},
		{`.A`, `.A`},
	}
	for _, test := range tests {
		got, err := normalizeQueryExpression(test.in)
		if err != nil || got != test.want {
			t.Fatalf("normalize(%q) = %q, %v; want %q", test.in, got, err, test.want)
		}
	}
	if _, err := normalizeQueryExpression(`.A == 'oops`); err == nil {
		t.Fatal("unbalanced single quote not rejected")
	}
}

func TestQuerySingleQuoteExpressionRuns(t *testing.T) {
	model, _ := queryModel(t, singleRecordPage(
		zosmf.Record{Number: 1, Data: []byte("ABC")},
		zosmf.Record{Number: 2, Data: []byte("XYZ")},
	))
	openQuery(t, model)
	runQueryExpr(t, model, `.[] | select(.NAME == 'ABC') | .NAME`)
	popup := model.query
	if popup.err != "" || popup.matches != 1 {
		t.Fatalf("err=%q matches=%d", popup.err, popup.matches)
	}
}

func TestFormatElapsedGranularity(t *testing.T) {
	tests := []struct {
		d    time.Duration
		want string
	}{
		{123 * time.Millisecond, "123ms"},
		{1400 * time.Millisecond, "1.40s"},
		{12300 * time.Millisecond, "12.3s"},
		{67 * time.Second, "1:07"},
	}
	for _, test := range tests {
		if got := formatElapsed(test.d); got != test.want {
			t.Fatalf("formatElapsed(%v) = %q, want %q", test.d, got, test.want)
		}
	}
}

func TestJQFieldRefQuotesSegmentsWithDashes(t *testing.T) {
	tests := []struct {
		parts []string
		want  string
	}{
		{[]string{"NAME"}, ".NAME"},
		{[]string{"CUST-TYPE"}, `."CUST-TYPE"`},
		{[]string{"CUST-GRP", "NAME"}, `."CUST-GRP".NAME`},
		{[]string{"A_1", "B-2"}, `.A_1."B-2"`},
	}
	for _, test := range tests {
		if got := jqFieldRef(test.parts); got != test.want {
			t.Fatalf("jqFieldRef(%v) = %q, want %q", test.parts, got, test.want)
		}
	}
}

func TestFieldTokenAtFindsThePartialReference(t *testing.T) {
	tests := []struct {
		value string
		start int
		token string
	}{
		{"select(.CUS", 7, "CUS"},
		{`select(."CUS`, 7, "CUS"},
		{".NA", 0, "NA"},
		{"CUS", 0, "CUS"},
		{"select(", 7, ""},
		{"", 0, ""},
	}
	for _, test := range tests {
		start, token := fieldTokenAt([]rune(test.value), len([]rune(test.value)))
		if start != test.start || token != test.token {
			t.Fatalf("fieldTokenAt(%q) = %d, %q, want %d, %q", test.value, start, token, test.start, test.token)
		}
	}
}

const dashedCopybook = "01 REC.\n 05 CUST-GRP.\n  10 CUST-TYPE PIC X(1).\n  10 NAME PIC X(2).\n"

func ctrlSpace() tea.KeyPressMsg {
	return tea.KeyPressMsg(tea.Key{Code: tea.KeySpace, Mod: tea.ModCtrl})
}

func TestQueryCompletionListNavigatesAndInsertsQuotedField(t *testing.T) {
	model, _ := queryModelWithCopybook(t, dashedCopybook, singleRecordPage(zosmf.Record{Number: 1, Data: []byte("ABC")}))
	openQuery(t, model)
	model.query.input.SetValue("select(")
	model.query.input.CursorEnd()

	applyMessage(t, model, ctrlSpace())
	comp := model.query.comp
	if comp == nil || len(comp.filtered) != 2 {
		t.Fatalf("completion = %#v, want two candidates", comp)
	}
	applyMessage(t, model, keyPress(tea.KeyDown, ""))
	if model.query.comp.selected != 1 {
		t.Fatalf("selected = %d, want 1", model.query.comp.selected)
	}
	applyMessage(t, model, keyPress(tea.KeyEnter, ""))
	if model.query.comp != nil {
		t.Fatal("completion list still open after insert")
	}
	if got := model.query.input.Value(); got != `select(."CUST-GRP".NAME` {
		t.Fatalf("value = %q", got)
	}
	if model.query.running {
		t.Fatal("insert must not run the query")
	}
}

func TestQueryCompletionSingleMatchInsertsImmediately(t *testing.T) {
	model, _ := queryModelWithCopybook(t, dashedCopybook, singleRecordPage(zosmf.Record{Number: 1, Data: []byte("ABC")}))
	openQuery(t, model)
	model.query.input.SetValue(".NA")
	model.query.input.CursorEnd()

	applyMessage(t, model, ctrlSpace())
	if model.query.comp != nil {
		t.Fatal("single candidate should insert without a list")
	}
	if got := model.query.input.Value(); got != `."CUST-GRP".NAME` {
		t.Fatalf("value = %q", got)
	}
}

func TestQueryCompletionFiltersWhileTypingAndCancels(t *testing.T) {
	model, _ := queryModelWithCopybook(t, dashedCopybook, singleRecordPage(zosmf.Record{Number: 1, Data: []byte("ABC")}))
	openQuery(t, model)

	applyMessage(t, model, ctrlSpace())
	if comp := model.query.comp; comp == nil || len(comp.filtered) != 2 {
		t.Fatalf("completion = %#v, want two candidates", comp)
	}
	applyMessage(t, model, keyPress('T', "T"))
	applyMessage(t, model, keyPress('Y', "Y"))
	if comp := model.query.comp; comp == nil || len(comp.filtered) != 1 || comp.filtered[0].label != "CUST-GRP.CUST-TYPE" {
		t.Fatalf("filtered = %#v, want CUST-GRP.CUST-TYPE only", comp)
	}
	applyMessage(t, model, keyPress(tea.KeyEscape, ""))
	if model.query.comp != nil {
		t.Fatal("esc should close the completion list")
	}
	if model.query == nil {
		t.Fatal("esc with an open list must not close the popup")
	}
}

func TestQueryPopupKeepsTableHeaderVisible(t *testing.T) {
	model, _ := queryModelWithCopybook(t, dashedCopybook, singleRecordPage(zosmf.Record{Number: 1, Data: []byte("ABC")}))
	applyMessage(t, model, tea.WindowSizeMsg{Width: 90, Height: 24})
	openQuery(t, model)

	lines := strings.Split(model.overlayQuery(model.mainView()), "\n")
	if len(lines) < 6 {
		t.Fatalf("composited view has %d lines", len(lines))
	}
	header := lines[3]
	if !strings.Contains(header, "CUST-TYPE") || !strings.Contains(header, "RECORD") {
		t.Fatalf("table header row not visible above the popup: %q", header)
	}
	if !strings.Contains(lines[4], "─") {
		t.Fatalf("popup border expected on the row below the header: %q", lines[4])
	}
}

func TestQueryPlaceholderBuiltFromOverlayFields(t *testing.T) {
	model, _ := queryModelWithCopybook(t, dashedCopybook, singleRecordPage(zosmf.Record{Number: 1, Data: []byte("ABC")}))
	openQuery(t, model)

	placeholder := model.query.input.Placeholder
	if placeholder == "" || strings.Contains(placeholder, "{f") {
		t.Fatalf("placeholder = %q", placeholder)
	}
	if strings.Contains(placeholder, ".FIELD") {
		t.Fatalf("placeholder still the static template: %q", placeholder)
	}
	// Every generated example references real overlay fields in quoted jq
	// form, except the bare `length` aggregate.
	for range 100 {
		example := examplePlaceholder(model.query.fields)
		if example != "length" && !strings.Contains(example, `."CUST-GRP"`) {
			t.Fatalf("example %q does not reference an overlay field", example)
		}
		if strings.Contains(example, "{f") {
			t.Fatalf("template slot left unfilled: %q", example)
		}
	}
	// Without an overlay the static fallback remains.
	if got := examplePlaceholder(nil); got != `select(.FIELD == "VALUE") | .FIELD` {
		t.Fatalf("fallback placeholder = %q", got)
	}
}

func TestQueryCopyPutsResultsOnClipboardWithFooterNotice(t *testing.T) {
	model, _ := queryModel(t, singleRecordPage(
		zosmf.Record{Number: 1, Data: []byte("ABC")},
		zosmf.Record{Number: 2, Data: []byte("XYZ")},
	))
	openQuery(t, model)

	// Nothing to copy yet: no command, no notice.
	if cmd := applyMessage(t, model, tea.KeyPressMsg(tea.Key{Code: 'y', Mod: tea.ModCtrl})); cmd != nil {
		t.Fatal("copy with no results dispatched a command")
	}
	runQueryExpr(t, model, ".[].NAME")
	cmd := applyMessage(t, model, tea.KeyPressMsg(tea.Key{Code: 'y', Mod: tea.ModCtrl}))
	if cmd == nil {
		t.Fatal("copy dispatched no clipboard command")
	}
	if footer := model.queryFooter(); !strings.Contains(footer, "copied 2 lines") {
		t.Fatalf("footer missing copy notice: %q", footer)
	}
	// A new run clears the stale notice.
	runQueryExpr(t, model, ".[].NAME")
	if footer := model.queryFooter(); strings.Contains(footer, "copied") {
		t.Fatalf("copy notice survived a new run: %q", footer)
	}
}

func TestQueryCompletionRendersAsPopOverLayer(t *testing.T) {
	model, _ := queryModelWithCopybook(t, dashedCopybook, singleRecordPage(zosmf.Record{Number: 1, Data: []byte("ABC")}))
	applyMessage(t, model, tea.WindowSizeMsg{Width: 90, Height: 24})
	openQuery(t, model)

	applyMessage(t, model, ctrlSpace())
	if model.query.comp == nil {
		t.Fatal("completion did not open")
	}
	view := ansi.Strip(model.overlayQuery(model.mainView()))
	lines := strings.Split(view, "\n")
	if len(lines) != model.height {
		t.Fatalf("composited view has %d lines, want %d (pop-over must not displace layout)", len(lines), model.height)
	}
	if !strings.Contains(view, "> CUST-GRP.CUST-TYPE") || !strings.Contains(view, "CUST-GRP.NAME") {
		t.Fatalf("completion candidates not rendered:\n%s", view)
	}
	// Two rounded borders on screen: the popup and the completion pop-over.
	if corners := strings.Count(view, "╭"); corners != 2 {
		t.Fatalf("border corners = %d, want popup + completion layer\n%s", corners, view)
	}
	// The list hangs under the input row (input at index 6: 4 chrome rows,
	// popup border, title), overlapping the results pane.
	if boxTop := strings.Index(view, "╭"); boxTop < 0 {
		t.Fatal("no border found")
	}
}
