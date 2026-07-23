package compaz

import (
	"context"
	"fmt"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/Tannex/cq/internal/decode"
	"github.com/Tannex/cq/internal/zosmf"
)

// defaultFollowInterval paces the idle polls of follow mode. z/OSMF has no
// push or long-poll interface, so tailing a running job is bounded polling.
const defaultFollowInterval = 2 * time.Second

// spoolFreshFadeDuration is how long a follow-appended line stays
// highlighted before it has fully faded to the default style;
// defaultFollowFadeStep paces the re-render ticks that step the fade.
const (
	spoolFreshFadeDuration = 2 * time.Second
	defaultFollowFadeStep  = 250 * time.Millisecond
)

// spoolFreshBatch marks one follow poll's appended lines for the fade
// highlight: every bulk line from FirstIndex up to the next batch (or the
// cache end) arrived at At.
type spoolFreshBatch struct {
	FirstIndex int
	At         time.Time
}

// spoolFadeTickMsg re-renders the follow view while fresh-line highlights
// are still fading; carrying no state of its own, it only needs to survive
// the generation check.
type spoolFadeTickMsg struct {
	Profile    string
	Generation uint64
}

// spoolFollowTickMsg wakes an idle follower for its next poll.
type spoolFollowTickMsg struct {
	Profile    string
	Identity   string
	Generation uint64
}

// IsSpoolFollowTick reports whether msg is one of follow mode's
// self-rescheduling ticks (the idle poll or the highlight fade). Harnesses
// that drain commands synchronously (render-shot) must stop at them: both
// reschedule themselves by design.
func IsSpoolFollowTick(msg tea.Msg) bool {
	switch msg.(type) {
	case spoolFollowTickMsg, spoolFadeTickMsg:
		return true
	}
	return false
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
	if ws.spoolBulkTruncated {
		// A truncated cache holds only a prefix: the follower would poll from
		// the wrong position and the first append would hit the cap anyway.
		ws.status = status{Level: statusWarn, Text: "spool file exceeds the cache limit; cannot follow"}
		return nil
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
		cmd := m.anchorSpoolTail(ws)
		ws.status = status{Level: statusWarn, Text: "job no longer exists; follow stopped"}
		return cmd
	}
	if msg.Err != nil {
		ws.status = status{Level: statusWarn, Text: "follow poll failed: " + msg.Err.Error() + " (retrying)"}
		return m.scheduleSpoolFollowTick(ws)
	}
	firstNew := len(ws.spoolBulk)
	wasEmpty := len(ws.spoolFresh) == 0
	for _, line := range msg.Lines {
		line = decode.DisplayText(line)
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
	if len(ws.spoolBulk) > firstNew {
		ws.markSpoolFresh(firstNew, time.Now())
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
			// Re-anchor on the tail so the final drained lines are visible;
			// without this the pager falls back to the pre-follow window.
			cmd := m.anchorSpoolTail(ws)
			text := "job ended"
			if msg.JobRC != "" {
				text += " (" + msg.JobRC + ")"
			}
			ws.status = status{Level: statusReady, Text: text + " — follow stopped"}
			return cmd
		}
	}
	ws.status = status{Level: statusReady, Text: spoolFollowStatus(ws)}
	// Seed a fade chain only on the empty-to-non-empty transition of the
	// fresh marks; handleSpoolFadeTick keeps exactly one chain alive after
	// that, so no extra bookkeeping flag is needed.
	var fade tea.Cmd
	if wasEmpty && len(ws.spoolFresh) > 0 {
		fade = m.scheduleSpoolFadeTick(ws)
	}
	if msg.More {
		return tea.Batch(m.pollSpoolFollow(ws), fade)
	}
	return tea.Batch(m.scheduleSpoolFollowTick(ws), fade)
}

func (m *Model) scheduleSpoolFollowTick(ws *workspace) tea.Cmd {
	msg := spoolFollowTickMsg{Profile: ws.profile, Identity: ws.spoolContentIdentity(), Generation: ws.spoolBulkGeneration}
	return tea.Tick(m.deps.FollowInterval, func(time.Time) tea.Msg { return msg })
}

// markSpoolFresh records lines [firstIndex, len(spoolBulk)) as freshly
// appended, starting their fade clock.
func (ws *workspace) markSpoolFresh(firstIndex int, at time.Time) {
	ws.spoolFresh = append(ws.spoolFresh, spoolFreshBatch{FirstIndex: firstIndex, At: at})
}

// pruneSpoolFresh drops batches whose fade has completed.
func (ws *workspace) pruneSpoolFresh(now time.Time) {
	keep := ws.spoolFresh[:0]
	for _, batch := range ws.spoolFresh {
		if now.Sub(batch.At) < spoolFreshFadeDuration {
			keep = append(keep, batch)
		}
	}
	if len(keep) == 0 {
		ws.spoolFresh = nil
		return
	}
	ws.spoolFresh = keep
}

// spoolFreshAge reports how long ago the bulk line at index arrived via a
// follow poll; ok is false for lines that predate follow mode or whose
// highlight has already faded out and been pruned.
func (ws *workspace) spoolFreshAge(index int, now time.Time) (time.Duration, bool) {
	// Batches are appended in arrival order, so the newest batch at or below
	// index owns it.
	for i := len(ws.spoolFresh) - 1; i >= 0; i-- {
		if index >= ws.spoolFresh[i].FirstIndex {
			return now.Sub(ws.spoolFresh[i].At), true
		}
	}
	return 0, false
}

// scheduleSpoolFadeTick arms the next fade re-render tick while any
// fresh-line highlight is still fading. Chains are seeded by
// handleSpoolFollowResult on the empty-to-non-empty transition of spoolFresh
// and kept alive here; a chain ends when the last batch is pruned (or its
// generation dies), so exactly one chain runs per workspace.
func (m *Model) scheduleSpoolFadeTick(ws *workspace) tea.Cmd {
	if len(ws.spoolFresh) == 0 {
		return nil
	}
	msg := spoolFadeTickMsg{Profile: ws.profile, Generation: ws.spoolBulkGeneration}
	return tea.Tick(m.deps.FollowFadeStep, func(time.Time) tea.Msg { return msg })
}

// handleSpoolFadeTick re-renders (by virtue of being a message), prunes
// finished highlights, and keeps ticking until the last one has faded.
func (m *Model) handleSpoolFadeTick(ws *workspace, msg spoolFadeTickMsg) tea.Cmd {
	if msg.Generation != ws.spoolBulkGeneration {
		return nil
	}
	ws.pruneSpoolFresh(time.Now())
	return m.scheduleSpoolFadeTick(ws)
}

func spoolFollowStatus(ws *workspace) string {
	if ws.spoolInclude != "" {
		return fmt.Sprintf("following — %d of %d lines match incl %q", len(ws.spoolFilterHits), len(ws.spoolBulk), ws.spoolInclude)
	}
	return fmt.Sprintf("following — %d lines", len(ws.spoolBulk))
}

// anchorSpoolTail re-anchors the pager on a full window ending at the cached
// tail — the lines the user was just watching — with the tail selected.
// Follow mode only runs over a complete cache, so startSpoolContent resolves
// synchronously and the pager is populated on return; the command carries
// any follow-up work (nil in practice at the cache end).
func (m *Model) anchorSpoolTail(ws *workspace) tea.Cmd {
	if ws != &m.workspace || ws.spoolInclude != "" || len(ws.spoolBulk) == 0 {
		// The filtered view keeps its own cursor, and background workspaces
		// re-anchor when they next become active; nothing to do.
		return nil
	}
	tail := int64(len(ws.spoolBulk) - 1)
	ws.cancelBrowse()
	ws.resetSpoolWindow()
	// A window ending on the tail, not starting at it, keeps the context the
	// user was watching instead of a single-line window.
	cmd := m.startSpoolContent(ws, ws.spoolContentPage.initialPlan(max(0, tail-int64(m.budget)+1)))
	ws.spoolContentPage.bottom()
	return cmd
}

// stopSpoolFollowNavigation ends follow mode and re-anchors the pager on the
// tail line the user was watching, so the view does not snap back to
// wherever the fetch window happened to be.
func (m *Model) stopSpoolFollowNavigation(ws *workspace) tea.Cmd {
	ws.stopSpoolFollow()
	cmd := m.anchorSpoolTail(ws)
	ws.status = status{Level: statusReady, Text: "follow stopped"}
	return cmd
}

// spoolFollowStopActions are the keys whose first press exits follow mode
// (the less +F convention). Navigation actions go on to perform their
// movement from the re-anchored tail; Back alone is fully consumed, so Esc
// ends the mode without also leaving the screen.
var spoolFollowStopActions = map[action]bool{
	actionUp: true, actionDown: true, actionPageUp: true, actionPageDown: true,
	actionTop: true, actionBottom: true, actionBack: true,
}

// spoolFollowInterrupt ends follow mode when selectedAction is one of its
// stop keys. consumed reports the key was fully handled here; otherwise the
// caller applies the same key's movement against the re-anchored window (or
// the filtered cursor) — the exit keypress is not swallowed.
func (m *Model) spoolFollowInterrupt(selectedAction action) (tea.Cmd, bool) {
	ws := m.ws()
	if ws.screen != ScreenSpoolContent || !ws.spoolFollow || !spoolFollowStopActions[selectedAction] {
		return nil, false
	}
	cmd := m.stopSpoolFollowNavigation(ws)
	switch {
	case selectedAction == actionBack:
		// Esc is a pure stop: end the mode, stay on the screen.
		return cmd, true
	case selectedAction == actionTop && ws.spoolInclude == "":
		// Top means the file top (less's g after F); the tail anchor the
		// stop just placed is the wrong starting point for pager movement,
		// so jump straight there from the cache. With a filter active the
		// fall-through filtered movement already goes to the first hit.
		return m.spoolJumpTo(0), true
	}
	// cmd is the anchor's follow-up work, nil at the cache end; the caller's
	// movement command takes precedence.
	_ = cmd
	return nil, false
}
