package cqt

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"charm.land/bubbles/v2/help"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/Tannex/cq/internal/decode"
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
		width:        80,
		height:       12,
		visible:      VisibleRows(80, 12),
		budget:       RowBudget(VisibleRows(80, 12)),
		screen:       ScreenRecords,
		dataSet:      zosmf.DataSet{Name: "HQ.DATA", Organization: "PS"},
		codepageName: "latin1",
		charmap:      cm,
		keys:         DefaultKeyMap(),
		help:         helpModelForTest(),
		status:       status{Level: statusReady, Text: "records ready"},
	}
	model.records = []recordRow{
		{Record: zosmf.Record{Number: 1, Data: []byte{'A', 0, '\n', 'B'}}},
		{Record: zosmf.Record{Number: 2, Data: []byte("SECOND")}},
	}
	model.recordPage.reset(0, model.visible, model.budget)
	model.recordPage.apply([]string{"1", "2"}, false, model.recordPage.initialPlan(0))
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
	model.showDiagnostics = true
	status := model.statusLine()
	if !strings.Contains(status, "field AMOUNT") || !strings.Contains(status, "offset 5") {
		t.Fatalf("selected-cell diagnostic detail = %q", status)
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

func TestFitHeightTruncatesOverwideLines(t *testing.T) {
	got := fitHeight("123456789", 5, 1)
	if width := lipgloss.Width(got); width > 5 {
		t.Fatalf("fitHeight width = %d, want <= 5: %q", width, got)
	}
}

func TestHelpPanelReplacesDataAreaWithGroupedBindings(t *testing.T) {
	model := recordViewModel(t)
	// Use a taller terminal so every help section fits without truncation.
	model.width, model.height = 80, 24
	model.visible = VisibleRows(model.width, model.height)
	model.budget = RowBudget(model.visible)
	model.showHelp = true
	content := model.View().Content
	if !strings.Contains(content, "HELP") || !strings.Contains(content, "press ? to close") {
		t.Fatalf("help panel header missing: %q", content)
	}
	for _, section := range []string{"NAVIGATION", "ACTIONS", "GENERAL"} {
		if !strings.Contains(content, section) {
			t.Fatalf("help panel missing section %q: %q", section, content)
		}
	}
	for _, binding := range []string{"enter", "f10", "f11", "q"} {
		if !strings.Contains(content, binding) {
			t.Fatalf("help panel missing binding %q: %q", binding, content)
		}
	}
	if lines := strings.Count(content, "\n") + 1; lines != model.height {
		t.Fatalf("help view lines = %d, want terminal height %d", lines, model.height)
	}
}

func TestCopybookDialogShowsFocusMarkerAndFitsTerminalWidth(t *testing.T) {
	model := recordViewModel(t)
	model.dialog = newCopybookDialog(CopybookSource{Local: "customer.cpy"})
	model.dialog.setWidth(model.width)

	content := model.View().Content
	stripped := ansi.Strip(content)
	if !strings.Contains(stripped, "> LOCAL") {
		t.Fatalf("dialog missing focused marker on LOCAL: %q", stripped)
	}
	if strings.Contains(stripped, "> DSN") {
		t.Fatalf("DSN should not be focused yet: %q", stripped)
	}

	model.dialog.moveFocus(1)
	content = model.View().Content
	stripped = ansi.Strip(content)
	if !strings.Contains(stripped, "> DSN") {
		t.Fatalf("dialog focus did not move to DSN: %q", stripped)
	}

	for i, line := range strings.Split(content, "\n") {
		if w := lipgloss.Width(line); w > model.width {
			t.Fatalf("dialog line %d width = %d, want <= %d: %q", i, w, model.width, line)
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

func TestTinyLoadingEmptyErrorAndDialogViewsAreExplicit(t *testing.T) {
	model := recordViewModel(t)
	model.width = MinTerminalWidth - 1
	model.height = 7
	model.visible = 0
	if content := model.View().Content; !strings.Contains(content, "TERMINAL TOO SMALL") || !strings.Contains(content, "no row request dispatched") {
		t.Fatalf("tiny view = %q", content)
	}

	model.width, model.height = 80, 12
	model.visible = VisibleRows(80, 12)
	model.budget = RowBudget(model.visible)
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

	model.dialog = newCopybookDialog(CopybookSource{Local: "customer.cpy", Format: "free", Record: "CUSTOMER"})
	content := model.View().Content
	for _, want := range []string{"COPYBOOK OVERLAY", "LOCAL", "DSN", "FORMAT", "RECORD", "previous valid overlay is retained"} {
		if !strings.Contains(content, want) {
			t.Fatalf("dialog view missing %q: %q", want, content)
		}
	}
}
