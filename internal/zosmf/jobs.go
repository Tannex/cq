package zosmf

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

const (
	maxJobListResponseBody = 8 << 20
	maxSpoolFileListBody   = 4 << 20
	maxSpoolContentBytes   = 16 << 20
	// maxJobsPerRequest mirrors z/OSMF's own documented ceiling for max-jobs;
	// values above it are clamped client-side instead of relying on the
	// server's "out of range defaults to 1000" behavior.
	maxJobsPerRequest = 1000
)

// JobBrowser is the optional read-only jobs surface. Deliberately separate
// from Browser (same convention as TextEditor/RecordStreamer): a session
// that cannot browse jobs must not silently gain that capability, and
// callers must type-assert for it and degrade gracefully when the session
// does not provide one.
type JobBrowser interface {
	// ListJobs performs one bounded job search. Unlike ListDataSets/
	// ListMembers, z/OSMF's job list has no cursor or "more remain" signal
	// at all, so JobPage.MoreRows is always false.
	ListJobs(ctx context.Context, request ListJobsRequest) (JobPage, error)
	// ListSpoolFiles lists every spool/DD file for one job. z/OSMF does not
	// paginate this endpoint; a job's spool file count is small in practice.
	ListSpoolFiles(ctx context.Context, jobName, jobID string) ([]SpoolFile, error)
	// ReadSpoolContent retrieves one bounded, zero-based line range of a
	// spool file's text content (or the submitted JCL via FileID "JCL").
	ReadSpoolContent(ctx context.Context, request ReadSpoolContentRequest) (SpoolContentPage, error)
}

var (
	_ JobBrowser = (*Client)(nil)
	_ JobBrowser = (*LazyClient)(nil)
)

// ListJobsRequest describes one bounded job search. Owner and Prefix accept
// z/OSMF's job-filter wildcards (* for any run of characters, ? for exactly
// one); z/OSMF folds both to uppercase and rejects values over 8 characters.
type ListJobsRequest struct {
	Owner    string
	Prefix   string
	MaxItems int
}

// JobPage is one bounded job list result. MoreRows is always false: the
// z/OSMF jobs REST interface documents no pagination cursor and no
// indication of whether more jobs exist beyond what was returned.
type JobPage struct {
	Items    []Job
	MoreRows bool
}

// Job mirrors the documented z/OSMF job document fields.
type Job struct {
	JobID            string
	JobName          string
	Subsystem        string
	Owner            string
	Status           string // INPUT, ACTIVE, or OUTPUT
	Type             string // JOB, STC, or TSU
	Class            string
	ReturnCode       string // nullable in JSON; empty when the job has not completed
	URL              string
	FilesURL         string
	JobCorrelator    string
	Phase            int
	PhaseName        string
	ReasonNotRunning string
}

// UnmarshalJSON accepts the documented z/OSMF job document fields, tolerant
// of the same JSON quirks (stringified numbers, null for absent values) as
// DataSet/Member.
func (j *Job) UnmarshalJSON(data []byte) error {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		return err
	}
	var err error
	*j = Job{}
	assign := func(target *string, name string) {
		if err != nil {
			return
		}
		*target, err = jsonStringField(fields, name)
	}
	assignInt := func(target *int, name string) {
		if err != nil {
			return
		}
		*target, err = jsonIntField(fields, name)
	}
	assign(&j.JobID, "jobid")
	assign(&j.JobName, "jobname")
	assign(&j.Subsystem, "subsystem")
	assign(&j.Owner, "owner")
	assign(&j.Status, "status")
	assign(&j.Type, "type")
	assign(&j.Class, "class")
	assign(&j.ReturnCode, "retcode")
	assign(&j.URL, "url")
	assign(&j.FilesURL, "files-url")
	assign(&j.JobCorrelator, "job-correlator")
	assignInt(&j.Phase, "phase")
	assign(&j.PhaseName, "phase-name")
	assign(&j.ReasonNotRunning, "reason-not-running")
	return err
}

// SpoolFile mirrors the documented z/OSMF job file document fields.
type SpoolFile struct {
	JobName     string
	JobID       string
	ID          int
	DDName      string
	StepName    string
	ProcStep    string // nullable in JSON; empty outside a procedure step
	Class       string
	ByteCount   int64
	RecordCount int64
	RecordsURL  string
}

// UnmarshalJSON accepts the documented z/OSMF job file document fields.
func (s *SpoolFile) UnmarshalJSON(data []byte) error {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		return err
	}
	var err error
	*s = SpoolFile{}
	assign := func(target *string, name string) {
		if err != nil {
			return
		}
		*target, err = jsonStringField(fields, name)
	}
	assignInt := func(target *int, name string) {
		if err != nil {
			return
		}
		*target, err = jsonIntField(fields, name)
	}
	assignInt64 := func(target *int64, name string) {
		if err != nil {
			return
		}
		var value int
		value, err = jsonIntField(fields, name)
		*target = int64(value)
	}
	assign(&s.JobName, "jobname")
	assign(&s.JobID, "jobid")
	assignInt(&s.ID, "id")
	assign(&s.DDName, "ddname")
	assign(&s.StepName, "stepname")
	assign(&s.ProcStep, "procstep")
	assign(&s.Class, "class")
	assignInt64(&s.ByteCount, "byte-count")
	assignInt64(&s.RecordCount, "record-count")
	assign(&s.RecordsURL, "records-url")
	return err
}

// ReadSpoolContentRequest describes one bounded spool-content read. FileID is
// the spool file's numeric id as a string, or the literal "JCL" to retrieve
// the submitted JCL (a pseudo spool file z/OSMF serves but never lists).
type ReadSpoolContentRequest struct {
	JobName  string
	JobID    string
	FileID   string
	Start    int64
	MaxItems int
}

// SpoolContentPage is one bounded, newline-split window of spool text.
// MoreRows is a heuristic (returned line count equals the requested count):
// z/OSMF's text-mode spool response carries no envelope, Content-Length, or
// Content-Range to determine this precisely.
type SpoolContentPage struct {
	Lines    []string
	Start    int64
	MoreRows bool
}

// ListJobs performs one bounded job search.
func (z *Client) ListJobs(ctx context.Context, request ListJobsRequest) (JobPage, error) {
	maxItems, err := validateMaxItems(request.MaxItems)
	if err != nil {
		return JobPage{}, err
	}
	owner, err := normalizeJobFilterValue("owner", request.Owner)
	if err != nil {
		return JobPage{}, err
	}
	prefix, err := normalizeJobFilterValue("prefix", request.Prefix)
	if err != nil {
		return JobPage{}, err
	}

	query := url.Values{}
	if owner != "" {
		query.Set("owner", owner)
	}
	if prefix != "" {
		query.Set("prefix", prefix)
	}
	query.Set("max-jobs", strconv.Itoa(min(maxItems, maxJobsPerRequest)))

	req, err := z.newAPIRequest(ctx, http.MethodGet, "/zosmf/restjobs/jobs", query, nil)
	if err != nil {
		return JobPage{}, err
	}
	res, err := z.doRequest(req, "job list", http.StatusOK)
	if err != nil {
		return JobPage{}, err
	}
	defer res.Body.Close()

	body, err := readBoundedBody(res.Body, maxJobListResponseBody, "job list response")
	if err != nil {
		return JobPage{}, err
	}
	var items []Job
	if len(strings.TrimSpace(string(body))) > 0 {
		if err := json.Unmarshal(body, &items); err != nil {
			return JobPage{}, &ProtocolError{Operation: "job list", Message: "malformed JSON"}
		}
	}
	// z/OSMF gives no pagination signal for this endpoint at all, so the
	// client-side truncation below is the only bound; MoreRows stays false
	// because there is nothing honest to report either way.
	if len(items) > maxItems {
		items = items[:maxItems]
	}
	return JobPage{Items: items}, nil
}

// ListSpoolFiles lists every spool/DD file for one job.
func (z *Client) ListSpoolFiles(ctx context.Context, jobName, jobID string) ([]SpoolFile, error) {
	name, err := normalizeJobFilterValue("job name", jobName)
	if err != nil {
		return nil, err
	}
	if name == "" {
		return nil, &RequestError{Field: "job name", Message: "must not be empty"}
	}
	id, err := normalizeJobFilterValue("job ID", jobID)
	if err != nil {
		return nil, err
	}
	if id == "" {
		return nil, &RequestError{Field: "job ID", Message: "must not be empty"}
	}

	path := "/zosmf/restjobs/jobs/" + url.PathEscape(name) + "/" + url.PathEscape(id) + "/files"
	req, err := z.newAPIRequest(ctx, http.MethodGet, path, nil, nil)
	if err != nil {
		return nil, err
	}
	res, err := z.doRequest(req, name+"/"+id, http.StatusOK)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()

	body, err := readBoundedBody(res.Body, maxSpoolFileListBody, "spool file list response")
	if err != nil {
		return nil, err
	}
	var items []SpoolFile
	if len(strings.TrimSpace(string(body))) > 0 {
		if err := json.Unmarshal(body, &items); err != nil {
			return nil, &ProtocolError{Operation: "spool file list", Message: "malformed JSON"}
		}
	}
	return items, nil
}

// ReadSpoolContent retrieves one bounded, zero-based line range of a spool
// file's text content.
func (z *Client) ReadSpoolContent(ctx context.Context, request ReadSpoolContentRequest) (SpoolContentPage, error) {
	maxItems, err := validateMaxItems(request.MaxItems)
	if err != nil {
		return SpoolContentPage{}, err
	}
	if request.Start < 0 {
		return SpoolContentPage{}, &RequestError{Field: "start", Message: "must be zero or greater"}
	}
	name, err := normalizeJobFilterValue("job name", request.JobName)
	if err != nil {
		return SpoolContentPage{}, err
	}
	if name == "" {
		return SpoolContentPage{}, &RequestError{Field: "job name", Message: "must not be empty"}
	}
	id, err := normalizeJobFilterValue("job ID", request.JobID)
	if err != nil {
		return SpoolContentPage{}, err
	}
	if id == "" {
		return SpoolContentPage{}, &RequestError{Field: "job ID", Message: "must not be empty"}
	}
	fileID := strings.ToUpper(strings.TrimSpace(request.FileID))
	if fileID == "" {
		return SpoolContentPage{}, &RequestError{Field: "spool file id", Message: "must not be empty"}
	}

	path := "/zosmf/restjobs/jobs/" + url.PathEscape(name) + "/" + url.PathEscape(id) +
		"/files/" + url.PathEscape(fileID) + "/records"
	req, err := z.newAPIRequest(ctx, http.MethodGet, path, nil, nil)
	if err != nil {
		return SpoolContentPage{}, err
	}
	req.Header.Set("X-IBM-Record-Range", fmt.Sprintf("%d,%d", request.Start, maxItems))

	res, err := z.doRequest(req, name+"/"+id+"/"+fileID, http.StatusOK)
	if err != nil {
		return SpoolContentPage{}, err
	}
	defer res.Body.Close()

	body, err := readBoundedBody(res.Body, maxSpoolContentBytes, "spool content response")
	if err != nil {
		return SpoolContentPage{}, err
	}
	text := strings.TrimSuffix(string(body), "\n")
	var lines []string
	// A body of just "\n" (one blank output line) trims to "", identical to
	// a genuinely empty body; check the untrimmed body length instead of the
	// trimmed text so the two aren't conflated into the same zero-line result.
	if len(body) > 0 {
		lines = strings.Split(text, "\n")
	}
	// The server is documented to honor X-IBM-Record-Range, but a server
	// that ignores it (as this project's local development mock does) must
	// not silently overrun the caller's budget; cap defensively and treat
	// the cap itself as evidence more content exists.
	moreRows := false
	if len(lines) >= maxItems {
		lines = lines[:maxItems]
		moreRows = true
	}
	return SpoolContentPage{Lines: lines, Start: request.Start, MoreRows: moreRows}, nil
}

// normalizeJobFilterValue trims and uppercases a jobs-API filter value
// (owner, prefix, job name, or job ID), rejecting control characters and
// z/OSMF's documented 8-character limit. Unlike data set names, job filters
// legitimately use "*"/"?" wildcards and carry no other character
// restriction.
func normalizeJobFilterValue(field, value string) (string, error) {
	trimmed := strings.ToUpper(strings.TrimSpace(value))
	if hasControlChar(trimmed) {
		return "", &RequestError{Field: field, Message: "contains invalid control characters"}
	}
	if len(trimmed) > 8 {
		return "", &RequestError{Field: field, Message: "must not exceed 8 characters"}
	}
	return trimmed, nil
}
