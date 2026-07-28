package zosmf

import (
	"context"
	"io"
	"net/http"
)

// SpoolStreamer downloads a spool file's whole text content in one request.
// Deliberately outside JobBrowser (same convention as RecordStreamer):
// interactive browsing must stay bounded, and only callers that have decided
// a single bulk transfer beats further paging should reach for it.
type SpoolStreamer interface {
	OpenSpoolContent(ctx context.Context, jobName, jobID, fileID string) (io.ReadCloser, error)
}

var (
	_ SpoolStreamer = (*Client)(nil)
	_ SpoolStreamer = (*LazyClient)(nil)
)

// OpenSpoolContent streams the whole spool file (or the submitted JCL via
// FileID "JCL") as newline-delimited text, without a record range. The
// caller bounds consumption.
func (z *Client) OpenSpoolContent(ctx context.Context, jobName, jobID, fileID string) (io.ReadCloser, error) {
	if err := browserContextError(ctx); err != nil {
		return nil, err
	}
	path, resource, err := spoolContentPath(jobName, jobID, fileID)
	if err != nil {
		return nil, err
	}
	query := z.spoolQuery()
	req, err := z.newAPIRequest(ctx, http.MethodGet, path, query, nil)
	if err != nil {
		return nil, err
	}
	res, err := z.doRequest(req, resource, http.StatusOK)
	if err != nil {
		return nil, spoolRequestError(err, query)
	}
	z.logf("z/OSMF spool content %s: streaming", resource)
	// The host converts the stream to its network codeset; normalize to
	// UTF-8 (see text.go) so consumers can treat lines as Go strings.
	return hostTextReader(res.Body, res.Header.Get("Content-Type")), nil
}

// OpenSpoolContent lazily initializes the client and streams the whole spool
// file as text.
func (l *LazyClient) OpenSpoolContent(ctx context.Context, jobName, jobID, fileID string) (io.ReadCloser, error) {
	if err := browserContextError(ctx); err != nil {
		return nil, err
	}
	client, err := l.get()
	if err != nil {
		return nil, err
	}
	return client.OpenSpoolContent(ctx, jobName, jobID, fileID)
}

// SpoolFollower tails one spool file: each Poll fetches only the lines
// appended since the previous successful Poll. z/OSMF has no push or
// long-poll interface, so following a running job is bounded polling; the
// caller owns the cadence and decides when the job is done.
type SpoolFollower struct {
	browser JobBrowser
	jobName string
	jobID   string
	fileID  string
	batch   int
	next    int64
}

// FollowSpool starts a follower at line zero. batch bounds one Poll's fetch.
func FollowSpool(browser JobBrowser, jobName, jobID, fileID string, batch int) *SpoolFollower {
	return &SpoolFollower{
		browser: browser,
		jobName: jobName,
		jobID:   jobID,
		fileID:  fileID,
		batch:   batch,
	}
}

// Poll fetches the lines appended since the previous successful Poll. An
// empty result means nothing new yet; more reports a full batch came back,
// so an immediate re-poll is likely to yield further lines. The position
// advances only on success, so a failed Poll can simply be retried.
func (f *SpoolFollower) Poll(ctx context.Context) (lines []string, more bool, err error) {
	page, err := f.browser.ReadSpoolContent(ctx, ReadSpoolContentRequest{
		JobName:  f.jobName,
		JobID:    f.jobID,
		FileID:   f.fileID,
		Start:    f.next,
		MaxItems: f.batch,
	})
	if err != nil {
		return nil, false, err
	}
	f.next += int64(len(page.Lines))
	return page.Lines, page.MoreRows, nil
}

// Position is the zero-based line number the next Poll starts from.
func (f *SpoolFollower) Position() int64 {
	return f.next
}

// SetPosition moves the next Poll to start at the given zero-based line
// number, for callers that already hold the earlier content (e.g. a
// whole-file cache).
func (f *SpoolFollower) SetPosition(position int64) {
	f.next = max(0, position)
}
