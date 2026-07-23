package compaz

import (
	"context"
	"fmt"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/Tannex/cq/internal/zosmf"
)

// defaultFollowInterval paces the idle polls of follow mode. z/OSMF has no
// push or long-poll interface, so tailing a running job is bounded polling.
const defaultFollowInterval = 2 * time.Second

// spoolFollowTickMsg wakes an idle follower for its next poll.
type spoolFollowTickMsg struct {
	Profile    string
	Identity   string
	Generation uint64
}

// IsSpoolFollowTick reports whether msg is the spool follower's idle tick.
// Harnesses that drain commands synchronously (render-shot) must stop at it:
// follow mode reschedules forever by design.
func IsSpoolFollowTick(msg tea.Msg) bool {
	_, ok := msg.(spoolFollowTickMsg)
	return ok
}

// spoolFollowResultMsg carries one poll's outcome. Lines are the file's
// growth past the bulk cache; More reports a full batch, so an immediate
// re-poll is likely to yield further lines. After an idle poll the job's
// status document is re-checked (when the session can): JobChecked carries
// its result, JobGone reports the job no longer exists.
type spoolFollowResultMsg struct {
	Profile    string
	Identity   string
	Generation uint64
	Lines      []string
	More       bool
	Err        error
	JobChecked bool
	JobGone    bool
	JobStatus  string
	JobRC      string
}

// startSpoolFollow enters follow mode: the whole file is cached first (the
// same bulk download incl/f use), then a follower polls for appended lines,
// folding them into the cache so the include filter composes incrementally.
func (m *Model) startSpoolFollow(ws *workspace) tea.Cmd {
	jobs, ok := ws.browser.(zosmf.JobBrowser)
	if !ok || ws.job.JobName == "" || ws.spoolFile == nil {
		ws.status = status{Level: statusError, Text: "no spool file open"}
		return nil
	}
	if !ws.spoolBulkReady() {
		return m.startSpoolBulk(ws, "follow")
	}
	follower := zosmf.FollowSpool(jobs, ws.job.JobName, ws.job.JobID, spoolFileKey(*ws.spoolFile), spoolBulkPageSize)
	follower.SetPosition(int64(len(ws.spoolBulk)))
	total := 0
	for _, line := range ws.spoolBulk {
		total += len(line) + 1
	}
	ws.spoolBulkBytes = total
	ws.spoolFollower = follower
	ws.spoolFollow = true
	ws.status = status{Level: statusReady, Text: spoolFollowStatus(ws)}
	return m.pollSpoolFollow(ws)
}

// pollSpoolFollow runs one poll asynchronously. Only one poll is ever in
// flight: the next is scheduled exclusively from this poll's result, and
// stale results are dropped by the generation check.
func (m *Model) pollSpoolFollow(ws *workspace) tea.Cmd {
	follower := ws.spoolFollower
	if follower == nil {
		return nil
	}
	profile, identity, generation := ws.profile, ws.spoolContentIdentity(), ws.spoolBulkGeneration
	statusReader, _ := ws.browser.(zosmf.JobStatusReader)
	jobName, jobID := ws.job.JobName, ws.job.JobID
	ctx, cancel := context.WithTimeout(context.Background(), m.deps.Timeout)
	ws.spoolFollowCancel = cancel
	return func() tea.Msg {
		defer cancel()
		msg := spoolFollowResultMsg{Profile: profile, Identity: identity, Generation: generation}
		msg.Lines, msg.More, msg.Err = follower.Poll(ctx)
		if msg.Err != nil || len(msg.Lines) > 0 || statusReader == nil {
			return msg
		}
		// Idle cycle: re-check whether the job is still producing output at
		// all. Errors other than "gone" are ignored — the next tick retries.
		job, err := statusReader.ReadJobStatus(ctx, jobName, jobID)
		switch {
		case zosmf.IsNotFound(err):
			msg.JobGone = true
		case err == nil:
			msg.JobChecked = true
			msg.JobStatus, msg.JobRC = job.Status, job.ReturnCode
			if jobFinished(job.Status) {
				// The job may have flushed final lines between the empty
				// poll and the status read; drain once more before the
				// handler stops following.
				if final, _, err := follower.Poll(ctx); err == nil {
					msg.Lines = final
				}
			}
		}
		return msg
	}
}

// jobFinished reports a status document that says the job will write no
// further output. INPUT and ACTIVE keep following; unknown values do too,
// conservatively.
func jobFinished(jobStatus string) bool {
	return strings.EqualFold(strings.TrimSpace(jobStatus), "OUTPUT")
}

func (m *Model) handleSpoolFollowTick(ws *workspace, msg spoolFollowTickMsg) tea.Cmd {
	if msg.Generation != ws.spoolBulkGeneration || msg.Identity != ws.spoolContentIdentity() || !ws.spoolFollow {
		return nil
	}
	return m.pollSpoolFollow(ws)
}

// handleSpoolFollowResult folds polled lines into the bulk cache and
// schedules the next poll: immediately after a full batch, on the idle
// cadence otherwise. A failed poll did not advance the follower, so it is
// simply retried on the same cadence.
func (m *Model) handleSpoolFollowResult(ws *workspace, msg spoolFollowResultMsg) tea.Cmd {
	if msg.Generation != ws.spoolBulkGeneration || msg.Identity != ws.spoolContentIdentity() || !ws.spoolFollow {
		return nil
	}
	ws.spoolFollowCancel = nil
	if msg.JobGone || zosmf.IsNotFound(msg.Err) {
		ws.stopSpoolFollow()
		ws.status = status{Level: statusWarn, Text: "job no longer exists; follow stopped"}
		return nil
	}
	if msg.Err != nil {
		ws.status = status{Level: statusWarn, Text: "follow poll failed: " + msg.Err.Error() + " (retrying)"}
		return m.scheduleSpoolFollowTick(ws)
	}
	for _, line := range msg.Lines {
		ws.spoolBulkBytes += len(line) + 1
		if ws.spoolBulkBytes > spoolBulkMaxBytes {
			ws.spoolBulkTruncated = true
			ws.stopSpoolFollow()
			ws.status = status{Level: statusWarn, Text: "cache full; follow stopped"}
			return nil
		}
		ws.spoolBulk = append(ws.spoolBulk, line)
		ws.spoolBulkUpper = append(ws.spoolBulkUpper, strings.ToUpper(line))
	}
	ws.refilterSpool()
	// Tail behavior: the filtered view stays pinned to the newest hit.
	if ws.spoolInclude != "" && len(ws.spoolFilterHits) > 0 {
		ws.spoolFilterCursor = len(ws.spoolFilterHits) - 1
	}
	if msg.JobChecked {
		// Fold the fresh status document into the job row the spool screens
		// display, whether or not it ends the follow.
		ws.job.Status = msg.JobStatus
		if msg.JobRC != "" {
			ws.job.ReturnCode = msg.JobRC
		}
		if jobFinished(msg.JobStatus) {
			ws.stopSpoolFollow()
			text := "job ended"
			if msg.JobRC != "" {
				text += " (" + msg.JobRC + ")"
			}
			ws.status = status{Level: statusReady, Text: text + " — follow stopped"}
			return nil
		}
	}
	ws.status = status{Level: statusReady, Text: spoolFollowStatus(ws)}
	if msg.More {
		return m.pollSpoolFollow(ws)
	}
	return m.scheduleSpoolFollowTick(ws)
}

func (m *Model) scheduleSpoolFollowTick(ws *workspace) tea.Cmd {
	msg := spoolFollowTickMsg{Profile: ws.profile, Identity: ws.spoolContentIdentity(), Generation: ws.spoolBulkGeneration}
	return tea.Tick(m.deps.FollowInterval, func(time.Time) tea.Msg { return msg })
}

func spoolFollowStatus(ws *workspace) string {
	if ws.spoolInclude != "" {
		return fmt.Sprintf("following — %d of %d lines match incl %q", len(ws.spoolFilterHits), len(ws.spoolBulk), ws.spoolInclude)
	}
	return fmt.Sprintf("following — %d lines", len(ws.spoolBulk))
}

// stopSpoolFollowNavigation ends follow mode and re-anchors the pager on the
// tail line the user was watching, so the view does not snap back to
// wherever the fetch window happened to be.
func (m *Model) stopSpoolFollowNavigation(ws *workspace) tea.Cmd {
	ws.stopSpoolFollow()
	ws.status = status{Level: statusReady, Text: "follow stopped"}
	if ws != &m.workspace || ws.spoolInclude != "" || len(ws.spoolBulk) == 0 {
		// The filtered view keeps its own cursor; nothing to re-anchor.
		return nil
	}
	return m.spoolJumpTo(int64(len(ws.spoolBulk) - 1))
}

// spoolFollowInterrupt consumes a navigation key while follow mode is
// active: the first key stops following (the less +F convention).
func (m *Model) spoolFollowInterrupt() (tea.Cmd, bool) {
	ws := m.ws()
	if ws.screen != ScreenSpoolContent || !ws.spoolFollow {
		return nil, false
	}
	return m.stopSpoolFollowNavigation(ws), true
}
