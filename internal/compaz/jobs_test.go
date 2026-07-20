package compaz

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"testing"

	"github.com/Tannex/cq/internal/zosmf"
	"github.com/Tannex/cq/internal/zowe"
)

func TestJobsOpenDefaultsOwnerAndPrefix(t *testing.T) {
	browser := &fakeBrowser{listJobs: func(_ context.Context, request zosmf.ListJobsRequest) (zosmf.JobPage, error) {
		return zosmf.JobPage{Items: []zosmf.Job{{JobID: "JOB00001", JobName: "NIGHTBAT", Status: "OUTPUT"}}}, nil
	}}
	model := readyModel(t, Options{Prefix: "A*"}, browser, "IBMUSER", "", 90, 16)
	if model.jobOwner != "IBMUSER" || model.jobPrefix != "*" {
		t.Fatalf("job filter defaults owner=%q prefix=%q", model.jobOwner, model.jobPrefix)
	}

	executeCommand(t, model, model.handleAction(actionJobs))
	if model.screen != ScreenJobs || len(model.jobs) != 1 || model.jobs[0].JobName != "NIGHTBAT" {
		t.Fatalf("jobs screen=%d jobs=%#v", model.screen, model.jobs)
	}
	if len(browser.jobRequests) != 1 {
		t.Fatalf("job requests = %#v", browser.jobRequests)
	}
	request := browser.jobRequests[0]
	if request.Owner != "IBMUSER" || request.Prefix != "*" || request.MaxItems != model.budget {
		t.Fatalf("job request = %#v, want owner IBMUSER prefix * budget %d", request, model.budget)
	}
}

func TestJobsPrefixFilterRefetches(t *testing.T) {
	browser := &fakeBrowser{listJobs: func(_ context.Context, request zosmf.ListJobsRequest) (zosmf.JobPage, error) {
		return zosmf.JobPage{Items: []zosmf.Job{{JobID: "JOB00001", JobName: request.Prefix + "1"}}}, nil
	}}
	model := readyModel(t, Options{}, browser, "IBMUSER", "", 90, 16)
	executeCommand(t, model, model.handleAction(actionJobs))
	if len(browser.jobRequests) != 1 {
		t.Fatalf("initial requests = %d", len(browser.jobRequests))
	}

	executeCommand(t, model, model.handleAction(actionSearch))
	if !model.jobFilterInput.Focused() {
		t.Fatal("actionSearch on ScreenJobs did not focus the job filter input")
	}
	model.jobFilterInput.SetValue("night*")
	executeCommand(t, model, model.acceptSearch())
	if model.jobFilterInput.Focused() {
		t.Fatal("acceptSearch left the job filter input focused")
	}
	if model.jobPrefix != "NIGHT*" {
		t.Fatalf("job prefix = %q, want NIGHT*", model.jobPrefix)
	}
	if len(browser.jobRequests) != 2 || browser.jobRequests[1].Prefix != "NIGHT*" {
		t.Fatalf("refetch requests = %#v", browser.jobRequests)
	}
	if len(model.jobs) != 1 || model.jobs[0].JobName != "NIGHT*1" {
		t.Fatalf("jobs after refetch = %#v", model.jobs)
	}
}

func TestJobsDrillDownToSpoolFilesAndContent(t *testing.T) {
	browser := &fakeBrowser{
		listJobs: func(context.Context, zosmf.ListJobsRequest) (zosmf.JobPage, error) {
			return zosmf.JobPage{Items: []zosmf.Job{{JobID: "JOB00001", JobName: "NIGHTBAT", Status: "OUTPUT"}}}, nil
		},
		listSpoolFiles: func(_ context.Context, jobName, jobID string) ([]zosmf.SpoolFile, error) {
			if jobName != "NIGHTBAT" || jobID != "JOB00001" {
				t.Fatalf("spool file list job = %s/%s", jobName, jobID)
			}
			return []zosmf.SpoolFile{{JobName: jobName, JobID: jobID, ID: 2, DDName: "SYSPRINT"}}, nil
		},
		readSpoolContent: func(_ context.Context, request zosmf.ReadSpoolContentRequest) (zosmf.SpoolContentPage, error) {
			return zosmf.SpoolContentPage{Lines: []string{"LINE ONE", "LINE TWO"}, Start: request.Start}, nil
		},
	}
	model := readyModel(t, Options{}, browser, "IBMUSER", "", 90, 16)
	executeCommand(t, model, model.handleAction(actionJobs))

	command := model.openSelection()
	if model.screen != ScreenSpoolFiles || command == nil {
		t.Fatalf("job selection screen=%d command=%v", model.screen, command)
	}
	executeCommand(t, model, command)
	if len(browser.spoolFileRequests) != 1 || browser.spoolFileRequests[0] != [2]string{"NIGHTBAT", "JOB00001"} {
		t.Fatalf("spool file requests = %#v", browser.spoolFileRequests)
	}
	if len(model.spoolFiles) != 2 || model.spoolFiles[0].DDName != "JCL" || model.spoolFiles[1].DDName != "SYSPRINT" {
		t.Fatalf("spool files = %#v", model.spoolFiles)
	}

	model.spoolFilePage.move(1)
	command = model.openSelection()
	if model.screen != ScreenSpoolContent || command == nil {
		t.Fatalf("spool file selection screen=%d command=%v", model.screen, command)
	}
	executeCommand(t, model, command)
	if len(browser.spoolContentRequests) != 1 || browser.spoolContentRequests[0].FileID != "2" {
		t.Fatalf("spool content requests = %#v", browser.spoolContentRequests)
	}
	if len(model.spoolContent) != 2 || model.spoolContent[0].Text != "LINE ONE" {
		t.Fatalf("spool content = %#v", model.spoolContent)
	}
}

func TestJobsBackNavigationChain(t *testing.T) {
	browser := &fakeBrowser{
		listDataSets: func(context.Context, zosmf.ListDataSetsRequest) (zosmf.DataSetPage, error) {
			return zosmf.DataSetPage{Items: []zosmf.DataSet{{Name: "A.DATA", Organization: "PS"}}}, nil
		},
		listJobs: func(context.Context, zosmf.ListJobsRequest) (zosmf.JobPage, error) {
			return zosmf.JobPage{Items: []zosmf.Job{{JobID: "JOB00001", JobName: "NIGHTBAT"}}}, nil
		},
		listSpoolFiles: func(context.Context, string, string) ([]zosmf.SpoolFile, error) {
			return []zosmf.SpoolFile{{ID: 2, DDName: "SYSPRINT"}}, nil
		},
		readSpoolContent: func(context.Context, zosmf.ReadSpoolContentRequest) (zosmf.SpoolContentPage, error) {
			return zosmf.SpoolContentPage{Lines: []string{"X"}}, nil
		},
	}
	model := readyModel(t, Options{Prefix: "A*"}, browser, "IBMUSER", "", 90, 16)
	executeCommand(t, model, model.handleAction(actionJobs))
	executeCommand(t, model, model.openSelection())
	executeCommand(t, model, model.openSelection())
	if model.screen != ScreenSpoolContent {
		t.Fatalf("did not reach spool content, screen=%d", model.screen)
	}

	executeCommand(t, model, model.navigateBack())
	if model.screen != ScreenSpoolFiles || len(model.spoolContent) != 0 {
		t.Fatalf("back from spool content screen=%d content=%#v", model.screen, model.spoolContent)
	}
	executeCommand(t, model, model.navigateBack())
	if model.screen != ScreenJobs || len(model.spoolFiles) != 0 || model.job.JobName != "" {
		t.Fatalf("back from spool files screen=%d files=%#v job=%#v", model.screen, model.spoolFiles, model.job)
	}
	executeCommand(t, model, model.navigateBack())
	if model.screen != ScreenDataSets {
		t.Fatalf("back from jobs screen=%d", model.screen)
	}
	// Unlike members-of-a-dataset, jobs is not a child of the data set
	// screen; its cache is retained across a visit to Back, just like the
	// data set list itself would be if it had a parent screen.
	if len(model.jobs) != 1 {
		t.Fatalf("jobs cache was cleared on back-to-parent: %#v", model.jobs)
	}
}

func TestJobsAndSpoolFilesPagersNeverForwardFetch(t *testing.T) {
	browser := &fakeBrowser{
		listJobs: func(context.Context, zosmf.ListJobsRequest) (zosmf.JobPage, error) {
			items := make([]zosmf.Job, 6)
			for i := range items {
				items[i] = zosmf.Job{JobID: fmt.Sprintf("JOB%05d", i), JobName: fmt.Sprintf("JOB%d", i)}
			}
			return zosmf.JobPage{Items: items}, nil
		},
		listSpoolFiles: func(context.Context, string, string) ([]zosmf.SpoolFile, error) {
			items := make([]zosmf.SpoolFile, 6)
			for i := range items {
				items[i] = zosmf.SpoolFile{ID: i + 1, DDName: fmt.Sprintf("DD%d", i+1)}
			}
			return items, nil
		},
	}
	model := readyModel(t, Options{}, browser, "IBMUSER", "", 90, MinTerminalHeight())
	executeCommand(t, model, model.handleAction(actionJobs))
	jobRequests := len(browser.jobRequests)
	for range len(model.jobs) + 2 {
		if command := model.moveSelection(1); command != nil {
			t.Fatal("jobs pager issued a forward-plan request")
		}
	}
	if len(browser.jobRequests) != jobRequests {
		t.Fatalf("job list requests grew from %d to %d", jobRequests, len(browser.jobRequests))
	}

	command := model.openSelection()
	if command == nil {
		t.Fatal("job selection did not dispatch a spool file fetch")
	}
	executeCommand(t, model, command)
	spoolFileRequests := len(browser.spoolFileRequests)
	for range len(model.spoolFiles) + 2 {
		if command := model.moveSelection(1); command != nil {
			t.Fatal("spool file pager issued a forward-plan request")
		}
	}
	if len(browser.spoolFileRequests) != spoolFileRequests {
		t.Fatalf("spool file list requests grew from %d to %d", spoolFileRequests, len(browser.spoolFileRequests))
	}
}

func TestSpoolContentForwardPrefetchFires(t *testing.T) {
	browser := &fakeBrowser{
		listJobs: func(context.Context, zosmf.ListJobsRequest) (zosmf.JobPage, error) {
			return zosmf.JobPage{Items: []zosmf.Job{{JobID: "JOB00001", JobName: "NIGHTBAT"}}}, nil
		},
		listSpoolFiles: func(context.Context, string, string) ([]zosmf.SpoolFile, error) {
			return []zosmf.SpoolFile{{ID: 2, DDName: "SYSPRINT"}}, nil
		},
		readSpoolContent: func(_ context.Context, request zosmf.ReadSpoolContentRequest) (zosmf.SpoolContentPage, error) {
			lines := make([]string, request.MaxItems)
			for i := range lines {
				lines[i] = fmt.Sprintf("LINE %d", request.Start+int64(i))
			}
			return zosmf.SpoolContentPage{Lines: lines, Start: request.Start, MoreRows: request.Start == 0}, nil
		},
	}
	model := readyModel(t, Options{}, browser, "IBMUSER", "", 90, MinTerminalHeight())
	executeCommand(t, model, model.handleAction(actionJobs))
	executeCommand(t, model, model.openSelection())
	model.spoolFilePage.move(1)
	executeCommand(t, model, model.openSelection())
	if model.screen != ScreenSpoolContent || len(model.spoolContent) != 6 {
		t.Fatalf("initial spool content screen=%d content=%d", model.screen, len(model.spoolContent))
	}

	command := model.moveSelection(3)
	if command == nil {
		t.Fatal("entering the final visible page did not prefetch spool content")
	}
	executeCommand(t, model, command)
	last := browser.spoolContentRequests[len(browser.spoolContentRequests)-1]
	if last.Start != 6 || last.MaxItems != 6 || len(model.spoolContent) != 12 {
		t.Fatalf("forward request=%#v content=%d", last, len(model.spoolContent))
	}
}

func TestStaleJobsResultIsRejectedAfterFilterChanges(t *testing.T) {
	browser := &fakeBrowser{listJobs: func(_ context.Context, request zosmf.ListJobsRequest) (zosmf.JobPage, error) {
		return zosmf.JobPage{Items: []zosmf.Job{{JobID: "JOB00001", JobName: request.Prefix + "JOB"}}}, nil
	}}
	model := readyModel(t, Options{}, browser, "IBMUSER", "", 90, 16)
	executeCommand(t, model, model.handleAction(actionJobs))
	if len(model.jobs) != 1 || model.jobs[0].JobName != "*JOB" {
		t.Fatalf("initial jobs = %#v", model.jobs)
	}

	staleCommand := model.refresh()
	model.jobFilterInput.Focus()
	model.jobFilterInput.SetValue("NIGHT*")
	freshCommand := model.acceptSearch()
	staleMessage := browseResultMessage(t, staleCommand)
	applyMessage(t, model, staleMessage)
	if model.jobPrefix != "NIGHT*" || len(model.jobs) != 0 {
		t.Fatalf("stale result mutated filtered state: prefix=%q jobs=%#v", model.jobPrefix, model.jobs)
	}
	executeCommand(t, model, freshCommand)
	if len(model.jobs) != 1 || model.jobs[0].JobName != "NIGHT*JOB" {
		t.Fatalf("fresh jobs = %#v", model.jobs)
	}
}

func TestStaleSpoolFilesResultIsRejectedAfterRefresh(t *testing.T) {
	callCount := 0
	browser := &fakeBrowser{
		listJobs: func(context.Context, zosmf.ListJobsRequest) (zosmf.JobPage, error) {
			return zosmf.JobPage{Items: []zosmf.Job{{JobID: "JOB00001", JobName: "NIGHTBAT"}}}, nil
		},
		listSpoolFiles: func(context.Context, string, string) ([]zosmf.SpoolFile, error) {
			callCount++
			return []zosmf.SpoolFile{{ID: callCount, DDName: fmt.Sprintf("DD%d", callCount)}}, nil
		},
	}
	model := readyModel(t, Options{}, browser, "IBMUSER", "", 90, 16)
	executeCommand(t, model, model.handleAction(actionJobs))
	executeCommand(t, model, model.openSelection())
	if model.screen != ScreenSpoolFiles || len(model.spoolFiles) != 2 {
		t.Fatalf("initial spool files screen=%d files=%#v", model.screen, model.spoolFiles)
	}

	staleCommand := model.refresh()
	freshCommand := model.refresh()
	staleMessage := browseResultMessage(t, staleCommand)
	applyMessage(t, model, staleMessage)
	if len(model.spoolFiles) != 0 {
		t.Fatalf("stale spool files result was applied: %#v", model.spoolFiles)
	}
	executeCommand(t, model, freshCommand)
	// call 1 populated the initial screen, call 2 was the stale refresh
	// (rejected), call 3 is the fresh refresh actually applied.
	if len(model.spoolFiles) != 2 || model.spoolFiles[1].DDName != "DD3" {
		t.Fatalf("fresh spool files = %#v", model.spoolFiles)
	}
}

func TestStaleSpoolContentResultIsRejectedAfterRefresh(t *testing.T) {
	callCount := 0
	browser := &fakeBrowser{
		listJobs: func(context.Context, zosmf.ListJobsRequest) (zosmf.JobPage, error) {
			return zosmf.JobPage{Items: []zosmf.Job{{JobID: "JOB00001", JobName: "NIGHTBAT"}}}, nil
		},
		listSpoolFiles: func(context.Context, string, string) ([]zosmf.SpoolFile, error) {
			return []zosmf.SpoolFile{{ID: 2, DDName: "SYSPRINT"}}, nil
		},
		readSpoolContent: func(_ context.Context, request zosmf.ReadSpoolContentRequest) (zosmf.SpoolContentPage, error) {
			callCount++
			return zosmf.SpoolContentPage{Lines: []string{fmt.Sprintf("CALL %d", callCount)}, Start: request.Start}, nil
		},
	}
	model := readyModel(t, Options{}, browser, "IBMUSER", "", 90, 16)
	executeCommand(t, model, model.handleAction(actionJobs))
	executeCommand(t, model, model.openSelection())
	executeCommand(t, model, model.openSelection())
	if model.screen != ScreenSpoolContent || len(model.spoolContent) != 1 {
		t.Fatalf("initial spool content screen=%d content=%#v", model.screen, model.spoolContent)
	}

	staleCommand := model.refresh()
	freshCommand := model.refresh()
	staleMessage := browseResultMessage(t, staleCommand)
	applyMessage(t, model, staleMessage)
	if len(model.spoolContent) != 0 {
		t.Fatalf("stale spool content result was applied: %#v", model.spoolContent)
	}
	executeCommand(t, model, freshCommand)
	// call 1 populated the initial screen, call 2 was the stale refresh
	// (rejected), call 3 is the fresh refresh actually applied.
	if len(model.spoolContent) != 1 || model.spoolContent[0].Text != "CALL 3" {
		t.Fatalf("fresh spool content = %#v", model.spoolContent)
	}
}

func TestModelEndToEndBrowsesJobsSpoolFilesAndContent(t *testing.T) {
	var requestLog []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestLog = append(requestLog, r.URL.String())
		switch r.URL.Path {
		case "/zosmf/restfiles/ds":
			_, _ = io.WriteString(w, `{"items":[],"returnedRows":0,"moreRows":false}`)
		case "/zosmf/restjobs/jobs":
			if got := r.URL.Query().Get("owner"); got != "IBMUSER" {
				t.Errorf("owner = %q, want IBMUSER", got)
			}
			if got := r.URL.Query().Get("prefix"); got != "*" {
				t.Errorf("prefix = %q, want *", got)
			}
			if got := r.URL.Query().Get("max-jobs"); got == "" {
				t.Error("max-jobs was not sent")
			}
			_, _ = io.WriteString(w, `[{"jobid":"JOB00001","jobname":"NIGHTBAT","status":"OUTPUT","retcode":"CC 0000"}]`)
		case "/zosmf/restjobs/jobs/NIGHTBAT/JOB00001/files":
			_, _ = io.WriteString(w, `[{"id":2,"ddname":"SYSPRINT","jobname":"NIGHTBAT","jobid":"JOB00001"}]`)
		case "/zosmf/restjobs/jobs/NIGHTBAT/JOB00001/files/2/records":
			if got := r.Header.Get("X-IBM-Record-Range"); got == "" {
				t.Error("spool content request did not send X-IBM-Record-Range")
			}
			_, _ = io.WriteString(w, "STEP1 OUTPUT LINE 1\nSTEP1 OUTPUT LINE 2\n")
		default:
			http.Error(w, "unexpected path "+r.URL.Path, http.StatusNotFound)
		}
	}))
	defer server.Close()

	u, err := url.Parse(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	host, portText, err := net.SplitHostPort(u.Host)
	if err != nil {
		t.Fatal(err)
	}
	port, err := strconv.Atoi(portText)
	if err != nil {
		t.Fatal(err)
	}
	session := zowe.Session{
		Protocol: u.Scheme, Host: host, Port: port,
		User: "IBMUSER", Password: "secret", RejectUnauthorized: true,
	}
	browser := zosmf.New(session, nil)
	model := readyModel(t, Options{Prefix: "A*", Codepage: "latin1"}, browser, "IBMUSER", "", 90, 16)

	executeCommand(t, model, model.handleAction(actionJobs))
	if model.screen != ScreenJobs || len(model.jobs) != 1 || model.jobs[0].JobName != "NIGHTBAT" {
		t.Fatalf("jobs screen=%d jobs=%#v", model.screen, model.jobs)
	}

	command := model.openSelection()
	if model.screen != ScreenSpoolFiles || command == nil {
		t.Fatalf("job selection screen=%d command=%v", model.screen, command)
	}
	executeCommand(t, model, command)
	if len(model.spoolFiles) != 2 || model.spoolFiles[0].DDName != "JCL" || model.spoolFiles[1].DDName != "SYSPRINT" {
		t.Fatalf("spool files = %#v", model.spoolFiles)
	}

	model.spoolFilePage.move(1)
	command = model.openSelection()
	if model.screen != ScreenSpoolContent || command == nil {
		t.Fatalf("spool file selection screen=%d command=%v", model.screen, command)
	}
	executeCommand(t, model, command)
	if len(model.spoolContent) != 2 || model.spoolContent[0].Text != "STEP1 OUTPUT LINE 1" {
		t.Fatalf("spool content = %#v", model.spoolContent)
	}

	executeCommand(t, model, model.navigateBack())
	executeCommand(t, model, model.navigateBack())
	executeCommand(t, model, model.navigateBack())
	if model.screen != ScreenDataSets {
		t.Fatalf("back x3 landed on screen=%d, want ScreenDataSets", model.screen)
	}

	foundJobsRequest, foundSpoolContentRequest := false, false
	for _, entry := range requestLog {
		if u, err := url.Parse(entry); err == nil {
			switch {
			case u.Path == "/zosmf/restjobs/jobs":
				foundJobsRequest = true
			case u.Path == "/zosmf/restjobs/jobs/NIGHTBAT/JOB00001/files/2/records":
				foundSpoolContentRequest = true
			}
		}
	}
	if !foundJobsRequest || !foundSpoolContentRequest {
		t.Fatalf("request log missing expected endpoints: %#v", requestLog)
	}
}
