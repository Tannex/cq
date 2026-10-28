package compaz

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/Tannex/cq/internal/zosmf"
)

func TestQueryReopenIgnoresLateCanceledEvaluation(t *testing.T) {
	model, _ := queryModel(t, singleRecordPage(zosmf.Record{Number: 1, Data: []byte("ABC")}))
	path := filepath.Join(t.TempDir(), "records")
	if err := os.WriteFile(path, framedRecords(1, 1), 0600); err != nil {
		t.Fatal(err)
	}
	model.ws().setBulkRecords(path)
	openQuery(t, model)
	model.query.input.SetValue("length")
	oldRun := model.runQuery()

	// Cancel and close before the evaluation command can return its message.
	executeQuery(t, model, applyMessage(t, model, keyPress(tea.KeyEscape, "")))
	executeQuery(t, model, applyMessage(t, model, keyPress(tea.KeyEscape, "")))
	openQuery(t, model)
	model.query.input.SetValue("unique | length")
	newRun := model.runQuery()
	popup := model.query

	executeQuery(t, model, oldRun)
	if !popup.running || popup.cancelled || popup.done {
		t.Fatalf("old evaluation changed the new run: running=%v cancelled=%v done=%v", popup.running, popup.cancelled, popup.done)
	}
	executeQuery(t, model, newRun)
	if !popup.done || popup.cancelled || len(popup.lines) != 1 || popup.lines[0] != "1" {
		t.Fatalf("new query did not finish with its own result: done=%v cancelled=%v lines=%v", popup.done, popup.cancelled, popup.lines)
	}
}

func TestQueryReopenIgnoresLateDownloadFromPreviousDataSet(t *testing.T) {
	model, browser := queryModel(t, singleRecordPage(zosmf.Record{Number: 1, Data: []byte("ABC")}))
	t.Cleanup(func() { model.ws().dropBulkRecords() })
	openQuery(t, model)
	model.query.input.SetValue("length")
	pending := model.runQuery()
	batch, ok := pending().(tea.BatchMsg)
	if !ok || len(batch) != 2 {
		t.Fatal("expected spinner and download commands")
	}
	// The old download has finished, but the UI has not received it yet.
	oldDownload, ok := batch[1]().(queryBulkMsg)
	if !ok || oldDownload.Err != nil || oldDownload.Path == "" {
		t.Fatalf("old download failed: %+v", oldDownload)
	}
	t.Cleanup(func() { _ = os.Remove(oldDownload.Path) })
	executeQuery(t, model, applyMessage(t, model, keyPress(tea.KeyEscape, "")))
	executeQuery(t, model, applyMessage(t, model, keyPress(tea.KeyEscape, "")))

	// Browse another data set and load its copybook before opening a new query.
	model.ws().dataSet.Name = "A.OTHER.DATA"
	model.ws().resetRecordState()
	executeCommand(t, model, model.startOverlay(model.ws(), CopybookSource{Local: "cust.cpy", Format: "free"}))
	browser.stream = func(_ context.Context, dsn string) (io.ReadCloser, error) {
		if dsn != "A.OTHER.DATA" {
			return nil, errors.New("unexpected target " + dsn)
		}
		return io.NopCloser(bytes.NewReader(framedRecords(2, 3))), nil
	}
	openQuery(t, model)
	model.query.input.SetValue("unique | length")
	newRun := model.runQuery()
	popup := model.query

	if next := applyMessage(t, model, oldDownload); next != nil {
		t.Fatal("old download started an evaluation for the new data set")
	}
	if model.ws().bulkRecordsPath != "" || !popup.running {
		t.Fatalf("old download changed the new run: cache=%q running=%v", model.ws().bulkRecordsPath, popup.running)
	}
	if _, err := os.Stat(oldDownload.Path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("old download was not removed: %v", err)
	}
	executeQuery(t, model, newRun)
	if !popup.done || popup.cancelled || len(popup.lines) != 1 || popup.lines[0] != "2" {
		t.Fatalf("new query did not finish with its own data: done=%v cancelled=%v lines=%v", popup.done, popup.cancelled, popup.lines)
	}
	if model.ws().bulkRecordsIdentity != model.ws().recordIdentity() {
		t.Fatalf("new download has the wrong identity: %q", model.ws().bulkRecordsIdentity)
	}
}
