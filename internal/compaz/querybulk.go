package compaz

import (
	"context"
	"errors"
	"io"
	"os"
	"sync"
	"sync/atomic"

	tea "charm.land/bubbletea/v2"

	"github.com/Tannex/cq/internal/query"
	"github.com/Tannex/cq/internal/record"
	"github.com/Tannex/cq/internal/recordset"
	"github.com/Tannex/cq/internal/zosmf"
)

type queryBulkMsg struct {
	Profile    string
	Generation uint64
	Path       string
	Err        error
}

// startQueryBulkDownload pulls every record of the browsed data set in one
// record-mode request into a temp file. Queries evaluate from that file, so
// the host is asked exactly once instead of page by page.
func (m *Model) startQueryBulkDownload(ws *workspace, streamer zosmf.RecordStreamer) tea.Cmd {
	popup := m.query
	target := ws.dataSet.Name
	if ws.member != nil {
		target += "(" + ws.member.Name + ")"
	}
	ctx := popup.ctx
	profile, generation := ws.profile, popup.generation
	return func() tea.Msg {
		msg := queryBulkMsg{Profile: profile, Generation: generation}
		file, err := os.CreateTemp("", "compaz-query-*.records")
		if err != nil {
			msg.Err = err
			return msg
		}
		stream, err := streamer.OpenRecords(ctx, target)
		if err == nil {
			_, err = io.Copy(file, stream)
			_ = stream.Close()
		}
		if closeErr := file.Close(); err == nil {
			err = closeErr
		}
		if err != nil {
			_ = os.Remove(file.Name())
			msg.Err = err
			return msg
		}
		msg.Path = file.Name()
		return msg
	}
}

// handleQueryBulk accepts a finished download and starts the evaluation from
// it. A failed download stops the query; a stale one only cleans up its file.
func (m *Model) handleQueryBulk(msg queryBulkMsg) tea.Cmd {
	popup := m.query
	ws := m.ws()
	if popup == nil || !popup.running || msg.Generation != popup.generation || ws.profile != msg.Profile {
		if msg.Path != "" {
			_ = os.Remove(msg.Path)
		}
		return nil
	}
	if msg.Err != nil {
		if popup.ctx != nil && popup.ctx.Err() != nil {
			return nil
		}
		popup.stopSearch()
		popup.err = "download failed: " + msg.Err.Error()
		return nil
	}
	ws.setBulkRecords(msg.Path)
	return evalQuery(popup.ctx, popup.compiled, ws.overlay, ws.bulkRecordsFile, ws.profile, popup.generation, popup.resultCap)
}

// bulkFile is a whole-data-set download as queries use it. The file is mapped
// and its records are indexed against the copybook on first use; every later
// query over the same overlay starts evaluating immediately.
//
// Evaluations run on their own goroutines and may outlive the workspace's
// interest in the download (a refresh, a new copybook, quit), so the mapped
// set is reference counted: it is unmapped when the last query using it ends.
type bulkFile struct {
	path string

	mu      sync.Mutex
	dropped bool
	set     *sharedSet
	decoder *record.Decoder // the overlay decoder set was indexed for
}

type sharedSet struct {
	*recordset.Set
	refs atomic.Int32
}

func (s *sharedSet) release() {
	if s.refs.Add(-1) == 0 {
		_ = s.Close()
	}
}

func newBulkFile(path string) *bulkFile { return &bulkFile{path: path} }

var errBulkDropped = errors.New("the downloaded records are no longer cached")

// open returns the record set for the overlay, indexing the file if this is
// the first query with that overlay. The caller must release the set.
func (b *bulkFile) open(ctx context.Context, ov *overlay) (*sharedSet, error) {
	b.mu.Lock()
	if b.dropped {
		b.mu.Unlock()
		return nil, errBulkDropped
	}
	if b.set != nil && b.decoder == ov.Decoder {
		set := b.set
		set.refs.Add(1)
		b.mu.Unlock()
		return set, nil
	}
	b.mu.Unlock()

	// Index outside the lock: it reads the whole file, and drop must never
	// wait on it from the UI loop.
	indexed, err := recordset.Open(ctx, b.path, ov.Decoder.QueryView())
	if err != nil {
		return nil, err
	}
	set := &sharedSet{Set: indexed}
	set.refs.Store(2) // the cache and the caller

	b.mu.Lock()
	defer b.mu.Unlock()
	if b.dropped {
		set.refs.Store(1)
		set.release()
		return nil, errBulkDropped
	}
	if b.set != nil {
		b.set.release()
	}
	b.set, b.decoder = set, ov.Decoder
	return set, nil
}

// drop ends the cache's hold on the set. The caller removes the file.
func (b *bulkFile) drop() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.dropped = true
	if b.set != nil {
		b.set.release()
		b.set, b.decoder = nil, nil
	}
}

// evalQuery runs the expression once with the whole downloaded data set as
// its input array. Records are decoded lazily, member by member, as the
// expression reads them.
func evalQuery(ctx context.Context, compiled *query.Query, ov *overlay, bulk *bulkFile, profile string, generation uint64, resultCap int) tea.Cmd {
	return func() tea.Msg {
		msg := queryEvalMsg{Profile: profile, Generation: generation}
		set, err := bulk.open(ctx, ov)
		if err != nil {
			if ctx.Err() != nil {
				msg.Canceled = true
			} else {
				msg.LastError = err.Error()
			}
			return msg
		}
		defer set.release()
		msg.Evaluated, msg.Errors = set.Frames(), set.Skipped()
		result := set.Query(ctx, compiled, resultCap)
		msg.Lines, msg.Matches, msg.Canceled = result.Lines, result.Matches, result.Canceled
		if result.Err != nil {
			msg.LastError = result.Err.Error()
		}
		return msg
	}
}
