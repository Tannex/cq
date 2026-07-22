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

	// The first navigation key stops following and anchors the pager on the
	// tail line the user was watching.
	executeCommand(t, model, model.handleAction(actionUp))
	if model.spoolFollow {
		t.Fatal("navigation did not stop follow mode")
	}
	if !strings.Contains(model.status.Text, "follow stopped") {
		t.Fatalf("status = %q", model.status.Text)
	}
	if got := model.spoolContentPage.selectedIndex(); model.spoolContent[got].Number != 2 {
		t.Fatalf("pager not anchored at the tail, line %d", model.spoolContent[got].Number)
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

func TestSpoolFollowDroppedByRefreshAndBack(t *testing.T) {
	content := []string{"one"}
	model := spoolFollowModel(t, &content)
	if !runSpoolFollowCommand(t, model, "follow") {
		t.Fatal("follow did not reach an idle tick")
	}
	executeCommand(t, model, model.handleAction(actionBack))
	if model.spoolFollow || model.spoolFollower != nil || model.spoolBulk != nil {
		t.Fatalf("back did not drop follow state: follow=%v bulk=%v", model.spoolFollow, model.spoolBulk)
	}
}
