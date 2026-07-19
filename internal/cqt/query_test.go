package cqt

import (
	"context"
	"strings"
	"testing"
	"time"

	"charm.land/bubbles/v2/spinner"
	tea "charm.land/bubbletea/v2"

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
// copybook overlay applied, backed by the supplied record pages.
func queryModel(t *testing.T, pages map[int64]zosmf.RecordPage) (*Model, *fakeBrowser) {
	t.Helper()
	browser := &fakeBrowser{
		listDataSets: func(context.Context, zosmf.ListDataSetsRequest) (zosmf.DataSetPage, error) {
			return zosmf.DataSetPage{Items: []zosmf.DataSet{{Name: "A.CUSTOMER.DATA", Organization: "PS"}}}, nil
		},
		readRecords: func(_ context.Context, request zosmf.ReadRecordsRequest) (zosmf.RecordPage, error) {
			return pages[request.Start], nil
		},
	}
	model, err := NewModel(Options{Prefix: "A*", Codepage: "latin1", Copybook: "cust.cpy"}, Dependencies{
		LoadSession: func(context.Context, string) (Session, error) {
			return Session{Browser: browser, User: "A", Encoding: "latin1"}, nil
		},
		LoadFile: func(context.Context, string) ([]byte, error) {
			return []byte("01 REC.\n 05 NAME PIC X(3).\n"), nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	applyMessage(t, model, tea.WindowSizeMsg{Width: 90, Height: 12})
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

func TestQueryStreamsResultsOverCachedRecords(t *testing.T) {
	model, _ := queryModel(t, singleRecordPage(
		zosmf.Record{Number: 1, Data: []byte("ABC")},
		zosmf.Record{Number: 2, Data: []byte("XYZ")},
	))
	openQuery(t, model)
	runQueryExpr(t, model, ".NAME")

	popup := model.query
	if !popup.done || popup.running {
		t.Fatalf("search not finished: done=%v running=%v err=%q", popup.done, popup.running, popup.err)
	}
	if popup.searched != 2 || popup.matches != 2 {
		t.Fatalf("searched=%d matches=%d, want 2/2", popup.searched, popup.matches)
	}
	if len(popup.lines) != 2 || popup.lines[0] != `    1 │ "ABC"` || popup.lines[1] != `    2 │ "XYZ"` {
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

func TestQueryLazyFetchContinuesToEndOfRecords(t *testing.T) {
	// The first page is deep enough (14 = the row budget at 90×12) that the
	// browse prefetch is not triggered, so the remaining records can only be
	// reached by the query's own lazy fetch.
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
	runQueryExpr(t, model, `select(.NAME == "ABC") | .NAME`)

	popup := model.query
	if !popup.done {
		t.Fatalf("search did not reach end of records: %+v err=%q", popup, popup.err)
	}
	if popup.searched != 16 {
		t.Fatalf("searched = %d, want 16 (lazy fetch to EOF)", popup.searched)
	}
	if len(browser.recordRequests) <= fetchesBefore {
		t.Fatal("no forward page was fetched for the search")
	}
	if len(popup.lines) != 1 || popup.lines[0] != `   16 │ "ABC"` {
		t.Fatalf("lines = %#v", popup.lines)
	}
	if len(model.records) != 16 {
		t.Fatalf("workspace cache = %d records, want 16 (shared cache)", len(model.records))
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
	model, _ := queryModel(t, singleRecordPage(
		zosmf.Record{Number: 1, Data: []byte("ABC")},
		zosmf.Record{Number: 2, Data: []byte("XYZ")},
	))
	openQuery(t, model)
	runQueryExpr(t, model, "range(600) | tostring")

	popup := model.query
	if !popup.capped || popup.running {
		t.Fatalf("cap did not stop search: capped=%v running=%v", popup.capped, popup.running)
	}
	if len(popup.lines) != queryResultCap {
		t.Fatalf("retained lines = %d, want cap %d", len(popup.lines), queryResultCap)
	}
	if popup.matches != 600 {
		t.Fatalf("matches = %d, want 600 (counted past the cap)", popup.matches)
	}
	if popup.searched != 1 {
		t.Fatalf("searched = %d, want 1 (second record not evaluated)", popup.searched)
	}
}

func TestQuerySkipsDecodeErrorRecords(t *testing.T) {
	model, _ := queryModel(t, singleRecordPage(
		zosmf.Record{Number: 1, Data: []byte("AB")}, // too short for PIC X(3)
		zosmf.Record{Number: 2, Data: []byte("ABC")},
	))
	openQuery(t, model)
	runQueryExpr(t, model, ".NAME")

	popup := model.query
	if !popup.done {
		t.Fatalf("search did not finish: %+v", popup)
	}
	if popup.errored != 1 || popup.matches != 1 {
		t.Fatalf("errored=%d matches=%d, want 1/1", popup.errored, popup.matches)
	}
	if len(popup.lines) != 1 || popup.lines[0] != `    2 │ "ABC"` {
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

func TestQueryFallsBackToArrayMode(t *testing.T) {
	model, _ := queryModel(t, singleRecordPage(
		zosmf.Record{Number: 1, Data: []byte("XYZ")},
		zosmf.Record{Number: 2, Data: []byte("ABC")},
		zosmf.Record{Number: 3, Data: []byte("ABC")},
	))
	openQuery(t, model)
	// map/unique need the whole data set as one array; per-record evaluation
	// errors on every record with zero matches, triggering the fallback.
	runQueryExpr(t, model, "map(.NAME) | unique")

	popup := model.query
	if !popup.done || popup.running {
		t.Fatalf("search not finished: done=%v running=%v err=%q lastErr=%q", popup.done, popup.running, popup.err, popup.lastErr)
	}
	if !popup.arrayMode {
		t.Fatal("array-mode fallback did not run")
	}
	if len(popup.lines) != 1 || popup.lines[0] != `["ABC","XYZ"]` {
		t.Fatalf("lines = %#v", popup.lines)
	}
	if popup.errored != 0 || popup.lastErr != "" {
		t.Fatalf("fallback left errors visible: errored=%d lastErr=%q", popup.errored, popup.lastErr)
	}
}

func TestQueryStreamingStillWinsWhenItMatches(t *testing.T) {
	model, _ := queryModel(t, singleRecordPage(
		zosmf.Record{Number: 1, Data: []byte("ABC")},
	))
	openQuery(t, model)
	runQueryExpr(t, model, ".NAME")
	if model.query.arrayMode {
		t.Fatal("array fallback ran despite streaming matches")
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
	runQueryExpr(t, model, `select(.NAME == 'ABC') | .NAME`)
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
