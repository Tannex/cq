package cqt

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/Tannex/cq/internal/record"
)

func TestBuildOverlayUsesLocalSourceAndExistingDSNCopyResolver(t *testing.T) {
	browser := &fakeBrowser{fetchText: func(_ context.Context, dsn string) ([]byte, error) {
		if dsn == "HLQ.COPYLIB(EXTRA)" {
			return []byte("05 AMOUNT PIC 9(2).\n"), nil
		}
		return nil, errors.New("not found")
	}}
	loadFile := func(_ context.Context, path string) ([]byte, error) {
		if path != "/tmp/record.cpy" {
			t.Fatalf("local path = %q", path)
		}
		return []byte("01 REC.\n 05 NAME PIC X(3).\n COPY EXTRA.\n"), nil
	}

	built, err := buildOverlay(context.Background(), CopybookSource{
		Local: "/tmp/record.cpy", Format: "free", Record: "REC",
	}, "latin1", browser, loadFile, []string{"HLQ.COPYLIB"})
	if err != nil {
		t.Fatal(err)
	}
	if len(browser.fetchRequests) != 1 || browser.fetchRequests[0] != "HLQ.COPYLIB(EXTRA)" {
		t.Fatalf("COPY requests = %#v", browser.fetchRequests)
	}
	if built.Record.Name != "REC" || len(built.Columns) != 2 || built.Columns[0].Path != "NAME" || built.Columns[1].Path != "AMOUNT" {
		t.Fatalf("overlay = %#v columns=%#v", built.Record, built.Columns)
	}
	decoded, err := built.Decoder.DecodeDisplay([]byte("ABC12"))
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := decoded.JSON()
	if err != nil {
		t.Fatal(err)
	}
	if got := string(encoded); got != `{"NAME":"ABC","AMOUNT":12}` {
		t.Fatalf("typed overlay JSON = %s", got)
	}
	amount, _ := valueAtPath(decoded.Value, []string{"AMOUNT"})
	if amount != json.Number("12") {
		t.Fatalf("amount type/value = %#v (%T)", amount, amount)
	}
}

func TestBuildOverlayUsesDSNSourceAndRecordSelection(t *testing.T) {
	browser := &fakeBrowser{fetchText: func(_ context.Context, dsn string) ([]byte, error) {
		if dsn != "HLQ.COPYLIB(RECS)" {
			t.Fatalf("copybook DSN = %q", dsn)
		}
		return []byte("01 FIRST. 05 A PIC X.\n01 SECOND. 05 B PIC X(2).\n"), nil
	}}
	built, err := buildOverlay(context.Background(), CopybookSource{
		DSN: "hlq.copylib(recs)", Format: "free", Record: "second",
	}, "latin1", browser, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if built.Source.DSN != "HLQ.COPYLIB(RECS)" || built.Record.Name != "SECOND" || built.Record.MaxLength != 2 {
		t.Fatalf("DSN overlay source=%#v record=%#v", built.Source, built.Record)
	}
}

func TestOverlayColumnsFlattenGroupsButKeepOccursAsCompactCells(t *testing.T) {
	built, err := buildOverlay(context.Background(), CopybookSource{Local: "book", Format: "free"}, "latin1", &fakeBrowser{}, func(context.Context, string) ([]byte, error) {
		return []byte(`01 R.
 05 CUSTOMER.
  10 NAME PIC X(3).
  10 ITEMS OCCURS 2 TIMES.
   15 CODE PIC X.
`), nil
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(built.Columns) != 2 || built.Columns[0].Path != "CUSTOMER.NAME" || built.Columns[1].Path != "CUSTOMER.ITEMS" {
		t.Fatalf("columns = %#v", built.Columns)
	}
	decoded, err := built.Decoder.DecodeDisplay([]byte("ABCXY"))
	if err != nil {
		t.Fatal(err)
	}
	items, ok := valueAtPath(decoded.Value, built.Columns[1].Parts)
	if !ok || compactValue(items) != `[{"CODE":"X"},{"CODE":"Y"}]` {
		t.Fatalf("OCCURS compact cell = %#v %q", items, compactValue(items))
	}
}

func TestInvalidOverlayResultRetainsPreviousValidOverlay(t *testing.T) {
	model := &Model{
		workspace: workspace{
			overlay:           &overlay{Source: CopybookSource{Local: "valid.cpy"}},
			overlayGeneration: 4,
			overlayPending:    true,
			status:            status{Level: statusReady},
		},
	}
	previous := model.overlay
	model.handleOverlayResult(model.ws(), overlayResultMsg{Generation: 4, Source: CopybookSource{Local: "bad.cpy"}, Err: errors.New("parse failed")})
	if model.overlay != previous || model.status.Level != statusError || !strings.Contains(model.status.Text, "previous overlay retained") {
		t.Fatalf("replacement failure overlay=%p previous=%p status=%#v", model.overlay, previous, model.status)
	}
}

func TestOverlayFailureRemainsVisibleAcrossConcurrentBrowseCompletion(t *testing.T) {
	model := &Model{
		workspace: workspace{
			overlayGeneration: 2,
			overlayPending:    true,
			status:            status{Level: statusLoading},
		},
	}
	model.handleOverlayResult(model.ws(), overlayResultMsg{Generation: 2, Err: errors.New("parse failed")})
	if strings.Contains(model.overlayError, "previous overlay retained") {
		t.Fatalf("first overlay failure incorrectly claimed a previous overlay: %q", model.overlayError)
	}

	// A concurrently dispatched browse command may complete after the overlay.
	// Its READY status must not erase the actionable copybook failure.
	model.status = status{Level: statusReady, Text: "10 data sets"}
	level, text := model.effectiveStatus()
	if level != statusError || !strings.Contains(text, "parse failed") {
		t.Fatalf("effective status = %s %q, want persistent overlay failure", level, text)
	}
}

func TestClearOverlayCancelsPendingReplacementAndRejectsItsResult(t *testing.T) {
	model := &Model{
		workspace: workspace{
			overlay:           &overlay{Source: CopybookSource{Local: "valid.cpy"}},
			overlaySource:     CopybookSource{Local: "valid.cpy"},
			overlayGeneration: 8,
			overlayPending:    true,
			recordMode:        ModeTable,
		},
	}
	model.handleAction(actionClearOverlay)
	if model.overlay != nil || model.overlayPending || model.recordMode != ModeRaw {
		t.Fatalf("clear state overlay=%#v pending=%v mode=%d", model.overlay, model.overlayPending, model.recordMode)
	}
	model.handleOverlayResult(model.ws(), overlayResultMsg{Generation: 8, Overlay: &overlay{Source: CopybookSource{Local: "late.cpy"}}})
	if model.overlay != nil {
		t.Fatal("stale replacement reapplied after clear")
	}
}

func TestCopybookDialogRequiresExactlyOneSourceAndValidFormat(t *testing.T) {
	dialog := newCopybookDialog(CopybookSource{})
	if _, _, err := dialog.source().validate(); err == nil {
		t.Fatal("empty dialog source was accepted")
	}
	dialog.local.SetValue("local.cpy")
	dialog.dsn.SetValue("HLQ.CPY(MEM)")
	if _, _, err := dialog.source().validate(); err == nil {
		t.Fatal("dialog accepted both local and DSN sources")
	}
	dialog.dsn.SetValue("")
	dialog.format.SetValue("variable")
	if _, _, err := dialog.source().validate(); err == nil || !strings.Contains(err.Error(), "auto, fixed, or free") {
		t.Fatalf("invalid format error = %v", err)
	}
}

func TestDiagnosticColumnMatchingIncludesNestedOccursPath(t *testing.T) {
	diagnostics := []record.Diagnostic{{FieldPath: "CUSTOMER.ITEMS[2].CODE", Offset: 7, Length: 1, Err: errors.New("bad")}}
	if _, ok := diagnosticForColumn(diagnostics, "CUSTOMER.ITEMS"); !ok {
		t.Fatal("OCCURS column did not match nested diagnostic")
	}
	if _, ok := diagnosticForColumn(diagnostics, "CUSTOMER.NAME"); ok {
		t.Fatal("unrelated column matched diagnostic")
	}
}
