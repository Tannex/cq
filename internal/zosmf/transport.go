// Package zosmf provides the shared z/OSMF data set transport used by cq executables.
package zosmf

import (
	"context"
	"crypto/tls"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Tannex/cq/internal/zowe"
)

const maxErrorBody = 16 * 1024

// Diagnostic receives non-sensitive transport diagnostics. A nil function
// disables diagnostics.
type Diagnostic func(format string, args ...any)

// DownloadHint bounds a download when cq knows it will not need the whole data
// set. It is only an optimization: the client falls back safely when the server
// cannot prove that its records have RecordLength bytes.
type DownloadHint struct {
	Records      int
	RecordLength int
}

func elapsed(start time.Time) time.Duration {
	return time.Since(start).Round(time.Millisecond)
}

// Client talks to z/OSMF in-process. It intentionally has no global timeout;
// bounded browser operations are controlled by their caller-provided contexts.
type Client struct {
	session    zowe.Session
	client     *http.Client
	diagnostic Diagnostic
}

var _ Browser = (*Client)(nil)

// New creates a z/OSMF client from a resolved Zowe session.
func New(session zowe.Session, diagnostic Diagnostic) *Client {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	skipCertificateVerification := session.Protocol == "https" && !session.RejectUnauthorized
	if skipCertificateVerification {
		if transport.TLSClientConfig == nil {
			transport.TLSClientConfig = &tls.Config{}
		}
		// Zowe defines rejectUnauthorized=false as an explicit opt-out from
		// server certificate verification. Keep the opt-out scoped to this
		// cloned transport so it cannot affect other HTTP clients.
		transport.TLSClientConfig.InsecureSkipVerify = true // #nosec G402 -- explicitly requested by the selected Zowe profile.
	}
	return &Client{session: session, client: &http.Client{Transport: transport}, diagnostic: diagnostic}
}

// FetchText retrieves a data set or member in z/OSMF text mode.
func (z *Client) FetchText(ctx context.Context, dsn string) ([]byte, error) {
	if ctx == nil {
		return nil, &RequestError{Field: "context", Message: "must not be nil"}
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	start := time.Now()
	res, err := z.openRequest(ctx, dsn, "text", "")
	if err != nil {
		z.logf("z/OSMF view %s: failed after %s", dsn, elapsed(start))
		return nil, fmt.Errorf("z/OSMF data set %q: %w", dsn, err)
	}
	defer res.Body.Close()
	// Preserve cq's established copybook-fetch behavior: record browsing is
	// strictly bounded, but copybook text has historically been read to EOF.
	body, err := io.ReadAll(res.Body)
	if err != nil {
		return nil, fmt.Errorf("z/OSMF data set %q: read response: %w", dsn, err)
	}
	z.logf("z/OSMF view %s: %d bytes in %s", dsn, len(body), elapsed(start))
	return body, nil
}

// Encoding returns the configured Zowe profile encoding.
func (z *Client) Encoding() (string, error) {
	return z.session.Encoding, nil
}

// OpenDataSet streams a data set in binary mode, optionally trying a bounded
// record-mode request first. This is cq's compatibility path and deliberately
// retains its ranged-to-binary fallback behavior.
func (z *Client) OpenDataSet(dsn string, hint DownloadHint) (io.ReadCloser, error) {
	ctx, cancel := context.WithCancel(context.Background())
	if hint.Records > 0 && hint.RecordLength > 0 {
		res, err := z.openRequest(ctx, dsn, "record", fmt.Sprintf("0,%d", hint.Records))
		if err == nil {
			z.logf("z/OSMF download %s: streaming (up to %d records of %d bytes)", dsn, hint.Records, hint.RecordLength)
			return &rangedDataSetStream{
				client: z, context: ctx, cancel: cancel, body: res.Body,
				dsn: dsn, recordLength: hint.RecordLength,
			}, nil
		}
		z.logf("z/OSMF download %s: ranged request failed (%v); retrying without record range", dsn, err)
	}

	res, err := z.openRequest(ctx, dsn, "binary", "")
	if err != nil {
		cancel()
		return nil, fmt.Errorf("z/OSMF data set %q: %w", dsn, err)
	}
	z.logf("z/OSMF download %s: streaming", dsn)
	return &cancelingReadCloser{ReadCloser: res.Body, cancel: cancel}, nil
}

func (z *Client) openRequest(ctx context.Context, dsn, dataType, recordRange string) (*http.Response, error) {
	path := "/zosmf/restfiles/ds/" + url.PathEscape(dsn)
	req, err := z.newAPIRequest(ctx, http.MethodGet, path, nil, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("X-IBM-Data-Type", dataType)
	if recordRange != "" {
		req.Header.Set("X-IBM-Record-Range", recordRange)
	}
	return z.doRequest(req, dsn, http.StatusOK)
}

func (z *Client) newAPIRequest(ctx context.Context, method, path string, query url.Values, body io.Reader) (*http.Request, error) {
	if ctx == nil {
		return nil, &RequestError{Field: "context", Message: "must not be nil"}
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	host := strings.Trim(z.session.Host, "[]")
	hostPort := net.JoinHostPort(host, strconv.Itoa(z.session.Port))
	basePath := strings.Trim(strings.TrimSpace(z.session.BasePath), "/")
	if basePath != "" {
		basePath = "/" + basePath
	}
	endpoint := fmt.Sprintf("%s://%s%s%s", z.session.Protocol, hostPort, basePath, path)
	if len(query) > 0 {
		endpoint += "?" + query.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, method, endpoint, body)
	if err != nil {
		return nil, fmt.Errorf("build z/OSMF request: %w", err)
	}
	req.Header.Set("X-CSRF-ZOSMF-HEADER", "")
	req.Header.Set("X-IBM-Migrated-Recall", "error")
	if z.session.TokenValue != "" {
		cookie := z.session.TokenType
		if cookie == "" {
			cookie = "apimlAuthenticationToken"
		}
		req.Header.Set("Cookie", cookie+"="+z.session.TokenValue)
	} else {
		req.SetBasicAuth(z.session.User, z.session.Password)
	}
	return req, nil
}

func (z *Client) doRequest(req *http.Request, resource string, successCodes ...int) (*http.Response, error) {
	res, err := z.client.Do(req)
	if err != nil {
		if contextErr := req.Context().Err(); contextErr != nil {
			return nil, contextErr
		}
		return nil, fmt.Errorf("z/OSMF request for %s: %w", resource, err)
	}
	for _, statusCode := range successCodes {
		if res.StatusCode == statusCode {
			return res, nil
		}
	}
	defer res.Body.Close()
	return nil, z.httpError(res, resource)
}

func (z *Client) httpError(res *http.Response, resource string) error {
	body, truncated, readErr := readAtMost(res.Body, maxErrorBody)
	if readErr != nil {
		return &HTTPError{
			StatusCode: res.StatusCode,
			Resource:   resource,
			Message:    z.redact(fmt.Sprintf("%s (could not read error response: %v)", http.StatusText(res.StatusCode), readErr)),
		}
	}
	code, message := parseErrorBody(body)
	if message == "" {
		message = http.StatusText(res.StatusCode)
	}
	return &HTTPError{
		StatusCode: res.StatusCode,
		Resource:   resource,
		Code:       z.redact(code),
		Message:    z.redact(message),
		Truncated:  truncated,
	}
}

func parseErrorBody(body []byte) (string, string) {
	trimmed := strings.TrimSpace(string(body))
	if trimmed == "" {
		return "", ""
	}
	decoder := json.NewDecoder(strings.NewReader(trimmed))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return "", trimmed
	}
	if object, ok := value.(map[string]any); ok {
		if code, message := errorFields(object); code != "" || message != "" {
			return code, message
		}
	}
	return "", ""
}

func errorFields(object map[string]any) (string, string) {
	code := firstString(object, "code", "errorCode")
	message := firstString(object, "message", "messageText", "detail", "description")
	if message != "" {
		return code, message
	}
	for _, key := range []string{"error", "errors", "messages"} {
		value, ok := lookupAnyField(object, key)
		if !ok {
			continue
		}
		switch nested := value.(type) {
		case map[string]any:
			nestedCode, nestedMessage := errorFields(nested)
			if code == "" {
				code = nestedCode
			}
			if nestedMessage != "" {
				return code, nestedMessage
			}
		case []any:
			for _, item := range nested {
				if child, ok := item.(map[string]any); ok {
					nestedCode, nestedMessage := errorFields(child)
					if code == "" {
						code = nestedCode
					}
					if nestedMessage != "" {
						return code, nestedMessage
					}
				}
			}
		}
	}
	return code, ""
}

func firstString(object map[string]any, names ...string) string {
	for _, name := range names {
		if value, ok := lookupAnyField(object, name); ok {
			if text, ok := value.(string); ok {
				return strings.TrimSpace(text)
			}
		}
	}
	return ""
}

func lookupAnyField(object map[string]any, name string) (any, bool) {
	if value, ok := object[name]; ok {
		return value, true
	}
	for key, value := range object {
		if strings.EqualFold(key, name) {
			return value, true
		}
	}
	return nil, false
}

func (z *Client) redact(value string) string {
	secrets := []string{z.session.Password, z.session.TokenValue}
	if z.session.Password != "" {
		basic := base64.StdEncoding.EncodeToString([]byte(z.session.User + ":" + z.session.Password))
		secrets = append(secrets, basic, url.QueryEscape(z.session.Password), url.PathEscape(z.session.Password))
	}
	if z.session.TokenValue != "" {
		secrets = append(secrets,
			base64.StdEncoding.EncodeToString([]byte(z.session.TokenValue)),
			url.QueryEscape(z.session.TokenValue),
			url.PathEscape(z.session.TokenValue),
		)
	}
	for _, secret := range secrets {
		if secret != "" {
			value = strings.ReplaceAll(value, secret, "[redacted]")
		}
	}
	return value
}

func readBoundedBody(reader io.Reader, limit int64, kind string) ([]byte, error) {
	body, truncated, err := readAtMost(reader, limit)
	if err != nil {
		return nil, fmt.Errorf("read z/OSMF %s: %w", kind, err)
	}
	if truncated {
		return nil, &LimitError{Kind: kind, Limit: limit}
	}
	return body, nil
}

func readAtMost(reader io.Reader, limit int64) ([]byte, bool, error) {
	body, err := io.ReadAll(io.LimitReader(reader, limit+1))
	if err != nil {
		return nil, false, err
	}
	if int64(len(body)) > limit {
		return body[:limit], true, nil
	}
	return body, false, nil
}

func (z *Client) logf(format string, args ...any) {
	if z.diagnostic != nil {
		z.diagnostic(format, args...)
	}
}

type cancelingReadCloser struct {
	io.ReadCloser
	cancel context.CancelFunc
}

func (r *cancelingReadCloser) Close() error {
	err := r.ReadCloser.Close()
	r.cancel()
	return err
}

// rangedDataSetStream removes z/OSMF's four-byte record lengths. If the server
// returns an unexpected record size or a partial ranged response, it switches
// to a normal binary request and skips bytes already delivered. The hint can
// therefore improve transfer size without changing cq's output.
type rangedDataSetStream struct {
	client       *Client
	context      context.Context
	cancel       context.CancelFunc
	body         io.ReadCloser
	dsn          string
	recordLength int
	pending      []byte
	delivered    int64
	plain        bool
	closed       bool
	err          error
}

func (r *rangedDataSetStream) Read(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	if r.closed {
		return 0, io.ErrClosedPipe
	}
	for len(r.pending) == 0 {
		if r.err != nil {
			return 0, r.err
		}
		if r.plain {
			return r.body.Read(p)
		}

		var header [4]byte
		_, err := io.ReadFull(r.body, header[:])
		if errors.Is(err, io.EOF) {
			return 0, io.EOF
		}
		if err != nil {
			if fallbackErr := r.fallBack(fmt.Sprintf("ranged response ended inside a record header (%v)", err)); fallbackErr != nil {
				return 0, fallbackErr
			}
			continue
		}
		length := int(binary.BigEndian.Uint32(header[:]))
		if length != r.recordLength {
			if err := r.fallBack(fmt.Sprintf("data set record is %d bytes, cq record is %d", length, r.recordLength)); err != nil {
				return 0, err
			}
			continue
		}
		record := make([]byte, length)
		if _, err := io.ReadFull(r.body, record); err != nil {
			if fallbackErr := r.fallBack(fmt.Sprintf("ranged response ended inside a record (%v)", err)); fallbackErr != nil {
				return 0, fallbackErr
			}
			continue
		}
		r.pending = record
	}
	n := copy(p, r.pending)
	r.pending = r.pending[n:]
	r.delivered += int64(n)
	return n, nil
}

func (r *rangedDataSetStream) fallBack(reason string) error {
	_ = r.body.Close()
	r.client.logf("z/OSMF download %s: %s; retrying without record range", r.dsn, reason)
	res, err := r.client.openRequest(r.context, r.dsn, "binary", "")
	if err != nil {
		r.err = fmt.Errorf("z/OSMF data set %q: ranged response invalid (%s), and fallback failed: %w", r.dsn, reason, err)
		return r.err
	}
	if r.delivered > 0 {
		if _, err := io.CopyN(io.Discard, res.Body, r.delivered); err != nil {
			_ = res.Body.Close()
			r.err = fmt.Errorf("z/OSMF data set %q: fallback response is shorter than the %d bytes already delivered: %w", r.dsn, r.delivered, err)
			return r.err
		}
	}
	r.body = res.Body
	r.plain = true
	return nil
}

func (r *rangedDataSetStream) Close() error {
	if r.closed {
		return nil
	}
	r.closed = true
	r.cancel()
	return r.body.Close()
}

func browserContextError(ctx context.Context) error {
	if ctx == nil {
		return &RequestError{Field: "context", Message: "must not be nil"}
	}
	return ctx.Err()
}

// LazyClient defers Zowe configuration and keyring access until a DSN is
// actually used, preserving local-only cq behavior.
type LazyClient struct {
	load       func() (zowe.Session, error)
	diagnostic Diagnostic
	once       sync.Once
	client     *Client
	err        error
}

var _ Browser = (*LazyClient)(nil)

// NewLazy creates a lazily initialized z/OSMF client.
func NewLazy(load func() (zowe.Session, error), diagnostic Diagnostic) *LazyClient {
	return &LazyClient{load: load, diagnostic: diagnostic}
}

func (l *LazyClient) get() (*Client, error) {
	l.once.Do(func() {
		var session zowe.Session
		session, l.err = l.load()
		if l.err == nil {
			if l.diagnostic != nil {
				l.diagnostic("Zowe profile %s: %s://%s:%d%s", session.Profile, session.Protocol, session.Host, session.Port, session.BasePath)
			}
			l.client = New(session, l.diagnostic)
		}
	})
	return l.client, l.err
}

// FetchText lazily initializes the client and retrieves a text data set or member.
func (l *LazyClient) FetchText(ctx context.Context, dsn string) ([]byte, error) {
	if err := browserContextError(ctx); err != nil {
		return nil, err
	}
	client, err := l.get()
	if err != nil {
		return nil, err
	}
	return client.FetchText(ctx, dsn)
}

// Encoding lazily initializes the client and returns its profile encoding.
func (l *LazyClient) Encoding() (string, error) {
	client, err := l.get()
	if err != nil {
		return "", err
	}
	return client.Encoding()
}

// OpenDataSet lazily initializes the client and streams a data set.
func (l *LazyClient) OpenDataSet(dsn string, hint DownloadHint) (io.ReadCloser, error) {
	client, err := l.get()
	if err != nil {
		return nil, err
	}
	return client.OpenDataSet(dsn, hint)
}

// ListDataSets lazily initializes the client and performs a bounded catalog search.
func (l *LazyClient) ListDataSets(ctx context.Context, request ListDataSetsRequest) (DataSetPage, error) {
	if err := browserContextError(ctx); err != nil {
		return DataSetPage{}, err
	}
	client, err := l.get()
	if err != nil {
		return DataSetPage{}, err
	}
	return client.ListDataSets(ctx, request)
}

// ListMembers lazily initializes the client and performs a bounded member search.
func (l *LazyClient) ListMembers(ctx context.Context, request ListMembersRequest) (MemberPage, error) {
	if err := browserContextError(ctx); err != nil {
		return MemberPage{}, err
	}
	client, err := l.get()
	if err != nil {
		return MemberPage{}, err
	}
	return client.ListMembers(ctx, request)
}

// ReadRecords lazily initializes the client and performs a bounded record request.
func (l *LazyClient) ReadRecords(ctx context.Context, request ReadRecordsRequest) (RecordPage, error) {
	if err := browserContextError(ctx); err != nil {
		return RecordPage{}, err
	}
	client, err := l.get()
	if err != nil {
		return RecordPage{}, err
	}
	return client.ReadRecords(ctx, request)
}

// ListJobs lazily initializes the client and performs a bounded job search.
func (l *LazyClient) ListJobs(ctx context.Context, request ListJobsRequest) (JobPage, error) {
	if err := browserContextError(ctx); err != nil {
		return JobPage{}, err
	}
	client, err := l.get()
	if err != nil {
		return JobPage{}, err
	}
	return client.ListJobs(ctx, request)
}

// ListSpoolFiles lazily initializes the client and lists a job's spool files.
func (l *LazyClient) ListSpoolFiles(ctx context.Context, jobName, jobID string) ([]SpoolFile, error) {
	if err := browserContextError(ctx); err != nil {
		return nil, err
	}
	client, err := l.get()
	if err != nil {
		return nil, err
	}
	return client.ListSpoolFiles(ctx, jobName, jobID)
}

// ReadSpoolContent lazily initializes the client and reads bounded spool content.
func (l *LazyClient) ReadSpoolContent(ctx context.Context, request ReadSpoolContentRequest) (SpoolContentPage, error) {
	if err := browserContextError(ctx); err != nil {
		return SpoolContentPage{}, err
	}
	client, err := l.get()
	if err != nil {
		return SpoolContentPage{}, err
	}
	return client.ReadSpoolContent(ctx, request)
}
