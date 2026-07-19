package cqt

import (
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"testing"

	"charm.land/bubbles/v2/help"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/Tannex/cq/internal/decode"
	"github.com/Tannex/cq/internal/dsnmap"
	"github.com/Tannex/cq/internal/record"
	"github.com/Tannex/cq/internal/zosmf"
)

func recordViewModel(t *testing.T) *Model {
	t.Helper()
	cm, err := decode.Codepage("latin1")
	if err != nil {
		t.Fatal(err)
	}
	model := &Model{
		width:   80,
		height:  12,
		visible: VisibleRows(80, 12, false),
		budget:  RowBudget(VisibleRows(80, 12, false)),
		keys:    DefaultKeyMap(),
		help:    helpModelForTest(),
		spinner: newStatusSpinner(),
		workspace: workspace{
			screen:       ScreenRecords,
			dataSet:      zosmf.DataSet{Name: "HQ.DATA", Organization: "PS"},
			codepageName: "latin1",
			charmap:      cm,
			status:       status{Level: statusReady, Text: "records ready"},
		},
	}
	model.records = []recordRow{
		{Record: zosmf.Record{Number: 1, Data: []byte{'A', 0, '\n', 'B'}}},
		{Record: zosmf.Record{Number: 2, Data: []byte("SECOND")}},
	}
	model.recordPage.reset(model.visible, model.budget)
	model.recordPage.apply([]string{"1", "2"}, false, model.recordPage.initialPlan(0))
	return model
}

func recordWindowModel(t *testing.T, visible, count int) *Model {
	t.Helper()
	model := recordViewModel(t)
	model.visible = visible
	model.budget = max(count, RowBudget(visible))
	model.records = make([]recordRow, count)
	keys := make([]string, count)
	for i := range model.records {
		number := int64(101 + i)
		keys[i] = strconv.FormatInt(number, 10)
		model.records[i] = recordRow{Record: zosmf.Record{Number: number, Data: []byte(fmt.Sprintf("ROW%02d", i))}}
	}
	model.recordPage.reset(model.visible, model.budget)
	model.recordPage.apply(keys, false, model.recordPage.initialPlan(0))
	return model
}

func helpModelForTest() help.Model {
	return help.New()
}

func TestRawRecordViewUsesFixedGutterAndVisibleControlMarkers(t *testing.T) {
	model := recordViewModel(t)
	model.recordMode = ModeRaw
	content := model.View().Content
	if !strings.Contains(content, "> 00000001 │") {
		t.Fatalf("raw view missing selected fixed gutter: %q", content)
	}
	if !strings.Contains(content, "A··B") {
		t.Fatalf("raw view did not replace LOW-VALUE/control bytes: %q", content)
	}
	if lines := strings.Count(content, "\n") + 1; lines != model.height {
		t.Fatalf("view lines = %d, want terminal height %d", lines, model.height)
	}
}

func TestEndMarkerBlankLineAppearsOnlyAtKnownEnd(t *testing.T) {
	model := recordWindowModel(t, 4, 8)
	model.recordMode = ModeRaw

	// Away from the end the window shows a full four rows.
	if content := ansi.Strip(model.rawRecordView()); !strings.Contains(content, "00000101 │") {
		t.Fatalf("full window did not start at the first row:\n%s", content)
	}

	// At the known end the oldest visible row yields to a trailing blank line.
	model.recordPage.bottom()
	content := ansi.Strip(model.rawRecordView())
	if strings.Contains(content, "00000105 │") || !strings.Contains(content, "00000106 │") {
		t.Fatalf("known end did not reserve a trailing blank line:\n%s", content)
	}
	lines := strings.Split(content, "\n")
	if last := strings.TrimSpace(lines[len(lines)-1]); last != "" {
		t.Fatalf("line after the last row is not blank: %q", last)
	}

	// With more rows available the full window returns.
	model.recordPage.more = true
	if content := ansi.Strip(model.rawRecordView()); !strings.Contains(content, "00000105 │") {
		t.Fatalf("blank end line appeared while more records were available:\n%s", content)
	}
}

func TestEndMarkerSkippedWhenListShorterThanWindow(t *testing.T) {
	model := recordWindowModel(t, 6, 3)
	model.recordMode = ModeRaw
	model.recordPage.bottom()
	content := ansi.Strip(model.rawRecordView())
	for _, want := range []string{"00000101 │", "00000102 │", "00000103 │"} {
		if !strings.Contains(content, want) {
			t.Fatalf("short list dropped row %q:\n%s", want, content)
		}
	}
}

func TestViewEnablesCellMotionMouseReporting(t *testing.T) {
	model := recordViewModel(t)
	if mode := model.View().MouseMode; mode != tea.MouseModeCellMotion {
		t.Fatalf("mouse mode = %v, want cell motion", mode)
	}
}

func TestRecordGutterStaysFixedForLargeRecordNumbers(t *testing.T) {
	model := recordViewModel(t)
	model.records[0].Record.Number = 9_000_000_001
	model.records[1].Record.Number = 9_000_000_002
	model.recordPage.apply([]string{"9000000001", "9000000002"}, false, model.recordPage.initialPlan(9_000_000_000))
	content := model.View().Content
	if !strings.Contains(content, "> 9000000001 │") || !strings.Contains(content, "  9000000002 │") {
		t.Fatalf("large record gutters are not fixed-width: %q", content)
	}
}

func TestRecordTableAndJSONShareTypedValueAndShowDiagnostics(t *testing.T) {
	model := recordViewModel(t)
	decoded := record.DecodedRecord{
		Value: record.Object{
			{Name: "NAME", Value: "ALICE"},
			{Name: "AMOUNT", Value: json.Number("123456789012345678.90")},
		},
		Diagnostics: []record.Diagnostic{{FieldPath: "AMOUNT", Offset: 5, Length: 20, RawHex: "F1", Err: errors.New("invalid scalar")}},
	}
	model.records[0].Decoded = &decoded
	model.records[1].Err = errors.New("record is short")
	model.overlay = &overlay{
		Source: CopybookSource{Local: "customer.cpy"},
		Columns: []fieldColumn{
			{Path: "NAME", Parts: []string{"NAME"}, Width: 10},
			{Path: "AMOUNT", Parts: []string{"AMOUNT"}, Width: 24},
		},
	}
	model.recordMode = ModeTable
	tableContent := model.View().Content
	if !strings.Contains(tableContent, "ALICE") || !strings.Contains(tableContent, "! 123456789012345678.90") {
		t.Fatalf("table did not render typed values and diagnostic marker: %q", tableContent)
	}
	if !strings.Contains(tableContent, "! SECOND") {
		t.Fatalf("structural error row did not fall back to raw data: %q", tableContent)
	}

	pointer := model.records[0].Decoded
	model.recordMode = ModeJSON
	jsonContent := model.View().Content
	if model.records[0].Decoded != pointer || !strings.Contains(jsonContent, `"AMOUNT": 123456789012345678.90`) {
		t.Fatalf("JSON view lost shared exact typed value: %q", jsonContent)
	}
	if !strings.Contains(jsonContent, "j/k record  pgup/pgdn scroll") {
		t.Fatalf("JSON view missing navigation hint: %q", jsonContent)
	}
	model.showDiagnostics = true
	status := model.statusLine()
	if !strings.Contains(status, "field AMOUNT") || !strings.Contains(status, "offset 5") {
		t.Fatalf("selected-cell diagnostic detail = %q", status)
	}
}

func TestStatusSpinnerSlotKeepsStatusLabelAligned(t *testing.T) {
	model := recordViewModel(t)
	model.status = status{Level: statusReady, Text: "records ready"}
	ready := ansi.Strip(model.statusLine())
	readyIndex := strings.Index(ready, "READY")

	model.status = status{Level: statusLoading, Text: "reading records"}
	loading := ansi.Strip(model.statusLine())
	loadingIndex := strings.Index(loading, "LOADING")
	if readyIndex < 0 || loadingIndex < 0 {
		t.Fatalf("status labels missing: ready=%q loading=%q", ready, loading)
	}
	readyColumn := lipgloss.Width(ready[:readyIndex])
	loadingColumn := lipgloss.Width(loading[:loadingIndex])
	if readyColumn != loadingColumn {
		t.Fatalf("status label columns moved: ready=%d %q loading=%d %q", readyColumn, ready, loadingColumn, loading)
	}
	if frame := model.spinner.View(); frame == "" || !strings.Contains(loading, frame) {
		t.Fatalf("loading status missing spinner frame %q: %q", frame, loading)
	}
}

func TestSpinnerTicksOnlyWhileLoading(t *testing.T) {
	model := recordViewModel(t)
	model.status = status{Level: statusLoading, Text: "reading records"}
	_, command := model.Update(model.spinner.Tick())
	if command == nil {
		t.Fatal("loading spinner tick did not schedule the next frame")
	}
	model.status = status{Level: statusReady, Text: "records ready"}
	_, command = model.Update(model.spinner.Tick())
	if command != nil {
		t.Fatal("ready status kept the spinner tick chain alive")
	}
}

func TestSelectedTableRowRemainsAboveStatusLine(t *testing.T) {
	model := recordViewModel(t)
	model.recordMode = ModeTable
	model.overlay = &overlay{Columns: []fieldColumn{{Path: "FIELD", Parts: []string{"FIELD"}, Width: 20}}}
	model.records = make([]recordRow, 12)
	keys := make([]string, len(model.records))
	for i := range model.records {
		number := int64(i + 1)
		keys[i] = strconv.FormatInt(number, 10)
		model.records[i] = recordRow{Record: zosmf.Record{Number: number, Data: []byte("VALUE")}}
	}
	model.recordPage.reset(model.visible, model.budget)
	model.recordPage.apply(keys, false, model.recordPage.initialPlan(0))
	model.recordPage.bottom()

	content := ansi.Strip(model.View().Content)
	selected := strings.Index(content, "> 00000012 │")
	status := strings.Index(content, "READY")
	if selected < 0 || status < 0 || selected >= status {
		t.Fatalf("selected row disappeared behind status: selected=%d status=%d\n%s", selected, status, content)
	}
	if !strings.Contains(content, "  00000007 │") || strings.Contains(content, "00000006 │") {
		t.Fatalf("table rendered the wrong end-marker window:\n%s", content)
	}
}

func TestBrowseViewsRenderPersistentWindow(t *testing.T) {
	t.Run("data sets", func(t *testing.T) {
		model := recordViewModel(t)
		model.visible = 5
		model.budget = 24
		model.datasets = make([]zosmf.DataSet, 12)
		keys := make([]string, len(model.datasets))
		for i := range model.datasets {
			name := fmt.Sprintf("DATASET.%02d", i)
			model.datasets[i] = zosmf.DataSet{Name: name, Organization: "PS"}
			keys[i] = name
		}
		model.datasetPage.reset(model.visible, model.budget)
		model.datasetPage.apply(keys, false, model.datasetPage.initialPlan(""))
		model.datasetPage.bottom()
		model.datasetPage.move(-2)

		assertRenderedWindow(t, model.dataSetTableView(), "DATASET.07", "DATASET.11", "DATASET.06", "DATASET.09", 5, 2)
	})

	t.Run("members", func(t *testing.T) {
		model := recordViewModel(t)
		model.visible = 5
		model.budget = 24
		model.members = make([]zosmf.Member, 12)
		keys := make([]string, len(model.members))
		for i := range model.members {
			name := fmt.Sprintf("MEMBER%02d", i)
			model.members[i] = zosmf.Member{Name: name}
			keys[i] = name
		}
		model.memberPage.reset(model.visible, model.budget)
		model.memberPage.apply(keys, false, model.memberPage.initialPlan(""))
		model.memberPage.bottom()
		model.memberPage.move(-2)

		assertRenderedWindow(t, model.memberTableView(), "MEMBER07", "MEMBER11", "MEMBER06", "MEMBER09", 5, 2)
	})

	t.Run("raw records", func(t *testing.T) {
		model := recordWindowModel(t, 5, 12)
		model.recordPage.bottom()
		model.recordPage.move(-2)

		assertRenderedWindow(t, model.rawRecordView(), "00000108", "00000112", "00000107", "00000110", 5, 2)
	})

	t.Run("copybook table", func(t *testing.T) {
		model := recordWindowModel(t, 5, 12)
		model.overlay = &overlay{Columns: []fieldColumn{{Path: "FIELD", Parts: []string{"FIELD"}, Width: 20}}}
		model.recordPage.bottom()
		model.recordPage.move(-2)

		assertRenderedWindow(t, model.recordTableView(), "00000108", "00000112", "00000107", "00000110", 5, 2)
	})
}

func assertRenderedWindow(t *testing.T, content, first, last, hidden, selected string, visible, cursorOffset int) {
	t.Helper()
	lines := strings.Split(ansi.Strip(content), "\n")
	if len(lines) != visible+1 {
		t.Fatalf("rendered lines = %d, want %d:\n%s", len(lines), visible+1, strings.Join(lines, "\n"))
	}
	if !strings.Contains(lines[1], first) || !strings.Contains(lines[visible], last) {
		t.Fatalf("rendered range does not start/end with %q/%q:\n%s", first, last, strings.Join(lines, "\n"))
	}
	if strings.Contains(strings.Join(lines, "\n"), hidden) {
		t.Fatalf("rendered hidden row %q:\n%s", hidden, strings.Join(lines, "\n"))
	}
	selectedLine := 1 + cursorOffset
	if !strings.Contains(lines[selectedLine], selected) || !strings.Contains(lines[selectedLine], ">") {
		t.Fatalf("cursor line %d does not select %q:\n%s", selectedLine, selected, strings.Join(lines, "\n"))
	}
}

func TestDiagnosticsDoNotHideLoadingOrErrorStatus(t *testing.T) {
	model := recordViewModel(t)
	model.showDiagnostics = true
	model.records[0].Err = errors.New("row decode failed")

	model.status = status{Level: statusLoading, Text: "reading records"}
	if line := model.statusLine(); !strings.Contains(line, "LOADING") || !strings.Contains(line, "reading records") {
		t.Fatalf("diagnostic hid loading status: %q", line)
	}
	model.status = status{Level: statusError, Text: "browse failed"}
	if line := model.statusLine(); !strings.Contains(line, "ERROR") || !strings.Contains(line, "browse failed") {
		t.Fatalf("diagnostic hid error status: %q", line)
	}
}

func TestStructuralJSONHorizontalExtentIncludesRenderedError(t *testing.T) {
	model := recordViewModel(t)
	model.width = 30
	model.recordMode = ModeJSON
	model.records[0].Err = errors.New(strings.Repeat("structural failure ", 6))
	if got := model.maxHorizontal(); got == 0 {
		t.Fatal("structural error prefix was omitted from JSON horizontal extent")
	}
}

func TestFitHeightTruncatesAndPadsLinesToTerminalWidth(t *testing.T) {
	for _, input := range []string{"123456789", "123"} {
		got := fitHeight(input, 5, 1)
		if width := lipgloss.Width(got); width != 5 {
			t.Fatalf("fitHeight(%q) width = %d, want 5: %q", input, width, got)
		}
	}
}

func TestHelpPopupOverlaysMainViewWithGroupedBindings(t *testing.T) {
	model := recordViewModel(t)
	// Use a taller terminal so every help section fits without truncation.
	model.width, model.height = 80, 36
	model.visible = VisibleRows(model.width, model.height, false)
	model.budget = RowBudget(model.visible)
	model.recordPage.resize(model.visible, model.budget)
	model.showHelp = true
	content := model.View().Content
	if !strings.Contains(content, "HELP") || !strings.Contains(content, "? or esc to close") {
		t.Fatalf("help popup header/footer missing: %q", content)
	}
	for _, section := range []string{"NAVIGATION", "ACTIONS", "GENERAL"} {
		if !strings.Contains(content, section) {
			t.Fatalf("help popup missing section %q: %q", section, content)
		}
	}
	for _, binding := range []string{"enter", "/", "←/h", "→/l", "q"} {
		if !strings.Contains(content, binding) {
			t.Fatalf("help popup missing binding %q: %q", binding, content)
		}
	}
	// The main view remains visible behind the popup.
	if !strings.Contains(ansi.Strip(content), "HQ.DATA") {
		t.Fatalf("main view title line hidden behind help popup: %q", ansi.Strip(content))
	}
	// Rounded popup border should be present.
	if !strings.Contains(content, "╭") || !strings.Contains(content, "╰") {
		t.Fatalf("help popup rounded border missing: %q", content)
	}
	if lines := strings.Count(content, "\n") + 1; lines != model.height {
		t.Fatalf("help view lines = %d, want terminal height %d", lines, model.height)
	}
}

func TestMappingFormShowsFocusMarkerAndFitsTerminalWidth(t *testing.T) {
	model := recordViewModel(t)
	model.handleAction(actionCopybook)
	if model.mappingView == nil || model.mappingView.form == nil {
		t.Fatalf("mapping view did not open the add form: %#v", model.mappingView)
	}

	content := model.View().Content
	stripped := ansi.Strip(content)
	if !strings.Contains(stripped, "> COPYBOOK") {
		t.Fatalf("form missing focused marker on COPYBOOK: %q", stripped)
	}
	if strings.Contains(stripped, "> PATTERN") {
		t.Fatalf("PATTERN should not be focused yet: %q", stripped)
	}

	model.mappingView.form.moveFocus(1)
	content = model.View().Content
	stripped = ansi.Strip(content)
	if !strings.Contains(stripped, "> PATTERN") {
		t.Fatalf("form focus did not move to PATTERN: %q", stripped)
	}

	for i, line := range strings.Split(content, "\n") {
		if w := lipgloss.Width(line); w > model.width {
			t.Fatalf("form line %d width = %d, want <= %d: %q", i, w, model.width, line)
		}
	}
}

func TestMappingListShowsEntriesAppliedMarkerAndSelection(t *testing.T) {
	model := recordViewModel(t)
	model.deps.Mappings = &fakeMappingStore{mappings: []dsnmap.Mapping{
		{Pattern: "HQ.DATA", DSN: "HQ.COPYLIB(EXACT)", Record: "EXACT-REC"},
		{Pattern: "HQ.*", Local: "wild.cpy"},
	}}
	model.overlayMappedPattern = "HQ.DATA"
	model.handleAction(actionCopybook)

	content := model.View().Content
	stripped := ansi.Strip(content)
	for _, want := range []string{"COPYBOOK MAPPINGS", "PATTERN", "RECORD", "HQ.DATA", "HQ.COPYLIB(EXACT)", "EXACT-REC", "HQ.*", "wild.cpy", "applied", "> HQ.DATA"} {
		if !strings.Contains(stripped, want) {
			t.Fatalf("mapping list missing %q: %q", want, stripped)
		}
	}
	for i, line := range strings.Split(content, "\n") {
		if w := lipgloss.Width(line); w > model.width {
			t.Fatalf("list line %d width = %d, want <= %d: %q", i, w, model.width, line)
		}
	}
}

func TestStatePanelAlignsStatusAndDetailColumns(t *testing.T) {
	model := recordViewModel(t)
	model.records = nil
	for _, level := range []statusLevel{statusLoading, statusReady, statusWarn, statusError, statusEmpty} {
		model.status = status{Level: level, Text: "sample detail"}
		content := model.View().Content
		stripped := ansi.Strip(content)
		headerIdx := strings.Index(stripped, "DETAIL")
		firstNewline := strings.Index(stripped, "\n")
		if headerIdx < 0 || firstNewline < 0 {
			t.Fatalf("status %q: header or newline missing\n%s", level, stripped)
		}
		bodyIdx := strings.Index(stripped[firstNewline+1:], "DETAIL")
		if bodyIdx < 0 {
			t.Fatalf("status %q: DETAIL not found in body\n%s", level, stripped)
		}
		bodyIdx += firstNewline + 1
		if headerIdx != bodyIdx {
			t.Fatalf("status %q: DETAIL column misaligned (header %d vs body %d)\n%s", level, headerIdx, bodyIdx, stripped)
		}
	}
}

func TestTinyLoadingEmptyErrorAndMappingViewsAreExplicit(t *testing.T) {
	model := recordViewModel(t)
	model.width = MinTerminalWidth - 1
	model.height = 7
	model.visible = 0
	model.budget = 0
	model.recordPage.resize(model.visible, model.budget)
	if content := model.View().Content; !strings.Contains(content, "TERMINAL TOO SMALL") || !strings.Contains(content, "no row request dispatched") {
		t.Fatalf("tiny view = %q", content)
	}

	model.width, model.height = 80, 12
	model.visible = VisibleRows(80, 12, false)
	model.budget = RowBudget(model.visible)
	model.recordPage.resize(model.visible, model.budget)
	model.records = nil
	model.status = status{Level: statusLoading, Text: "reading bounded range"}
	if content := model.View().Content; !strings.Contains(content, "LOADING") {
		t.Fatalf("loading view = %q", content)
	}
	model.status = status{Level: statusEmpty, Text: "no records returned"}
	if content := model.View().Content; !strings.Contains(content, "EMPTY") {
		t.Fatalf("empty view = %q", content)
	}
	model.status = status{Level: statusError, Text: "request failed"}
	if content := model.View().Content; !strings.Contains(content, "ERROR") || !strings.Contains(content, "request failed") {
		t.Fatalf("error view = %q", content)
	}

	model.handleAction(actionCopybook)
	content := model.View().Content
	for _, want := range []string{"COPYBOOK MAPPINGS", "COPYBOOK", "PATTERN", "empty copybook removes the mapping"} {
		if !strings.Contains(content, want) {
			t.Fatalf("mapping view missing %q: %q", want, content)
		}
	}
}

func TestStatusLineShowsPositionFlushRightWithMoreIndicator(t *testing.T) {
	model := recordViewModel(t)
	model.screen = ScreenDataSets
	model.datasets = []zosmf.DataSet{
		{Name: "A"}, {Name: "B"}, {Name: "C"}, {Name: "D"}, {Name: "E"},
	}
	model.datasetPage.reset(model.visible, model.budget)
	model.datasetPage.apply([]string{"A", "B", "C", "D", "E"}, true, model.datasetPage.initialPlan(""))
	model.datasetPage.move(2)
	model.status = status{Level: statusReady, Text: "5 data sets"}

	line := ansi.Strip(model.statusLine())
	if !strings.Contains(line, "row 3 of 5+") {
		t.Fatalf("status line missing position indicator: %q", line)
	}
	trimmed := strings.TrimRight(line, " ")
	if !strings.HasSuffix(trimmed, "5+") {
		t.Fatalf("position not flush right: %q", line)
	}
}

func TestStatusLineRecordPositionUsesActualRecordNumber(t *testing.T) {
	model := recordWindowModel(t, 5, 10)
	model.recordPage.move(3)
	model.status = status{Level: statusReady, Text: "10 records"}

	line := ansi.Strip(model.statusLine())
	if !strings.Contains(line, "record 00000104 of 10") {
		t.Fatalf("status line missing record position: %q", line)
	}
}

func TestTitleLinesOmitCacheAndRangeDetails(t *testing.T) {
	model := recordViewModel(t)
	model.prefix = "DEMO.*"
	model.screen = ScreenDataSets
	model.datasets = []zosmf.DataSet{{Name: "DEMO.A"}, {Name: "DEMO.B"}}

	title := ansi.Strip(model.titleLine())
	if !strings.Contains(title, "DATASETS") || !strings.Contains(title, "prefix DEMO.*") {
		t.Fatalf("data set title missing expected body: %q", title)
	}
	if strings.Contains(title, "range") || strings.Contains(title, "cached") {
		t.Fatalf("data set title still contains old internals: %q", title)
	}

	model.screen = ScreenMembers
	model.dataSet = zosmf.DataSet{Name: "DEMO.PDS"}
	model.memberPattern = "MEM*"
	model.members = []zosmf.Member{{Name: "MEM1"}}
	title = ansi.Strip(model.titleLine())
	if !strings.Contains(title, "DEMO.PDS") || !strings.Contains(title, "members") || !strings.Contains(title, "filter MEM*") {
		t.Fatalf("member title missing expected body: %q", title)
	}
	if strings.Contains(title, "range") || strings.Contains(title, "cached") {
		t.Fatalf("member title still contains old internals: %q", title)
	}

	model.screen = ScreenRecords
	model.dataSet = zosmf.DataSet{Name: "HQ.DATA"}
	model.recordMode = ModeTable
	model.overlay = &overlay{Source: CopybookSource{Local: "book.cpy"}, Columns: []fieldColumn{{Path: "F", Parts: []string{"F"}}}}
	title = ansi.Strip(model.titleLine())
	if !strings.Contains(title, "HQ.DATA") || !strings.Contains(title, "COPYBOOK TABLE") {
		t.Fatalf("record title missing expected body: %q", title)
	}
	if strings.Contains(title, "records ") || strings.Contains(title, "cached") || strings.Contains(title, "1–") {
		t.Fatalf("record title still contains old internals: %q", title)
	}
}
