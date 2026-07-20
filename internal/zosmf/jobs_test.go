package zosmf

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Tannex/cq/internal/zowe"
)

func TestListJobsSendsOwnerPrefixMaxJobs(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/zosmf/restjobs/jobs" {
			t.Errorf("path = %s", r.URL.Path)
		}
		if got := r.URL.Query().Get("owner"); got != "IBMUSER" {
			t.Errorf("owner = %q", got)
		}
		if got := r.URL.Query().Get("prefix"); got != "TESTJOB*" {
			t.Errorf("prefix = %q", got)
		}
		if got := r.URL.Query().Get("max-jobs"); got != "20" {
			t.Errorf("max-jobs = %q", got)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `[]`)
	}))
	defer server.Close()

	client := New(sessionForServer(t, server), nil)
	if _, err := client.ListJobs(context.Background(), ListJobsRequest{Owner: "ibmuser", Prefix: "testjob*", MaxItems: 20}); err != nil {
		t.Fatal(err)
	}
}

func TestListJobsClampsMaxJobsTo1000(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.URL.Query().Get("max-jobs"); got != "1000" {
			t.Errorf("max-jobs = %q, want clamped to 1000", got)
		}
		_, _ = io.WriteString(w, `[]`)
	}))
	defer server.Close()

	client := New(sessionForServer(t, server), nil)
	if _, err := client.ListJobs(context.Background(), ListJobsRequest{MaxItems: 5000}); err != nil {
		t.Fatal(err)
	}
}

func TestListJobsRejectsNonPositiveBudget(t *testing.T) {
	client := New(sessionForServer(t, httptest.NewServer(nil)), nil)
	if _, err := client.ListJobs(context.Background(), ListJobsRequest{MaxItems: 0}); err == nil {
		t.Fatal("want error for zero max items")
	}
}

func TestListJobsTruncatesOversizedResponseAndReportsNoMoreRows(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `[
			{"jobid":"JOB00001","jobname":"A","owner":"IBMUSER","status":"OUTPUT","type":"JOB","class":"A","retcode":"CC 0000"},
			{"jobid":"JOB00002","jobname":"B","owner":"IBMUSER","status":"OUTPUT","type":"JOB","class":"A","retcode":"CC 0000"},
			{"jobid":"JOB00003","jobname":"C","owner":"IBMUSER","status":"OUTPUT","type":"JOB","class":"A","retcode":"CC 0000"}
		]`)
	}))
	defer server.Close()

	client := New(sessionForServer(t, server), nil)
	page, err := client.ListJobs(context.Background(), ListJobsRequest{MaxItems: 2})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Items) != 2 {
		t.Fatalf("items = %d, want 2 (client-side truncation to budget)", len(page.Items))
	}
	// z/OSMF's job list has no pagination signal at all: MoreRows must stay
	// false even when the client truncated, since there is nothing honest
	// to report either way.
	if page.MoreRows {
		t.Fatal("MoreRows must always be false for job lists")
	}
}

func TestListJobsParsesDocumentedFieldNames(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `[{
			"jobid":"JOB00023","jobname":"TESTJOB2","subsystem":null,"owner":"IBMUSER",
			"status":"OUTPUT","type":"JOB","class":"A","retcode":"CC 0000",
			"url":"https://host/zosmf/restjobs/jobs/TESTJOB2/JOB00023",
			"files-url":"https://host/zosmf/restjobs/jobs/TESTJOB2/JOB00023/files",
			"job-correlator":"J0000023SYS1.....",
			"phase":20,"phase-name":"Job is on the hard copy queue"
		}]`)
	}))
	defer server.Close()

	client := New(sessionForServer(t, server), nil)
	page, err := client.ListJobs(context.Background(), ListJobsRequest{MaxItems: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Items) != 1 {
		t.Fatalf("items = %#v", page.Items)
	}
	got := page.Items[0]
	want := Job{
		JobID: "JOB00023", JobName: "TESTJOB2", Owner: "IBMUSER", Status: "OUTPUT",
		Type: "JOB", Class: "A", ReturnCode: "CC 0000",
		URL:           "https://host/zosmf/restjobs/jobs/TESTJOB2/JOB00023",
		FilesURL:      "https://host/zosmf/restjobs/jobs/TESTJOB2/JOB00023/files",
		JobCorrelator: "J0000023SYS1.....", Phase: 20,
		PhaseName: "Job is on the hard copy queue",
	}
	if got != want {
		t.Fatalf("job = %#v, want %#v", got, want)
	}
}

func TestListJobsParsesNullRetcodeAsEmpty(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `[{"jobid":"STC00052","jobname":"BLS3PRMI","owner":"IBMUSER","status":"ACTIVE","type":"STC","class":"STC","retcode":null,"phase":14,"phase-name":"Job is actively executing"}]`)
	}))
	defer server.Close()

	client := New(sessionForServer(t, server), nil)
	page, err := client.ListJobs(context.Background(), ListJobsRequest{MaxItems: 10})
	if err != nil {
		t.Fatal(err)
	}
	if page.Items[0].ReturnCode != "" {
		t.Fatalf("ReturnCode = %q, want empty for a running job", page.Items[0].ReturnCode)
	}
	if page.Items[0].Status != "ACTIVE" {
		t.Fatalf("Status = %q", page.Items[0].Status)
	}
}

func TestListSpoolFilesSendsCorrectPathAndParsesFields(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/zosmf/restjobs/jobs/TESTJOB1/JOB00023/files" {
			t.Errorf("path = %s", r.URL.Path)
		}
		if r.URL.RawQuery != "" {
			t.Errorf("query = %q, want none", r.URL.RawQuery)
		}
		_, _ = io.WriteString(w, `[
			{"jobid":"JOB00023","jobname":"TESTJOB1","stepname":"JES2","procstep":null,"class":"H","ddname":"JESMSGLG","record-count":14,"byte-count":1200,"records-url":"https://host/zosmf/restjobs/jobs/TESTJOB1/JOB00023/files/1/records","id":1},
			{"jobid":"JOB00023","jobname":"TESTJOB1","stepname":"STEP57","procstep":"COMPILE","class":"A","ddname":"SYSPRINT","record-count":3,"byte-count":209,"records-url":"https://host/zosmf/restjobs/jobs/TESTJOB1/JOB00023/files/5/records","id":5}
		]`)
	}))
	defer server.Close()

	client := New(sessionForServer(t, server), nil)
	files, err := client.ListSpoolFiles(context.Background(), "testjob1", "job00023")
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 2 {
		t.Fatalf("files = %#v", files)
	}
	if files[0].ID != 1 || files[0].DDName != "JESMSGLG" || files[0].StepName != "JES2" || files[0].ProcStep != "" {
		t.Fatalf("file[0] = %#v", files[0])
	}
	if files[1].ID != 5 || files[1].ProcStep != "COMPILE" || files[1].RecordCount != 3 || files[1].ByteCount != 209 {
		t.Fatalf("file[1] = %#v", files[1])
	}
}

func TestListSpoolFilesRejectsEmptyJobNameOrID(t *testing.T) {
	client := New(sessionForServer(t, httptest.NewServer(nil)), nil)
	if _, err := client.ListSpoolFiles(context.Background(), "", "JOB00023"); err == nil {
		t.Fatal("want error for empty job name")
	}
	if _, err := client.ListSpoolFiles(context.Background(), "TESTJOB1", ""); err == nil {
		t.Fatal("want error for empty job ID")
	}
}

func TestReadSpoolContentSendsRecordRangeHeader(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/zosmf/restjobs/jobs/TESTJOB1/JOB00023/files/1/records" {
			t.Errorf("path = %s", r.URL.Path)
		}
		if got := r.Header.Get("X-IBM-Record-Range"); got != "0,10" {
			t.Errorf("X-IBM-Record-Range = %q", got)
		}
		w.Header().Set("Content-Type", "text/plain")
		_, _ = io.WriteString(w, "line one\nline two\n")
	}))
	defer server.Close()

	client := New(sessionForServer(t, server), nil)
	page, err := client.ReadSpoolContent(context.Background(), ReadSpoolContentRequest{
		JobName: "testjob1", JobID: "job00023", FileID: "1", Start: 0, MaxItems: 10,
	})
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"line one", "line two"}; !equalLines(page.Lines, want) {
		t.Fatalf("lines = %#v, want %#v", page.Lines, want)
	}
	if page.MoreRows {
		t.Fatal("MoreRows should be false: returned fewer lines than requested")
	}
}

func TestReadSpoolContentJCLPseudoFile(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/zosmf/restjobs/jobs/TESTJOB1/JOB00023/files/JCL/records" {
			t.Errorf("path = %s", r.URL.Path)
		}
		_, _ = io.WriteString(w, "//TESTJOB1 JOB (),CLASS=A\n")
	}))
	defer server.Close()

	client := New(sessionForServer(t, server), nil)
	page, err := client.ReadSpoolContent(context.Background(), ReadSpoolContentRequest{
		JobName: "TESTJOB1", JobID: "JOB00023", FileID: "JCL", Start: 0, MaxItems: 10,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Lines) != 1 || page.Lines[0] != "//TESTJOB1 JOB (),CLASS=A" {
		t.Fatalf("lines = %#v", page.Lines)
	}
}

func TestReadSpoolContentInfersMoreRowsFromLineCount(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, "one\ntwo\nthree\n")
	}))
	defer server.Close()

	client := New(sessionForServer(t, server), nil)
	page, err := client.ReadSpoolContent(context.Background(), ReadSpoolContentRequest{
		JobName: "A", JobID: "B", FileID: "1", MaxItems: 2,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Lines) != 2 || !page.MoreRows {
		t.Fatalf("lines = %#v moreRows = %v, want 2 lines capped and MoreRows true", page.Lines, page.MoreRows)
	}
}

func TestReadSpoolContentEmptyBodyYieldsNoLines(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	client := New(sessionForServer(t, server), nil)
	page, err := client.ReadSpoolContent(context.Background(), ReadSpoolContentRequest{
		JobName: "A", JobID: "B", FileID: "1", MaxItems: 10,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Lines) != 0 {
		t.Fatalf("lines = %#v, want none", page.Lines)
	}
}

func TestReadSpoolContentSingleBlankLineIsNotConflatedWithEmpty(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, "\n")
	}))
	defer server.Close()

	client := New(sessionForServer(t, server), nil)
	page, err := client.ReadSpoolContent(context.Background(), ReadSpoolContentRequest{
		JobName: "A", JobID: "B", FileID: "1", MaxItems: 10,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Lines) != 1 || page.Lines[0] != "" {
		t.Fatalf("lines = %#v, want one blank line", page.Lines)
	}
}

func TestReadSpoolContentBoundsOversizedResponse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, strings.Repeat("x", maxSpoolContentBytes+1))
	}))
	defer server.Close()

	client := New(sessionForServer(t, server), nil)
	if _, err := client.ReadSpoolContent(context.Background(), ReadSpoolContentRequest{
		JobName: "A", JobID: "B", FileID: "1", MaxItems: 10,
	}); err == nil {
		t.Fatal("want a LimitError for an oversized response")
	} else if _, ok := err.(*LimitError); !ok {
		t.Fatalf("err = %#v, want *LimitError", err)
	}
}

func TestJobFilterValuesRejectOver8Characters(t *testing.T) {
	client := New(sessionForServer(t, httptest.NewServer(nil)), nil)
	if _, err := client.ListJobs(context.Background(), ListJobsRequest{Owner: "TOOLONGOWNERNAME", MaxItems: 10}); err == nil {
		t.Fatal("want error for an owner over 8 characters")
	}
}

func TestLazyClientJobBrowserPassthrough(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/files/1/records"):
			_, _ = io.WriteString(w, "hi\n")
		case strings.HasSuffix(r.URL.Path, "/files"):
			_, _ = io.WriteString(w, `[]`)
		default:
			_, _ = io.WriteString(w, `[]`)
		}
	}))
	defer server.Close()
	session := sessionForServer(t, server)
	client := NewLazy(func() (zowe.Session, error) { return session, nil }, nil)
	if _, err := client.ListJobs(context.Background(), ListJobsRequest{MaxItems: 5}); err != nil {
		t.Fatal(err)
	}
	if _, err := client.ListSpoolFiles(context.Background(), "A", "B"); err != nil {
		t.Fatal(err)
	}
	if _, err := client.ReadSpoolContent(context.Background(), ReadSpoolContentRequest{JobName: "A", JobID: "B", FileID: "1", MaxItems: 5}); err != nil {
		t.Fatal(err)
	}
}

func equalLines(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}
