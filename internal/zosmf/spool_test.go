package zosmf

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestOpenSpoolContentStreamsWholeFileWithoutRecordRange(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/zosmf/restjobs/jobs/TESTJOB1/JOB00023/files/5/records" {
			t.Errorf("path = %s", r.URL.Path)
		}
		if got := r.Header.Get("X-IBM-Record-Range"); got != "" {
			t.Errorf("X-IBM-Record-Range = %q, want none for a whole-file stream", got)
		}
		_, _ = io.WriteString(w, "line one\nline two\nline three\n")
	}))
	defer server.Close()

	client := New(sessionForServer(t, server), nil)
	body, err := client.OpenSpoolContent(context.Background(), "testjob1", "job00023", "5")
	if err != nil {
		t.Fatal(err)
	}
	defer body.Close()
	content, err := io.ReadAll(body)
	if err != nil {
		t.Fatal(err)
	}
	if string(content) != "line one\nline two\nline three\n" {
		t.Fatalf("content = %q", content)
	}
}

// TestOpenSpoolContentFileEncodingParameter mirrors the ReadSpoolContent
// table for the streaming path: canonical codeset names are sent, spellings
// with no host codeset are not.
func TestOpenSpoolContentFileEncodingParameter(t *testing.T) {
	for _, tc := range []struct {
		encoding string
		want     string // "" means the parameter must be absent
	}{
		{encoding: "", want: ""},
		{encoding: "cp277", want: "IBM-277"},
		{encoding: "latin1", want: ""},
	} {
		t.Run(tc.encoding, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if got := r.URL.Query().Get("fileEncoding"); got != tc.want {
					t.Errorf("fileEncoding = %q, want %q", got, tc.want)
				}
			}))
			defer server.Close()

			session := sessionForServer(t, server)
			session.Encoding = tc.encoding
			client := New(session, nil)
			body, err := client.OpenSpoolContent(context.Background(), "TESTJOB1", "JOB00023", "5")
			if err != nil {
				t.Fatal(err)
			}
			_ = body.Close()
		})
	}
}

// TestOpenSpoolContentDecodesLatin1Stream pins the streaming path's wire
// charset the same way the paged reader's test does.
func TestOpenSpoolContentDecodesLatin1Stream(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte{'B', 0xD8, 'F', '\n'}) // BØF in ISO 8859-1
	}))
	defer server.Close()

	client := New(sessionForServer(t, server), nil)
	body, err := client.OpenSpoolContent(context.Background(), "TESTJOB1", "JOB00023", "5")
	if err != nil {
		t.Fatal(err)
	}
	defer body.Close()
	content, err := io.ReadAll(body)
	if err != nil {
		t.Fatal(err)
	}
	if string(content) != "BØF\n" {
		t.Fatalf("content = %q, want the 8859-1 byte decoded to BØF", content)
	}
}

func TestOpenSpoolContentRejectsEmptyIdentifiers(t *testing.T) {
	client := New(sessionForServer(t, httptest.NewServer(nil)), nil)
	for _, tc := range []struct{ name, id, file string }{
		{"", "JOB00023", "1"},
		{"TESTJOB1", "", "1"},
		{"TESTJOB1", "JOB00023", ""},
	} {
		if _, err := client.OpenSpoolContent(context.Background(), tc.name, tc.id, tc.file); err == nil {
			t.Fatalf("want error for %q/%q/%q", tc.name, tc.id, tc.file)
		}
	}
}

func TestReadJobStatusFetchesOneJobDocument(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/zosmf/restjobs/jobs/TESTJOB1/JOB00023" {
			t.Errorf("path = %s", r.URL.Path)
		}
		_, _ = io.WriteString(w, `{"jobname":"TESTJOB1","jobid":"JOB00023","status":"OUTPUT","retcode":"CC 0000"}`)
	}))
	defer server.Close()

	client := New(sessionForServer(t, server), nil)
	job, err := client.ReadJobStatus(context.Background(), "testjob1", "job00023")
	if err != nil {
		t.Fatal(err)
	}
	if job.Status != "OUTPUT" || job.ReturnCode != "CC 0000" || job.JobName != "TESTJOB1" {
		t.Fatalf("job = %+v", job)
	}
}

func TestReadJobStatusRejectsEmptyIdentifiers(t *testing.T) {
	client := New(sessionForServer(t, httptest.NewServer(nil)), nil)
	for _, tc := range []struct{ name, id string }{{"", "JOB00023"}, {"TESTJOB1", ""}} {
		if _, err := client.ReadJobStatus(context.Background(), tc.name, tc.id); err == nil {
			t.Fatalf("want error for %q/%q", tc.name, tc.id)
		}
	}
}

// followerBrowser stubs JobBrowser with a scripted ReadSpoolContent.
type followerBrowser struct {
	read     func(ReadSpoolContentRequest) (SpoolContentPage, error)
	requests []ReadSpoolContentRequest
}

func (b *followerBrowser) ListJobs(context.Context, ListJobsRequest) (JobPage, error) {
	return JobPage{}, nil
}

func (b *followerBrowser) ListSpoolFiles(context.Context, string, string) ([]SpoolFile, error) {
	return nil, nil
}

func (b *followerBrowser) ReadSpoolContent(_ context.Context, request ReadSpoolContentRequest) (SpoolContentPage, error) {
	b.requests = append(b.requests, request)
	return b.read(request)
}

func TestSpoolFollowerPollsIncrementally(t *testing.T) {
	content := []string{"one", "two", "three", "four", "five"}
	browser := &followerBrowser{}
	browser.read = func(request ReadSpoolContentRequest) (SpoolContentPage, error) {
		start := min(int(request.Start), len(content))
		end := min(start+request.MaxItems, len(content))
		return SpoolContentPage{
			Lines:    content[start:end],
			Start:    request.Start,
			MoreRows: end-start == request.MaxItems && end < len(content),
		}, nil
	}
	follower := FollowSpool(browser, "TESTJOB1", "JOB00023", "5", 2)

	lines, more, err := follower.Poll(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if fmt.Sprint(lines) != "[one two]" || !more {
		t.Fatalf("first poll = %v more=%v", lines, more)
	}
	if follower.Position() != 2 {
		t.Fatalf("position = %d, want 2", follower.Position())
	}

	lines, _, err = follower.Poll(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if fmt.Sprint(lines) != "[three four]" {
		t.Fatalf("second poll = %v", lines)
	}
	lines, more, err = follower.Poll(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if fmt.Sprint(lines) != "[five]" || more {
		t.Fatalf("third poll = %v more=%v", lines, more)
	}

	// Nothing new appended: an empty poll leaves the position in place.
	lines, more, err = follower.Poll(context.Background())
	if err != nil || len(lines) != 0 || more {
		t.Fatalf("idle poll = %v more=%v err=%v", lines, more, err)
	}
	if follower.Position() != 5 {
		t.Fatalf("position = %d, want 5", follower.Position())
	}

	// New lines appear (the job wrote more output): the next poll picks up
	// exactly from the recorded position.
	content = append(content, "six")
	lines, _, err = follower.Poll(context.Background())
	if err != nil || fmt.Sprint(lines) != "[six]" {
		t.Fatalf("poll after growth = %v err=%v", lines, err)
	}
	if got := browser.requests[len(browser.requests)-1].Start; got != 5 {
		t.Fatalf("last request start = %d, want 5", got)
	}
}

func TestSpoolFollowerSetPositionSkipsHeldLines(t *testing.T) {
	content := []string{"one", "two", "three", "four"}
	browser := &followerBrowser{}
	browser.read = func(request ReadSpoolContentRequest) (SpoolContentPage, error) {
		start := min(int(request.Start), len(content))
		end := min(start+request.MaxItems, len(content))
		return SpoolContentPage{Lines: content[start:end], Start: request.Start}, nil
	}
	follower := FollowSpool(browser, "TESTJOB1", "JOB00023", "5", 10)
	follower.SetPosition(3)

	lines, _, err := follower.Poll(context.Background())
	if err != nil || fmt.Sprint(lines) != "[four]" {
		t.Fatalf("poll after SetPosition = %v err=%v", lines, err)
	}
	if got := browser.requests[0].Start; got != 3 {
		t.Fatalf("first request start = %d, want 3", got)
	}

	follower.SetPosition(-2)
	if follower.Position() != 0 {
		t.Fatalf("negative position not clamped: %d", follower.Position())
	}
}

func TestSpoolFollowerDoesNotAdvanceOnError(t *testing.T) {
	browser := &followerBrowser{}
	fail := true
	browser.read = func(request ReadSpoolContentRequest) (SpoolContentPage, error) {
		if fail {
			return SpoolContentPage{}, errors.New("boom")
		}
		return SpoolContentPage{Lines: []string{"one"}, Start: request.Start}, nil
	}
	follower := FollowSpool(browser, "TESTJOB1", "JOB00023", "5", 10)

	if _, _, err := follower.Poll(context.Background()); err == nil {
		t.Fatal("want error")
	}
	if follower.Position() != 0 {
		t.Fatalf("position advanced on error: %d", follower.Position())
	}

	fail = false
	lines, _, err := follower.Poll(context.Background())
	if err != nil || len(lines) != 1 {
		t.Fatalf("retry poll = %v err=%v", lines, err)
	}
	if got := browser.requests[len(browser.requests)-1].Start; got != 0 {
		t.Fatalf("retry start = %d, want 0", got)
	}
}
