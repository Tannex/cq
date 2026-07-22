package compaz

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"strconv"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/Tannex/cq/internal/zosmf"
)

// spoolBulkMaxBytes caps the whole-file cache; longer files are truncated
// and flagged so the commands can report the tail is missing.
const spoolBulkMaxBytes = 32 << 20

// spoolBulkPageSize is the drain fallback's page size for sessions whose
// browser cannot stream.
const spoolBulkPageSize = 1000

type spoolBulkResultMsg struct {
	Profile    string
	Identity   string
	Generation uint64
	Lines      []string
	Truncated  bool
	Command    string
	Err        error
}

// startSpoolBulk downloads the whole spool file into the workspace cache and
// re-runs command once it lands. Sessions with a SpoolStreamer get it in one
// request; others drain bounded ReadSpoolContent pages.
func (m *Model) startSpoolBulk(ws *workspace, command string) tea.Cmd {
	jobs, ok := ws.browser.(zosmf.JobBrowser)
	if !ok || ws.job.JobName == "" || ws.spoolFile == nil {
		ws.status = status{Level: statusError, Text: "no spool file open"}
		return nil
	}
	ws.dropSpoolBulk()
	ws.spoolPendingCommand = command
	identity := ws.spoolContentIdentity()
	profile, generation := ws.profile, ws.spoolBulkGeneration
	jobName, jobID, fileID := ws.job.JobName, ws.job.JobID, spoolFileKey(*ws.spoolFile)
	ctx, cancel := context.WithCancel(context.Background())
	ws.spoolBulkCancel = cancel
	ws.status = status{Level: statusLoading, Text: fmt.Sprintf("downloading spool file %s", identity)}
	streamer, canStream := ws.browser.(zosmf.SpoolStreamer)
	return m.loadingCommand(func() tea.Msg {
		msg := spoolBulkResultMsg{Profile: profile, Identity: identity, Generation: generation, Command: command}
		if canStream {
			msg.Lines, msg.Truncated, msg.Err = streamSpoolBulk(ctx, streamer, jobName, jobID, fileID)
		} else {
			msg.Lines, msg.Truncated, msg.Err = drainSpoolBulk(ctx, jobs, jobName, jobID, fileID)
		}
		return msg
	})
}

// streamSpoolBulk reads the whole spool file from one streaming request.
func streamSpoolBulk(ctx context.Context, streamer zosmf.SpoolStreamer, jobName, jobID, fileID string) ([]string, bool, error) {
	stream, err := streamer.OpenSpoolContent(ctx, jobName, jobID, fileID)
	if err != nil {
		return nil, false, err
	}
	defer stream.Close()
	reader := bufio.NewReaderSize(stream, 256<<10)
	var lines []string
	total := 0
	for {
		if err := ctx.Err(); err != nil {
			return nil, false, err
		}
		line, err := reader.ReadString('\n')
		if line != "" {
			text := strings.TrimSuffix(line, "\n")
			total += len(text) + 1
			if total > spoolBulkMaxBytes {
				return lines, true, nil
			}
			lines = append(lines, text)
		}
		if err == io.EOF {
			return lines, false, nil
		}
		if err != nil {
			return nil, false, err
		}
	}
}

// drainSpoolBulk collects the whole spool file through bounded page reads —
// correct but slower than streaming, for sessions without a SpoolStreamer.
func drainSpoolBulk(ctx context.Context, jobs zosmf.JobBrowser, jobName, jobID, fileID string) ([]string, bool, error) {
	var lines []string
	total := 0
	start := int64(0)
	for {
		page, err := jobs.ReadSpoolContent(ctx, zosmf.ReadSpoolContentRequest{
			JobName: jobName, JobID: jobID, FileID: fileID,
			Start: start, MaxItems: spoolBulkPageSize,
		})
		if err != nil {
			return nil, false, err
		}
		for _, line := range page.Lines {
			total += len(line) + 1
			if total > spoolBulkMaxBytes {
				return lines, true, nil
			}
			lines = append(lines, line)
		}
		if len(page.Lines) == 0 || !page.MoreRows {
			return lines, false, nil
		}
		start += int64(len(page.Lines))
	}
}

// handleSpoolBulkResult stores a finished download and re-runs the command
// that was waiting for it. Stale generations and identities are dropped.
func (m *Model) handleSpoolBulkResult(ws *workspace, msg spoolBulkResultMsg) tea.Cmd {
	if msg.Generation != ws.spoolBulkGeneration || msg.Identity != ws.spoolContentIdentity() {
		return nil
	}
	ws.spoolBulkCancel = nil
	command := ws.spoolPendingCommand
	ws.spoolPendingCommand = ""
	if msg.Err != nil {
		ws.status = status{Level: statusError, Text: "spool download failed: " + msg.Err.Error()}
		return nil
	}
	ws.spoolBulk = msg.Lines
	if ws.spoolBulk == nil {
		ws.spoolBulk = []string{}
	}
	ws.spoolBulkIdentity = msg.Identity
	ws.spoolBulkTruncated = msg.Truncated
	ws.spoolBulkScanned = 0
	ws.spoolFilterHits = nil
	ws.spoolFilterCursor = 0
	ws.refilterSpool()
	if command == "" || ws != &m.workspace {
		return nil
	}
	return m.applySpoolCommand(ws, command)
}

// applySpoolCommand executes an already-validated incl/f command against the
// ready bulk cache.
func (m *Model) applySpoolCommand(ws *workspace, command string) tea.Cmd {
	verb, argument, _ := strings.Cut(command, " ")
	argument = strings.TrimSpace(argument)
	switch verb {
	case "incl":
		ws.setSpoolInclude(argument)
		if argument == "" {
			ws.status = status{Level: statusReady, Text: "include filter cleared"}
			return nil
		}
		text := fmt.Sprintf("incl %q: %d of %d lines", argument, len(ws.spoolFilterHits), len(ws.spoolBulk))
		if ws.spoolBulkTruncated {
			text += " (cache truncated)"
		}
		ws.status = status{Level: statusReady, Text: text}
		return nil
	case "f":
		ws.spoolFind = argument
		return m.spoolFindNext()
	}
	return nil
}

// spoolFindNext jumps to the next line containing the last f pattern. With a
// filter active it advances through the filter hits; otherwise it scans the
// whole cached file from the current line, wrapping past the end.
func (m *Model) spoolFindNext() tea.Cmd {
	ws := m.ws()
	if ws.spoolFind == "" {
		ws.status = status{Level: statusWarn, Text: "no find pattern; use f <pattern>"}
		return nil
	}
	if !ws.spoolBulkReady() {
		return m.startSpoolBulk(ws, "f "+ws.spoolFind)
	}
	pattern := strings.ToUpper(ws.spoolFind)
	if ws.spoolInclude != "" {
		hits := ws.spoolFilterHits
		for offset := 1; offset <= len(hits); offset++ {
			position := (ws.spoolFilterCursor + offset) % len(hits)
			if !strings.Contains(strings.ToUpper(ws.spoolBulk[hits[position]]), pattern) {
				continue
			}
			wrapped := position <= ws.spoolFilterCursor
			ws.spoolFilterCursor = position
			ws.status = spoolFindStatus(ws, int64(hits[position]), wrapped)
			return nil
		}
		ws.status = spoolNotFoundStatus(ws, "in filtered lines")
		return nil
	}

	total := len(ws.spoolBulk)
	if total == 0 {
		ws.status = status{Level: statusWarn, Text: "spool file is empty"}
		return nil
	}
	current := int64(-1)
	if index := ws.spoolContentPage.selectedIndex(); index >= 0 && index < len(ws.spoolContent) {
		current = ws.spoolContent[index].Number
	}
	for offset := int64(1); offset <= int64(total); offset++ {
		number := ((current+offset)%int64(total) + int64(total)) % int64(total)
		if !strings.Contains(strings.ToUpper(ws.spoolBulk[number]), pattern) {
			continue
		}
		ws.status = spoolFindStatus(ws, number, number <= current)
		return m.spoolJumpTo(number)
	}
	ws.status = spoolNotFoundStatus(ws, "")
	return nil
}

func spoolFindStatus(ws *workspace, number int64, wrapped bool) status {
	text := fmt.Sprintf("line %d matches %q", number, ws.spoolFind)
	if wrapped {
		text += " (wrapped)"
	}
	return status{Level: statusReady, Text: text}
}

func spoolNotFoundStatus(ws *workspace, scope string) status {
	text := fmt.Sprintf("%q not found", ws.spoolFind)
	if scope != "" {
		text += " " + scope
	}
	if ws.spoolBulkTruncated {
		text += " (cache truncated)"
	}
	return status{Level: statusWarn, Text: text}
}

// spoolJumpTo selects a line already in the pager window or re-anchors the
// window on it, without evicting the bulk cache.
func (m *Model) spoolJumpTo(number int64) tea.Cmd {
	ws := m.ws()
	if ws.spoolContentPage.selectKey(strconv.FormatInt(number, 10)) {
		return m.maybePrefetch(ws)
	}
	ws.cancelBrowse()
	ws.resetSpoolWindow()
	return m.startSpoolContent(ws, ws.spoolContentPage.initialPlan(number))
}

// spoolFilteredMove steps the filter cursor while the include filter is
// active; handled is false when the default selection movement should run.
func (m *Model) spoolFilteredMove(delta int) (tea.Cmd, bool) {
	ws := m.ws()
	if ws.screen != ScreenSpoolContent || ws.spoolInclude == "" || !ws.spoolBulkReady() {
		return nil, false
	}
	if len(ws.spoolFilterHits) == 0 {
		return nil, true
	}
	ws.spoolFilterCursor = max(0, min(ws.spoolFilterCursor+delta, len(ws.spoolFilterHits)-1))
	return nil, true
}
