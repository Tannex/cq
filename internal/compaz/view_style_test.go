package compaz

import (
	"encoding/json"
	"errors"
	"fmt"
	"image/color"
	"strings"
	"testing"

	"charm.land/bubbles/v2/table"
	"github.com/charmbracelet/x/ansi"

	"github.com/Tannex/cq/internal/layout"
	"github.com/Tannex/cq/internal/record"
	"github.com/Tannex/cq/internal/zosmf"
)

// fgSGR returns the truecolor SGR foreground fragment for a palette color so
// assertions track the palette instead of hardcoding hex values.
func fgSGR(c color.Color) string {
	r, g, b, _ := c.RGBA()
	return fmt.Sprintf("38;2;%d;%d;%d", r>>8, g>>8, b>>8)
}

// SGR foreground fragments for the palette colors used by cell tints.
var (
	cyanSGR   = fgSGR(ayu.tag)
	greenSGR  = fgSGR(ayu.str)
	mutedSGR  = fgSGR(ayu.comment)
	dangerSGR = fgSGR(ayu.markup)
)

func TestRightAligned(t *testing.T) {
	if got := rightAligned("42", 6); got != "    42" {
		t.Fatalf("rightAligned(42, 6) = %q", got)
	}
	if got := rightAligned("1234567", 6); got != "1234567" {
		t.Fatalf("overlong text must pass through, got %q", got)
	}
	if got := rightAligned("", 6); got != "" {
		t.Fatalf("empty text must stay empty for the placeholder, got %q", got)
	}
}

func TestStyledCell(t *testing.T) {
	if got := styledCell("", consolePalette.cyan, false); ansi.Strip(got) != emptyCellMark || !strings.Contains(got, mutedSGR) {
		t.Fatalf("empty cell should render dim placeholder, got %q", got)
	}
	if got := styledCell("", consolePalette.cyan, true); got != emptyCellMark {
		t.Fatalf("empty cursor cell should be a bare placeholder, got %q", got)
	}
	if got := styledCell("X", consolePalette.cyan, true); got != "X" {
		t.Fatalf("cursor cell must stay untinted, got %q", got)
	}
	if got := styledCell("X", consolePalette.cyan, false); !strings.Contains(got, cyanSGR) {
		t.Fatalf("data cell should carry the tint, got %q", got)
	}
}

func TestKindCellStyle(t *testing.T) {
	for _, kind := range []layout.Kind{layout.KindZoned, layout.KindPacked, layout.KindBinary, layout.KindFloat} {
		style, alignRight := kindCellStyle(kind)
		if !alignRight || style.GetForeground() != consolePalette.cyan.GetForeground() {
			t.Fatalf("kind %s should be right-aligned numeric tone", kind)
		}
	}
	style, alignRight := kindCellStyle(layout.KindEdited)
	if !alignRight || style.GetForeground() != consolePalette.muted.GetForeground() {
		t.Fatal("edited kind should be dimmed and right-aligned")
	}
	if style, alignRight = kindCellStyle(layout.KindText); alignRight || style.GetForeground() != consolePalette.plain.GetForeground() {
		t.Fatal("text kind should stay plain and left-aligned")
	}
}

func TestDataSetTableStyling(t *testing.T) {
	model := recordViewModel(t)
	model.visible = 4
	model.budget = 24
	model.datasets = []zosmf.DataSet{
		{Name: "HQ.LIB", Organization: "PO", RecordFormat: "FB", RecordLength: "80", Volume: "WORK01", ReferenceDate: "2026/07/01"},
		{Name: "HQ.SEQ", Organization: "PS", RecordFormat: "VB", RecordLength: "120"},
	}
	model.datasetPage.reset(model.visible, model.budget)
	model.datasetPage.apply([]string{"HQ.LIB", "HQ.SEQ"}, true, model.datasetPage.initialPlan(""))
	model.width = 100 // wide enough for VOLUME and REFERENCED

	view := model.dataSetTableView()
	lines := strings.Split(view, "\n")
	stripped := strings.Split(ansi.Strip(view), "\n")

	// Cursor row (HQ.LIB) must stay untinted so Selected renders uniformly.
	cursorLine := lines[1]
	for _, sgr := range []string{cyanSGR, greenSGR, mutedSGR} {
		if strings.Contains(cursorLine, sgr) {
			t.Fatalf("cursor row must not carry cell tints (%s):\n%q", sgr, cursorLine)
		}
	}
	// Non-cursor PS row carries the green DSORG tint and dim placeholders for
	// its empty VOLUME and REFERENCED cells.
	dataLine := lines[2]
	if !strings.Contains(dataLine, greenSGR) {
		t.Fatalf("PS row should tint DSORG green:\n%q", dataLine)
	}
	if !strings.Contains(stripped[2], emptyCellMark) {
		t.Fatalf("empty cells should render placeholder:\n%q", stripped[2])
	}
	// LRECL right-aligned within its 6-wide column: "    80" and "   120".
	if !strings.Contains(stripped[1], "    80") || !strings.Contains(stripped[2], "   120") {
		t.Fatalf("LRECL should be right-aligned:\n%q\n%q", stripped[1], stripped[2])
	}
}

func TestMemberTableRightAlignsNumericColumns(t *testing.T) {
	model := recordViewModel(t)
	model.visible = 4
	model.budget = 24
	model.members = []zosmf.Member{
		{Name: "ALPHA", Version: 1, Modification: 12, CurrentRecords: 7, User: "IBMUSER", ModifiedDate: "2026/07/01", ModifiedTime: "10:00"},
		{Name: "BETA", Version: 10, Modification: 2, CurrentRecords: 1234},
	}
	model.memberPage.reset(model.visible, model.budget)
	model.memberPage.apply([]string{"ALPHA", "BETA"}, true, model.memberPage.initialPlan(""))
	model.width = 100

	view := model.memberTableView()
	stripped := strings.Split(ansi.Strip(view), "\n")
	if !strings.Contains(stripped[1], "   1 ") || !strings.Contains(stripped[1], "       7") {
		t.Fatalf("VER/RECORDS should be right-aligned:\n%q", stripped[1])
	}
	if !strings.Contains(stripped[2], "  10 ") || !strings.Contains(stripped[2], "    1234") {
		t.Fatalf("VER/RECORDS should be right-aligned:\n%q", stripped[2])
	}
	// Non-cursor row without stats shows placeholders for the empty USER and
	// MODIFIED columns.
	if !strings.Contains(stripped[2], emptyCellMark) {
		t.Fatalf("empty USER/MODIFIED cells should render placeholder:\n%q", stripped[2])
	}
}

func TestRecordTableTypeAwareAndDiagnosticStyling(t *testing.T) {
	model := recordViewModel(t)
	model.visible = 4
	model.budget = 24
	model.overlay = &overlay{Columns: []fieldColumn{
		{Path: "NAME", Parts: []string{"NAME"}, Width: 12, Kind: layout.KindText},
		{Path: "AMOUNT", Parts: []string{"AMOUNT"}, Width: 10, Kind: layout.KindPacked},
	}}
	model.records = []recordRow{
		{
			Record: zosmf.Record{Number: 1, Data: []byte("ROW1")},
			Decoded: &record.DecodedRecord{Value: record.Object{
				{Name: "NAME", Value: "ALPHA"},
				{Name: "AMOUNT", Value: json.Number("400.25")},
			}},
		},
		{
			Record: zosmf.Record{Number: 2, Data: []byte("ROW2")},
			Decoded: &record.DecodedRecord{
				Value: record.Object{
					{Name: "NAME", Value: "BETA"},
					{Name: "AMOUNT", Value: "�"},
				},
				Diagnostics: []record.Diagnostic{{FieldPath: "AMOUNT", Err: errors.New("bad packed digit")}},
			},
		},
		{
			Record: zosmf.Record{Number: 3, Data: []byte("RAWROW")},
			Err:    errors.New("structural decode error"),
		},
	}
	model.recordPage.reset(model.visible, model.budget)
	model.recordPage.apply([]string{"1", "2", "3"}, true, model.recordPage.initialPlan(0))

	view := model.recordTableView()
	lines := strings.Split(view, "\n")
	stripped := strings.Split(ansi.Strip(view), "\n")

	// Cursor row (record 1) stays untinted; numeric value still right-aligned.
	if strings.Contains(lines[1], cyanSGR) {
		t.Fatalf("cursor row must not carry the numeric tint:\n%q", lines[1])
	}
	if !strings.Contains(stripped[1], "    400.25") {
		t.Fatalf("numeric cell should be right-aligned on cursor row:\n%q", stripped[1])
	}
	// Diagnostic cell on a non-cursor row is danger-tinted and keeps `! `.
	if !strings.Contains(lines[2], dangerSGR) || !strings.Contains(stripped[2], "! ") {
		t.Fatalf("diagnostic cell should be danger-tinted with prefix:\n%q", lines[2])
	}
	// Structural decode error row keeps the `! ` raw fallback, danger-tinted.
	if !strings.Contains(lines[3], dangerSGR) || !strings.Contains(stripped[3], "! RAWROW") {
		t.Fatalf("decode-error row should be danger-tinted raw fallback:\n%q", lines[3])
	}
}

func TestStyledCellsSurviveNarrowTruncation(t *testing.T) {
	columns := []table.Column{{Title: "NAME", Width: 8}}
	rows := []table.Row{{styledCell("VERYLONGVALUE", consolePalette.cyan, false)}}
	view := renderTable(20, 3, columns, rows, -1, false)
	stripped := ansi.Strip(view)
	if strings.Contains(stripped, "VERYLONGVALUE") {
		t.Fatalf("cell should be truncated at column width:\n%q", stripped)
	}
	if !strings.Contains(stripped, "VERYLON…") {
		t.Fatalf("truncated cell should keep leading content:\n%q", stripped)
	}
	if !strings.Contains(view, cyanSGR) {
		t.Fatalf("tint should survive truncation:\n%q", view)
	}
}

func TestChromeRuleSeparatesMetaRowsFromData(t *testing.T) {
	model := recordViewModel(t)
	model.records = []recordRow{{Record: zosmf.Record{Number: 1, Data: []byte("VALUE")}}}
	model.recordPage.reset(model.visible, model.budget)
	model.recordPage.apply([]string{"1"}, false, model.recordPage.initialPlan(0))

	lines := strings.Split(ansi.Strip(model.View().Content), "\n")
	rule := -1
	for i, line := range lines {
		if strings.Count(line, "─") == model.width {
			rule = i
			break
		}
	}
	if rule != 2 {
		t.Fatalf("chrome rule not on the row under title+search: index %d\n%s", rule, strings.Join(lines, "\n"))
	}
}
