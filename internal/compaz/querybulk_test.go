package compaz

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/Tannex/cq/internal/zosmf"
)

// fakeStreamBrowser adds the RecordStreamer capability to fakeBrowser.
type fakeStreamBrowser struct {
	fakeBrowser
	streamRequests []string
	stream         func(context.Context, string) (io.ReadCloser, error)
}

func (f *fakeStreamBrowser) OpenRecords(ctx context.Context, dsn string) (io.ReadCloser, error) {
	f.streamRequests = append(f.streamRequests, dsn)
	return f.stream(ctx, dsn)
}

// framedRecords encodes 3-byte NAME records n=from..to as z/OSMF record frames.
func framedRecords(from, to int) []byte {
	var buffer bytes.Buffer
	for i := from; i <= to; i++ {
		data := fmt.Appendf(nil, "N%02d", i)
		_ = binary.Write(&buffer, binary.BigEndian, uint32(len(data)))
		buffer.Write(data)
	}
	return buffer.Bytes()
}

func recordsPage(from, to int, more bool) zosmf.RecordPage {
	page := zosmf.RecordPage{MoreRows: more}
	for i := from; i <= to; i++ {
		page.Records = append(page.Records, zosmf.Record{Number: int64(i), Data: fmt.Appendf(nil, "N%02d", i)})
	}
	return page
}

func instantBulkThreshold(t *testing.T) {
	t.Helper()
	previous := queryBulkThreshold
	queryBulkThreshold = 0
	t.Cleanup(func() { queryBulkThreshold = previous })
}

func bulkQueryModel(t *testing.T, browser *fakeStreamBrowser) *Model {
	t.Helper()
	// The 90×13 geometry gives a 14-row budget: page one holds records 1-14
	// with more rows, so the search must go past the cache.
	browser.readRecords = func(_ context.Context, request zosmf.ReadRecordsRequest) (zosmf.RecordPage, error) {
		if request.Start == 0 {
			return recordsPage(1, 14, true), nil
		}
		return recordsPage(int(request.Start)+1, min(int(request.Start)+14, 30), int(request.Start)+14 < 30), nil
	}
	browser.listDataSets = func(context.Context, zosmf.ListDataSetsRequest) (zosmf.DataSetPage, error) {
		return zosmf.DataSetPage{Items: []zosmf.DataSet{{Name: "A.CUSTOMER.DATA", Organization: "PS"}}}, nil
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
	applyMessage(t, model, tea.WindowSizeMsg{Width: 90, Height: 13})
	executeCommand(t, model, model.Init())
	executeCommand(t, model, model.openSelection())
	if model.screen != ScreenRecords || model.overlay == nil {
		t.Fatalf("records screen not ready: screen=%d overlayErr=%q", model.screen, model.overlayError)
	}
	return model
}

func TestQuerySwitchesToBulkDownloadWhenPagingIsSlow(t *testing.T) {
	instantBulkThreshold(t)
	browser := &fakeStreamBrowser{stream: func(_ context.Context, dsn string) (io.ReadCloser, error) {
		if dsn != "A.CUSTOMER.DATA" {
			return nil, errors.New("unexpected target " + dsn)
		}
		return io.NopCloser(bytes.NewReader(framedRecords(1, 30))), nil
	}}
	model := bulkQueryModel(t, browser)
	pagesBefore := len(browser.recordRequests)
	openQuery(t, model)
	runQueryExpr(t, model, ".NAME")

	popup := model.query
	if !popup.done || popup.running {
		t.Fatalf("search not finished: done=%v running=%v err=%q lastErr=%q", popup.done, popup.running, popup.err, popup.lastErr)
	}
	if len(browser.streamRequests) != 1 {
		t.Fatalf("stream requests = %v, want one bulk download", browser.streamRequests)
	}
	if len(browser.recordRequests) != pagesBefore {
		t.Fatalf("paging continued despite the bulk download: %d new fetches", len(browser.recordRequests)-pagesBefore)
	}
	if popup.searched != 30 || popup.matches != 30 {
		t.Fatalf("searched=%d matches=%d, want 30/30", popup.searched, popup.matches)
	}
	// Streaming covered records 1-14 from the cache; the file continues
	// seamlessly at 15 with correct numbering.
	if popup.lines[14] != `   15 │ "N15"` {
		t.Fatalf("line 15 = %q", popup.lines[14])
	}
	if popup.bulkPath == "" {
		t.Fatal("bulk file not retained")
	}
	if _, err := os.Stat(popup.bulkPath); err != nil {
		t.Fatalf("bulk file missing: %v", err)
	}

	// A re-run evaluates entirely from the retained file: no new download, no
	// new page fetches, full coverage from record one.
	runQueryExpr(t, model, ".NAME")
	if popup := model.query; popup.searched != 30 || popup.matches != 30 || popup.lines[0] != `    1 │ "N01"` {
		t.Fatalf("re-run searched=%d matches=%d first=%q", popup.searched, popup.matches, popup.lines[0])
	}
	if len(browser.streamRequests) != 1 || len(browser.recordRequests) != pagesBefore {
		t.Fatalf("re-run refetched: streams=%d pages=%d", len(browser.streamRequests), len(browser.recordRequests)-pagesBefore)
	}

	// Closing the popup removes the download.
	path := model.query.bulkPath
	executeQuery(t, model, applyMessage(t, model, keyPress(tea.KeyEscape, "")))
	if model.query != nil {
		t.Fatal("popup still open")
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("bulk file not cleaned up: %v", err)
	}
}

func TestQueryBulkDownloadFailureFallsBackToPaging(t *testing.T) {
	instantBulkThreshold(t)
	browser := &fakeStreamBrowser{stream: func(context.Context, string) (io.ReadCloser, error) {
		return nil, errors.New("record mode without a range is rejected")
	}}
	model := bulkQueryModel(t, browser)
	openQuery(t, model)
	runQueryExpr(t, model, ".NAME")

	popup := model.query
	if !popup.done || popup.searched != 30 || popup.matches != 30 {
		t.Fatalf("paging fallback incomplete: done=%v searched=%d matches=%d", popup.done, popup.searched, popup.matches)
	}
	if popup.bulkPath != "" {
		t.Fatalf("failed download left a path: %q", popup.bulkPath)
	}
	if !strings.Contains(popup.lastErr, "bulk download failed") {
		t.Fatalf("lastErr = %q", popup.lastErr)
	}
}

func TestQueryBulkArrayFallbackUsesTheDownloadedFile(t *testing.T) {
	instantBulkThreshold(t)
	browser := &fakeStreamBrowser{stream: func(context.Context, string) (io.ReadCloser, error) {
		return io.NopCloser(bytes.NewReader(framedRecords(1, 30))), nil
	}}
	model := bulkQueryModel(t, browser)
	openQuery(t, model)
	// Errors on every single record, so the fallback must evaluate the whole
	// data set as one array — sourced from the downloaded file.
	runQueryExpr(t, model, "map(.NAME) | length")

	popup := model.query
	if !popup.done || !popup.arrayMode {
		t.Fatalf("array fallback did not run: done=%v array=%v lastErr=%q", popup.done, popup.arrayMode, popup.lastErr)
	}
	// All 30 downloaded records form the array input.
	if len(popup.lines) != 1 || popup.lines[0] != "30" {
		t.Fatalf("lines = %#v", popup.lines)
	}
}

func TestStaleBulkDownloadIsRemoved(t *testing.T) {
	file, err := os.CreateTemp(t.TempDir(), "stale-*.records")
	if err != nil {
		t.Fatal(err)
	}
	path := file.Name()
	_ = file.Close()
	browser := &fakeStreamBrowser{}
	model := bulkQueryModel(t, browser)
	openQuery(t, model)
	if cmd := applyMessage(t, model, queryBulkMsg{Path: path, Generation: 99}); cmd != nil {
		t.Fatal("stale bulk result should be dropped")
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("stale bulk file not removed")
	}
}
