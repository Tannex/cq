package compaz

import (
	"context"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/Tannex/cq/internal/zosmf"
)

// spoolFollowModel builds a model on the spool content screen whose fake
// browser serves *content — tests grow the slice to simulate a running job
// appending output.
func spoolFollowModel(t *testing.T, content *[]string) *Model {
	t.Helper()
	browser := &fakeBrowser{
		listJobs: func(context.Context, zosmf.ListJobsRequest) (zosmf.JobPage, error) {
			return zosmf.JobPage{Items: []zosmf.Job{{JobID: "JOB00001", JobName: "NIGHTBAT"}}}, nil
		},
		listSpoolFiles: func(context.Context, string, string) ([]zosmf.SpoolFile, error) {
			return []zosmf.SpoolFile{{ID: 2, DDName: "SYSPRINT"}}, nil
		},
		readSpoolContent: func(_ context.Context, request zosmf.ReadSpoolContentRequest) (zosmf.SpoolContentPage, error) {
			lines := *content
			start := min(int(request.Start), len(lines))
			end := min(start+request.MaxItems, len(lines))
			return zosmf.SpoolContentPage{Lines: lines[start:end], Start: request.Start, MoreRows: end < len(lines)}, nil
		},
	}
	model := readyModel(t, Options{}, browser, "IBMUSER", "", 90, 16)
	model.deps.FollowInterval = time.Millisecond
	model.deps.FollowFadeStep = time.Millisecond
	executeCommand(t, model, model.handleAction(actionJobs))
	executeCommand(t, model, model.openSelection())
	model.spoolFilePage.move(1)
	executeCommand(t, model, model.openSelection())
	if model.screen != ScreenSpoolContent {
		t.Fatalf("did not reach spool content, screen=%d", model.screen)
	}
	return model
}

// drainSpoolFollow executes a command tree but stops at the follower's idle
// tick instead of applying it — follow mode reschedules forever, so a full
// drain would never terminate. Reports whether a tick was reached.
func drainSpoolFollow(t *testing.T, model *Model, command tea.Cmd) bool {
	t.Helper()
	return walkMessages(command, func(message tea.Msg) (tea.Cmd, bool) {
		if _, ok := message.(spoolFollowTickMsg); ok {
			return nil, true
		}
		if _, ok := message.(spoolFadeTickMsg); ok {
			// Skip without applying: the fade chain reschedules itself until
			// real time passes, which a synchronous drain must not wait for.
			return nil, false
		}
		return applyMessage(t, model, message), false
	})
}

func runSpoolFollowCommand(t *testing.T, model *Model, command string) bool {
	t.Helper()
	executeCommand(t, model, applyMessage(t, model, keyPress(':', ":")))
	model.spoolCmdInput.SetValue(command)
	return drainSpoolFollow(t, model, applyMessage(t, model, keyPress(tea.KeyEnter, "")))
}

func spoolFollowTick(t *testing.T, model *Model) bool {
	t.Helper()
	tick := spoolFollowTickMsg{
		Profile:    model.profile,
		Identity:   model.spoolContentIdentity(),
		Generation: model.spoolBulkGeneration,
	}
	return drainSpoolFollow(t, model, applyMessage(t, model, tick))
}

func TestSpoolFollowTailsAppendedLines(t *testing.T) {
	content := []string{"one", "two", "three"}
	model := spoolFollowModel(t, &content)

	if !runSpoolFollowCommand(t, model, "follow") {
		t.Fatal("follow did not reach an idle tick")
	}
	if !model.spoolFollow || len(model.spoolBulk) != 3 {
		t.Fatalf("follow=%v bulk=%v", model.spoolFollow, model.spoolBulk)
	}
	if !strings.Contains(model.status.Text, "following — 3 lines") {
		t.Fatalf("status = %q", model.status.Text)
	}
	view := ansi.Strip(model.mainView())
	if !strings.Contains(view, "SPOOL CONTENT (following)") || !strings.Contains(view, "three") {
		t.Fatalf("follow view missing tail:\n%s", view)
	}
	if !strings.Contains(view, "· following") {
		t.Fatalf("title missing follow marker:\n%s", view)
	}

	// The job writes more output; the next poll picks it up and the view
	// stays pinned to the newest line.
	content = append(content, "four", "five")
	if !spoolFollowTick(t, model) {
		t.Fatal("poll after growth did not reschedule")
	}
	if len(model.spoolBulk) != 5 {
		t.Fatalf("bulk after growth = %v", model.spoolBulk)
	}
	view = ansi.Strip(model.mainView())
	if !strings.Contains(view, "five") {
		t.Fatalf("follow view missing appended tail:\n%s", view)
	}
	if !strings.Contains(model.status.Text, "following — 5 lines") {
		t.Fatalf("status = %q", model.status.Text)
	}
}

func TestSpoolFollowComposesWithIncludeFilter(t *testing.T) {
	content := []string{"alpha", "ERROR one", "beta"}
	model := spoolFollowModel(t, &content)
	runSpoolCommand(t, model, "incl error")
	if !runSpoolFollowCommand(t, model, "follow") {
		t.Fatal("follow did not reach an idle tick")
	}

	content = append(content, "gamma", "error two")
	if !spoolFollowTick(t, model) {
		t.Fatal("poll after growth did not reschedule")
	}
	if len(model.spoolFilterHits) != 2 || model.spoolFilterHits[1] != 4 {
		t.Fatalf("filter hits = %v, want the appended match", model.spoolFilterHits)
	}
	if model.spoolFilterCursor != 1 {
		t.Fatalf("cursor = %d, want pinned to the newest hit", model.spoolFilterCursor)
	}
	view := ansi.Strip(model.mainView())
	if !strings.Contains(view, "error two") || strings.Contains(view, "gamma") {
		t.Fatalf("filtered follow view wrong:\n%s", view)
	}
	if !strings.Contains(model.status.Text, "2 of 5 lines") {
		t.Fatalf("status = %q", model.status.Text)
	}
}

func TestSpoolFollowStopsOnNavigationAndToggle(t *testing.T) {
	content := []string{"one", "two", "three"}
	model := spoolFollowModel(t, &content)
	if !runSpoolFollowCommand(t, model, "follow") {
		t.Fatal("follow did not reach an idle tick")
	}

	// The first navigation key stops following, anchors the pager on the
	// tail line the user was watching, and still performs its own movement —
	// the exit keypress is not swallowed.
	executeCommand(t, model, model.handleAction(actionUp))
	if model.spoolFollow {
		t.Fatal("navigation did not stop follow mode")
	}
	if got := model.spoolContentPage.selectedIndex(); model.spoolContent[got].Number != 1 {
		t.Fatalf("selection = line %d, want line 1 (tail line 2, then the Up moved)", model.spoolContent[got].Number)
	}
	if len(model.spoolContent) != 3 {
		t.Fatalf("window = %d lines, want the full tail context from the cache", len(model.spoolContent))
	}

	// :follow toggles back on, and again off.
	if !runSpoolFollowCommand(t, model, "follow") {
		t.Fatal("re-follow did not reach an idle tick")
	}
	if runSpoolFollowCommand(t, model, "follow") {
		t.Fatal("toggle off still scheduled a tick")
	}
	if model.spoolFollow {
		t.Fatal("toggle did not stop follow mode")
	}

	// Stale ticks from the stopped follower are dropped.
	if spoolFollowTick(t, model) {
		t.Fatal("stale tick was not dropped")
	}
}

// followStatusBrowser adds JobStatusReader to fakeBrowser so idle polls
// re-check the job, mirroring sessions whose browser supports it.
type followStatusBrowser struct {
	*fakeBrowser
	readJobStatus func() (zosmf.Job, error)
}

func (b *followStatusBrowser) ReadJobStatus(context.Context, string, string) (zosmf.Job, error) {
	return b.readJobStatus()
}

// spoolFollowStatusModel is spoolFollowModel with a job-status hook.
func spoolFollowStatusModel(t *testing.T, content *[]string, readJobStatus func() (zosmf.Job, error)) *Model {
	t.Helper()
	inner := &fakeBrowser{
		listJobs: func(context.Context, zosmf.ListJobsRequest) (zosmf.JobPage, error) {
			return zosmf.JobPage{Items: []zosmf.Job{{JobID: "JOB00001", JobName: "NIGHTBAT", Status: "ACTIVE"}}}, nil
		},
		listSpoolFiles: func(context.Context, string, string) ([]zosmf.SpoolFile, error) {
			return []zosmf.SpoolFile{{ID: 2, DDName: "SYSPRINT"}}, nil
		},
		readSpoolContent: func(_ context.Context, request zosmf.ReadSpoolContentRequest) (zosmf.SpoolContentPage, error) {
			lines := *content
			start := min(int(request.Start), len(lines))
			end := min(start+request.MaxItems, len(lines))
			return zosmf.SpoolContentPage{Lines: lines[start:end], Start: request.Start, MoreRows: end < len(lines)}, nil
		},
	}
	browser := &followStatusBrowser{fakeBrowser: inner, readJobStatus: readJobStatus}
	model := readyModel(t, Options{}, browser, "IBMUSER", "", 90, 16)
	model.deps.FollowInterval = time.Millisecond
	model.deps.FollowFadeStep = time.Millisecond
	executeCommand(t, model, model.handleAction(actionJobs))
	executeCommand(t, model, model.openSelection())
	model.spoolFilePage.move(1)
	executeCommand(t, model, model.openSelection())
	if model.screen != ScreenSpoolContent {
		t.Fatalf("did not reach spool content, screen=%d", model.screen)
	}
	return model
}

func TestSpoolFollowStopsWhenJobEnds(t *testing.T) {
	content := []string{"one", "two"}
	jobStatus := "ACTIVE"
	model := spoolFollowStatusModel(t, &content, func() (zosmf.Job, error) {
		if jobStatus == "OUTPUT" {
			// The job flushes its last line between the empty poll and the
			// status read; the final drain must still pick it up.
			if content[len(content)-1] != "late line" {
				content = append(content, "late line")
			}
			return zosmf.Job{Status: "OUTPUT", ReturnCode: "CC 0000"}, nil
		}
		return zosmf.Job{Status: jobStatus}, nil
	})

	if !runSpoolFollowCommand(t, model, "follow") {
		t.Fatal("follow did not reach an idle tick while the job is active")
	}
	if !model.spoolFollow {
		t.Fatal("follow stopped while the job was still active")
	}

	jobStatus = "OUTPUT"
	if spoolFollowTick(t, model) {
		t.Fatal("follow kept polling after the job ended")
	}
	if model.spoolFollow {
		t.Fatal("follow still on after the job ended")
	}
	if !strings.Contains(model.status.Text, "job ended (CC 0000) — follow stopped") {
		t.Fatalf("status = %q", model.status.Text)
	}
	if model.spoolBulk[len(model.spoolBulk)-1] != "late line" {
		t.Fatalf("final drain missed the job's last flush: %v", model.spoolBulk)
	}
	if model.job.Status != "OUTPUT" || model.job.ReturnCode != "CC 0000" {
		t.Fatalf("job row not refreshed: %+v", model.job)
	}
}

func TestSpoolFollowStopsWhenJobIsPurged(t *testing.T) {
	content := []string{"one"}
	gone := false
	model := spoolFollowStatusModel(t, &content, func() (zosmf.Job, error) {
		if gone {
			return zosmf.Job{}, &zosmf.HTTPError{StatusCode: 404, Resource: "NIGHTBAT/JOB00001", Message: "job not found"}
		}
		return zosmf.Job{Status: "ACTIVE"}, nil
	})
	if !runSpoolFollowCommand(t, model, "follow") {
		t.Fatal("follow did not reach an idle tick")
	}

	gone = true
	if spoolFollowTick(t, model) {
		t.Fatal("follow kept polling after the job was purged")
	}
	if model.spoolFollow || !strings.Contains(model.status.Text, "job no longer exists; follow stopped") {
		t.Fatalf("follow=%v status=%q", model.spoolFollow, model.status.Text)
	}
}

func TestSpoolFollowStopsWhenSpoolReadReturnsNotFound(t *testing.T) {
	content := []string{"one"}
	model := spoolFollowModel(t, &content)
	if !runSpoolFollowCommand(t, model, "follow") {
		t.Fatal("follow did not reach an idle tick")
	}

	browser := model.browser.(*fakeBrowser)
	browser.readSpoolContent = func(context.Context, zosmf.ReadSpoolContentRequest) (zosmf.SpoolContentPage, error) {
		return zosmf.SpoolContentPage{}, &zosmf.HTTPError{StatusCode: 404, Resource: "NIGHTBAT/JOB00001/2", Message: "job not found"}
	}
	if spoolFollowTick(t, model) {
		t.Fatal("follow kept retrying a purged spool file")
	}
	if model.spoolFollow || !strings.Contains(model.status.Text, "job no longer exists; follow stopped") {
		t.Fatalf("follow=%v status=%q", model.spoolFollow, model.status.Text)
	}
}

func TestSpoolFollowDroppedByRefreshAndBack(t *testing.T) {
	content := []string{"one"}
	model := spoolFollowModel(t, &content)
	if !runSpoolFollowCommand(t, model, "follow") {
		t.Fatal("follow did not reach an idle tick")
	}
	// The first Back is a pure stop: follow ends but the screen and the bulk
	// cache stay, so an accidental Esc does not throw away the download.
	executeCommand(t, model, model.handleAction(actionBack))
	if model.spoolFollow || model.spoolFollower != nil {
		t.Fatal("back did not stop follow mode")
	}
	if model.screen != ScreenSpoolContent || model.spoolBulk == nil {
		t.Fatalf("first back left screen=%d bulk=%v, want to stay on content with the cache", model.screen, model.spoolBulk)
	}

	// The second Back actually leaves the screen and drops the spool state.
	executeCommand(t, model, model.handleAction(actionBack))
	if model.screen != ScreenSpoolFiles || model.spoolBulk != nil {
		t.Fatalf("second back screen=%d bulk=%v, want spool files with the cache dropped", model.screen, model.spoolBulk)
	}
}

func TestSpoolFollowHighlightsFreshLinesAndFadesThem(t *testing.T) {
	content := []string{"one", "two", "three"}
	model := spoolFollowModel(t, &content)
	if !runSpoolFollowCommand(t, model, "follow") {
		t.Fatal("follow did not reach an idle tick")
	}
	if model.spoolFresh != nil {
		t.Fatalf("pre-follow content marked fresh: %v", model.spoolFresh)
	}

	content = append(content, "four", "five")
	if !spoolFollowTick(t, model) {
		t.Fatal("poll after growth did not reschedule")
	}
	if len(model.spoolFresh) != 1 || model.spoolFresh[0].FirstIndex != 3 {
		t.Fatalf("fresh batches = %v, want one starting at bulk index 3", model.spoolFresh)
	}
	if _, ok := model.spoolFreshAge(2, time.Now()); ok {
		t.Fatal("pre-follow line reported as fresh")
	}
	if _, ok := model.spoolFreshAge(4, time.Now()); !ok {
		t.Fatal("appended line not reported as fresh")
	}

	// A new line renders in the accent highlight, not the default style.
	if got := model.spoolFreshStyle(0).GetForeground(); got != ayu.accent {
		t.Fatalf("fresh style foreground = %v, want the accent", got)
	}
	if faded := model.spoolFreshStyle(spoolFreshFadeDuration).GetForeground(); faded == ayu.accent {
		t.Fatal("fully faded style still renders the accent")
	}

	// Once the fade duration has passed, the tick prunes the batch and stops
	// rescheduling itself.
	model.spoolFresh[0].At = time.Now().Add(-spoolFreshFadeDuration - time.Second)
	model.spoolFadeTicking = true
	tick := spoolFadeTickMsg{Profile: model.profile, Generation: model.spoolBulkGeneration}
	if cmd := applyMessage(t, model, tick); cmd != nil {
		t.Fatal("fade tick kept rescheduling after every highlight expired")
	}
	if model.spoolFresh != nil {
		t.Fatalf("expired batches not pruned: %v", model.spoolFresh)
	}
	if _, ok := model.spoolFreshAge(4, time.Now()); ok {
		t.Fatal("pruned line still reported as fresh")
	}

	// Stopping follow clears any remaining marks outright.
	model.markSpoolFresh(3, time.Now())
	model.stopSpoolFollow()
	if model.spoolFresh != nil {
		t.Fatal("stopSpoolFollow left fresh marks behind")
	}
}
