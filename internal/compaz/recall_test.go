package compaz

import (
	"context"
	"errors"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/Tannex/cq/internal/zosmf"
)

// fakeRecallBrowser adds the optional Recaller capability to fakeBrowser.
type fakeRecallBrowser struct {
	fakeBrowser
	recallRequests []string
	recall         func(context.Context, string) error
}

func (f *fakeRecallBrowser) RecallDataSet(ctx context.Context, dsn string) error {
	f.recallRequests = append(f.recallRequests, dsn)
	if f.recall != nil {
		return f.recall(ctx, dsn)
	}
	return nil
}

func recallResultMessage(t *testing.T, command tea.Cmd) recallResultMsg {
	t.Helper()
	queue := []tea.Cmd{command}
	for len(queue) > 0 {
		current := queue[0]
		queue = queue[1:]
		if current == nil {
			continue
		}
		message := current()
		if batch, ok := message.(tea.BatchMsg); ok {
			queue = append(queue, batch...)
			continue
		}
		if result, ok := message.(recallResultMsg); ok {
			return result
		}
	}
	t.Fatal("command produced no recall result")
	return recallResultMsg{}
}

func migratedDataSetPage(migrated bool) zosmf.DataSetPage {
	item := zosmf.DataSet{Name: "IBMUSER.MIGR", Migrated: "YES", Volume: "MIGRAT"}
	if !migrated {
		item = zosmf.DataSet{Name: "IBMUSER.MIGR", Organization: "PS", Migrated: "NO", Volume: "VOL001"}
	}
	return zosmf.DataSetPage{Items: []zosmf.DataSet{item}}
}

func TestRecallKeyStartsRecallAndPollsUntilTheDataSetReturns(t *testing.T) {
	migrated := true
	browser := &fakeRecallBrowser{}
	browser.listDataSets = func(context.Context, zosmf.ListDataSetsRequest) (zosmf.DataSetPage, error) {
		return migratedDataSetPage(migrated), nil
	}
	model := readyModel(t, Options{Prefix: "IBMUSER"}, browser, "IBMUSER", "cp037", 100, 20)

	result := recallResultMessage(t, model.handleKey(keyPress('R', "R")))
	if len(browser.recallRequests) != 1 || browser.recallRequests[0] != "IBMUSER.MIGR" {
		t.Fatalf("recall requests = %v", browser.recallRequests)
	}
	pollCmd := applyMessage(t, model, result)
	if pollCmd == nil {
		t.Fatal("accepted recall scheduled no poll")
	}
	if !model.ws().recallPending("IBMUSER.MIGR") {
		t.Fatal("recall not marked pending")
	}
	if view := model.dataSetTableView(); !strings.Contains(view, "RECALL") {
		t.Fatalf("data set view has no RECALL indicator:\n%s", view)
	}

	// First poll still sees the data set migrated: the watch continues.
	checkCmd := applyMessage(t, model, recallPollMsg{DSN: "IBMUSER.MIGR", Attempt: 1})
	if checkCmd == nil {
		t.Fatal("poll produced no check command")
	}
	next := applyMessage(t, model, checkCmd())
	if next == nil || !model.ws().recallPending("IBMUSER.MIGR") {
		t.Fatal("still-migrated check should keep polling")
	}
	checkRequest := browser.dataSetRequests[len(browser.dataSetRequests)-1]
	if checkRequest.Prefix != "IBMUSER.MIGR" || checkRequest.MaxItems != 1 {
		t.Fatalf("watch request = %#v, want exact-name single-item lookup", checkRequest)
	}

	// The next poll finds the recalled data set on primary storage.
	migrated = false
	checkCmd = applyMessage(t, model, recallPollMsg{DSN: "IBMUSER.MIGR", Attempt: 2})
	if done := applyMessage(t, model, checkCmd()); done != nil {
		t.Fatal("completed recall should stop polling")
	}
	if model.ws().recallPending("IBMUSER.MIGR") {
		t.Fatal("recall still pending after completion")
	}
	if model.ws().datasets[0].Volume != "VOL001" {
		t.Fatalf("data set entry not refreshed: %#v", model.ws().datasets[0])
	}
	if model.ws().status.Level != statusReady || !strings.Contains(model.ws().status.Text, "recalled IBMUSER.MIGR to VOL001") {
		t.Fatalf("status = %#v", model.ws().status)
	}
	if view := model.dataSetTableView(); strings.Contains(view, "RECALL") {
		t.Fatalf("indicator not cleared:\n%s", view)
	}
}

func TestRecallWarnsWhenDataSetIsNotMigrated(t *testing.T) {
	browser := &fakeRecallBrowser{}
	browser.listDataSets = func(context.Context, zosmf.ListDataSetsRequest) (zosmf.DataSetPage, error) {
		return migratedDataSetPage(false), nil
	}
	model := readyModel(t, Options{Prefix: "IBMUSER"}, browser, "IBMUSER", "cp037", 100, 20)

	if cmd := model.handleKey(keyPress('R', "R")); cmd != nil {
		t.Fatal("non-migrated data set should not start a recall")
	}
	if len(browser.recallRequests) != 0 {
		t.Fatalf("recall requests = %v", browser.recallRequests)
	}
	if model.ws().status.Level != statusWarn || !strings.Contains(model.ws().status.Text, "not migrated") {
		t.Fatalf("status = %#v", model.ws().status)
	}
}

func TestRecallWarnsWhenSessionCannotRecall(t *testing.T) {
	browser := &fakeBrowser{}
	browser.listDataSets = func(context.Context, zosmf.ListDataSetsRequest) (zosmf.DataSetPage, error) {
		return migratedDataSetPage(true), nil
	}
	model := readyModel(t, Options{Prefix: "IBMUSER"}, browser, "IBMUSER", "cp037", 100, 20)

	if cmd := model.handleKey(keyPress('R', "R")); cmd != nil {
		t.Fatal("unsupported session should not start a recall")
	}
	if model.ws().status.Level != statusWarn || !strings.Contains(model.ws().status.Text, "cannot recall") {
		t.Fatalf("status = %#v", model.ws().status)
	}
}

func TestRecallFailureClearsTheIndicator(t *testing.T) {
	browser := &fakeRecallBrowser{recall: func(context.Context, string) error {
		return errors.New("HRECALL failed on the host")
	}}
	browser.listDataSets = func(context.Context, zosmf.ListDataSetsRequest) (zosmf.DataSetPage, error) {
		return migratedDataSetPage(true), nil
	}
	model := readyModel(t, Options{Prefix: "IBMUSER"}, browser, "IBMUSER", "cp037", 100, 20)

	result := recallResultMessage(t, model.handleKey(keyPress('R', "R")))
	if cmd := applyMessage(t, model, result); cmd != nil {
		t.Fatal("failed recall should not schedule a poll")
	}
	if model.ws().recallPending("IBMUSER.MIGR") {
		t.Fatal("failed recall left the indicator pending")
	}
	if model.ws().status.Level != statusError || !strings.Contains(model.ws().status.Text, "HRECALL failed") {
		t.Fatalf("status = %#v", model.ws().status)
	}
}

func TestRecallPollingGivesUpAfterTheAttemptCap(t *testing.T) {
	browser := &fakeRecallBrowser{}
	browser.listDataSets = func(context.Context, zosmf.ListDataSetsRequest) (zosmf.DataSetPage, error) {
		return migratedDataSetPage(true), nil
	}
	model := readyModel(t, Options{Prefix: "IBMUSER"}, browser, "IBMUSER", "cp037", 100, 20)
	applyMessage(t, model, recallResultMessage(t, model.handleKey(keyPress('R', "R"))))

	check := recallCheckMsg{DSN: "IBMUSER.MIGR", Attempt: maxRecallPolls, Page: migratedDataSetPage(true)}
	if cmd := applyMessage(t, model, check); cmd != nil {
		t.Fatal("capped watch should stop polling")
	}
	if model.ws().recallPending("IBMUSER.MIGR") {
		t.Fatal("capped watch left the indicator pending")
	}
	if model.ws().status.Level != statusWarn || !strings.Contains(model.ws().status.Text, "still running") {
		t.Fatalf("status = %#v", model.ws().status)
	}
}

func TestRecallCapWarningIncludesTheLastCheckError(t *testing.T) {
	browser := &fakeRecallBrowser{}
	browser.listDataSets = func(context.Context, zosmf.ListDataSetsRequest) (zosmf.DataSetPage, error) {
		return migratedDataSetPage(true), nil
	}
	model := readyModel(t, Options{Prefix: "IBMUSER"}, browser, "IBMUSER", "cp037", 100, 20)
	applyMessage(t, model, recallResultMessage(t, model.handleKey(keyPress('R', "R"))))

	failed := recallCheckMsg{DSN: "IBMUSER.MIGR", Attempt: 1, Err: errors.New("catalog is unavailable")}
	if cmd := applyMessage(t, model, failed); cmd == nil {
		t.Fatal("failed check should keep polling")
	}
	capped := recallCheckMsg{DSN: "IBMUSER.MIGR", Attempt: maxRecallPolls, Err: errors.New("catalog is unavailable")}
	if cmd := applyMessage(t, model, capped); cmd != nil {
		t.Fatal("capped watch should stop polling")
	}
	if model.ws().status.Level != statusWarn || !strings.Contains(model.ws().status.Text, "catalog is unavailable") {
		t.Fatalf("status = %#v", model.ws().status)
	}
}

func TestManualRefreshResolvesAPendingRecall(t *testing.T) {
	migrated := true
	browser := &fakeRecallBrowser{}
	browser.listDataSets = func(context.Context, zosmf.ListDataSetsRequest) (zosmf.DataSetPage, error) {
		return migratedDataSetPage(migrated), nil
	}
	model := readyModel(t, Options{Prefix: "IBMUSER"}, browser, "IBMUSER", "cp037", 100, 20)
	applyMessage(t, model, recallResultMessage(t, model.handleKey(keyPress('R', "R"))))
	if !model.ws().recallPending("IBMUSER.MIGR") {
		t.Fatal("recall not marked pending")
	}

	// The recall completes on the host; the user refreshes before the
	// watcher's next check fires.
	migrated = false
	executeCommand(t, model, model.handleKey(keyPress('r', "r")))
	if model.ws().recallPending("IBMUSER.MIGR") {
		t.Fatal("refresh did not resolve the pending recall")
	}
	if model.ws().status.Level != statusReady || !strings.Contains(model.ws().status.Text, "recalled IBMUSER.MIGR to VOL001") {
		t.Fatalf("status = %#v", model.ws().status)
	}
}

func TestOpeningMigratedDataSetSuggestsRecall(t *testing.T) {
	browser := &fakeRecallBrowser{}
	browser.listDataSets = func(context.Context, zosmf.ListDataSetsRequest) (zosmf.DataSetPage, error) {
		return migratedDataSetPage(true), nil
	}
	model := readyModel(t, Options{Prefix: "IBMUSER"}, browser, "IBMUSER", "cp037", 100, 20)

	if cmd := model.handleKey(keyPress(tea.KeyEnter, "")); cmd != nil {
		t.Fatal("opening a migrated data set should not fetch")
	}
	if model.ws().status.Level != statusWarn || !strings.Contains(model.ws().status.Text, "press R to recall") {
		t.Fatalf("status = %#v", model.ws().status)
	}
}
