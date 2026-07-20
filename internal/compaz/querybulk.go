package compaz

import (
	"bufio"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"os"

	tea "charm.land/bubbletea/v2"

	"github.com/Tannex/cq/internal/query"
	"github.com/Tannex/cq/internal/zosmf"
)

// queryBulkMaxRecord mirrors the transport's per-record bound when parsing
// frames back out of the downloaded file.
const queryBulkMaxRecord = 16 << 20

type queryBulkMsg struct {
	Profile    string
	Generation uint64
	Path       string
	Err        error
}

// startQueryBulkDownload pulls every record of the browsed data set in one
// record-mode request into a temp file. The query evaluates from that file,
// so the host is asked exactly once instead of page by page.
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
	return evalQueryArrayFile(popup.ctx, popup.compiled, ws.overlay, msg.Path, ws.profile, popup.generation)
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

// evalQueryArrayFile decodes every frame of the downloaded record file and
// runs the expression once with the whole data set as one JSON array.
func evalQueryArrayFile(ctx context.Context, compiled *query.Query, ov *overlay, path, profile string, generation uint64) tea.Cmd {
	return func() tea.Msg {
		msg := queryEvalMsg{Profile: profile, Generation: generation}
		file, err := os.Open(path)
		if err != nil {
			msg.LastError = err.Error()
			return msg
		}
		defer file.Close()
		reader := bufio.NewReaderSize(file, 256<<10)
		var values []any
		for {
			if ctx.Err() != nil {
				msg.Canceled = true
				return msg
			}
			data, err := readFrame(reader)
			if errors.Is(err, io.EOF) {
				break
			}
			if err != nil {
				msg.LastError = err.Error()
				return msg
			}
			msg.Evaluated++
			if value, ok := decodeQueryValue(ov, data, &msg); ok {
				values = append(values, value)
			}
		}
		runQueryArray(ctx, compiled, values, &msg)
		return msg
	}
}
