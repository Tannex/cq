package compaz

import (
	"bufio"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"os"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/Tannex/cq/internal/query"
	"github.com/Tannex/cq/internal/zosmf"
)

// queryBulkThreshold is how long a streaming search may keep paging before the
// whole data set is downloaded in one request instead. A variable so tests can
// trigger the switch without waiting.
var queryBulkThreshold = 10 * time.Second

// queryBulkMaxRecord mirrors the transport's per-record bound when parsing
// frames back out of the downloaded file.
const queryBulkMaxRecord = 16 << 20

type queryBulkMsg struct {
	Profile    string
	Generation uint64
	Path       string
	Err        error
}

// closeBulk removes the downloaded record file, if any.
func (p *queryPopup) closeBulk() {
	if p.bulkPath != "" {
		_ = os.Remove(p.bulkPath)
		p.bulkPath = ""
	}
}

// startQueryBulkDownload pulls every record of the browsed data set in one
// record-mode request into a temp file. The search continues from that file,
// so a slow host is asked exactly once more instead of page by page.
func (m *Model) startQueryBulkDownload(ws *workspace, streamer zosmf.RecordStreamer) tea.Cmd {
	popup := m.query
	target := ws.dataSet.Name
	if ws.member != nil {
		target += "(" + ws.member.Name + ")"
	}
	// The cache may start past record one (the user located forward); the
	// downloaded file always starts at record one, so remember how many
	// leading frames every file pass must skip to match the streaming scope.
	popup.bulkSkip = 0
	if len(ws.records) > 0 {
		popup.bulkSkip = ws.records[0].Record.Number - 1
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

// handleQueryBulk accepts a finished download. A failed download falls back to
// the paging path; a stale download only cleans up its file.
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
		popup.bulkFailed = true
		popup.lastErr = "bulk download failed: " + msg.Err.Error()
		return m.queryStep(ws)
	}
	popup.bulkPath = msg.Path
	popup.bulkOffset = 0
	popup.bulkEOF = false
	return m.queryStep(ws)
}

// countingReader tracks consumed bytes so a buffered frame reader can report
// the exact resume offset for the next chunk.
type countingReader struct {
	r io.Reader
	n int64
}

func (c *countingReader) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	c.n += int64(n)
	return n, err
}

// readFrame reads one four-byte length-prefixed record. io.EOF marks a clean
// end between frames.
func readFrame(r *bufio.Reader) ([]byte, error) {
	var header [4]byte
	if _, err := io.ReadFull(r, header[:]); err != nil {
		if errors.Is(err, io.EOF) {
			return nil, io.EOF
		}
		return nil, fmt.Errorf("truncated record frame header: %w", err)
	}
	length := int64(binary.BigEndian.Uint32(header[:]))
	if length > queryBulkMaxRecord {
		return nil, fmt.Errorf("record frame of %d bytes exceeds the %d byte bound", length, queryBulkMaxRecord)
	}
	data := make([]byte, int(length))
	if _, err := io.ReadFull(r, data); err != nil {
		return nil, fmt.Errorf("record frame declares %d bytes but is truncated: %w", length, err)
	}
	return data, nil
}

// evalQueryFileChunk evaluates the next chunk of frames from the downloaded
// record file. base is the number of records already covered before this
// chunk; on the first chunk of a pass (offset zero) it doubles as the count
// of leading frames to discard, so file evaluation lines up with what
// streaming already covered.
func evalQueryFileChunk(ctx context.Context, compiled *query.Query, ov *overlay, path string, offset, base int64, count, lineRoom int, profile string, generation uint64) tea.Cmd {
	return func() tea.Msg {
		msg := queryEvalMsg{Profile: profile, Generation: generation, FromFile: true}
		fail := func(err error) tea.Msg {
			msg.FileEOF = true
			msg.LastError = err.Error()
			return msg
		}
		file, err := os.Open(path)
		if err != nil {
			return fail(err)
		}
		defer file.Close()
		if _, err := file.Seek(offset, io.SeekStart); err != nil {
			return fail(err)
		}
		counter := &countingReader{r: file}
		reader := bufio.NewReaderSize(counter, 256<<10)
		number := base
		if offset == 0 {
			for discarded := int64(0); discarded < base; discarded++ {
				if ctx.Err() != nil {
					msg.Canceled = true
					return msg
				}
				if _, err := readFrame(reader); err != nil {
					if errors.Is(err, io.EOF) {
						msg.FileEOF = true
						msg.FileOffset = offset + counter.n - int64(reader.Buffered())
						return msg
					}
					return fail(err)
				}
			}
		}
		rows := make([]zosmf.Record, 0, count)
		for len(rows) < count {
			data, err := readFrame(reader)
			if errors.Is(err, io.EOF) {
				msg.FileEOF = true
				break
			}
			if err != nil {
				return fail(err)
			}
			number++
			rows = append(rows, zosmf.Record{Number: number, Data: data})
		}
		msg.FileOffset = offset + counter.n - int64(reader.Buffered())
		evaluateRecords(ctx, compiled, ov, rows, lineRoom, &msg)
		return msg
	}
}

// evalQueryArrayFile is the whole-data-set array fallback sourced from the
// bulk download instead of the workspace cache.
func evalQueryArrayFile(ctx context.Context, compiled *query.Query, ov *overlay, path string, skip int64, profile string, generation uint64) tea.Cmd {
	return func() tea.Msg {
		msg := queryEvalMsg{Profile: profile, Generation: generation, Array: true}
		frames, err := readSkippedFrames(ctx, path, skip)
		if err != nil {
			if ctx.Err() != nil {
				msg.Canceled = true
			} else {
				msg.LastError = err.Error()
			}
			return msg
		}
		values := make([]any, 0, len(frames))
		for _, data := range frames {
			if ctx.Err() != nil {
				msg.Canceled = true
				return msg
			}
			if value, ok := decodeQueryValue(ov, data, &msg); ok {
				values = append(values, value)
			}
		}
		runQueryArray(ctx, compiled, values, &msg)
		return msg
	}
}

// readSkippedFrames loads every frame past the skipped prefix, for the
// whole-data-set array fallback after a bulk download.
func readSkippedFrames(ctx context.Context, path string, skip int64) ([][]byte, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	reader := bufio.NewReaderSize(file, 256<<10)
	var frames [][]byte
	for index := int64(0); ; index++ {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		data, err := readFrame(reader)
		if errors.Is(err, io.EOF) {
			return frames, nil
		}
		if err != nil {
			return nil, err
		}
		if index < skip {
			continue
		}
		frames = append(frames, data)
	}
}
