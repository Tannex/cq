package zosmf

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
)

const maxTextContent = 16 << 20

// TextEditor is the optional write-capable text surface. It is deliberately
// separate from Browser so read-only clients never gain write access by
// accident; callers must type-assert for it and degrade gracefully when the
// session does not provide one.
type TextEditor interface {
	// ReadText retrieves a data set or member in z/OSMF text mode together
	// with the entity tag needed for a conflict-safe write-back.
	ReadText(ctx context.Context, dsn string) (TextContent, error)
	// WriteText replaces the data set or member content in text mode. It
	// returns the new entity tag when the server reports one.
	WriteText(ctx context.Context, request WriteTextRequest) (string, error)
}

// TextContent is one text-mode retrieval with its entity tag.
type TextContent struct {
	Text []byte
	ETag string
}

// WriteTextRequest describes one explicit text write-back. A non-empty ETag is
// sent as If-Match so a concurrent host-side change fails the save instead of
// being overwritten.
type WriteTextRequest struct {
	Target string
	Body   []byte
	ETag   string
}

// IsConflict reports whether err is an HTTP 412 Precondition Failed, meaning
// the target changed on the host after the entity tag was captured.
func IsConflict(err error) bool {
	var httpErr *HTTPError
	return errors.As(err, &httpErr) && httpErr.StatusCode == http.StatusPreconditionFailed
}

// ReadText retrieves a data set or member in text mode and captures its ETag.
func (z *Client) ReadText(ctx context.Context, dsn string) (TextContent, error) {
	if err := browserContextError(ctx); err != nil {
		return TextContent{}, err
	}
	path := "/zosmf/restfiles/ds/" + url.PathEscape(dsn)
	req, err := z.newAPIRequest(ctx, http.MethodGet, path, nil, nil)
	if err != nil {
		return TextContent{}, err
	}
	req.Header.Set("X-IBM-Data-Type", "text")
	req.Header.Set("X-IBM-Return-Etag", "true")
	res, err := z.doRequest(req, dsn, http.StatusOK)
	if err != nil {
		return TextContent{}, err
	}
	defer res.Body.Close()
	body, err := readBoundedBody(res.Body, maxTextContent, "text content")
	if err != nil {
		return TextContent{}, fmt.Errorf("z/OSMF data set %q: %w", dsn, err)
	}
	return TextContent{Text: body, ETag: res.Header.Get("Etag")}, nil
}

// WriteText writes text content back with If-Match conflict protection.
func (z *Client) WriteText(ctx context.Context, request WriteTextRequest) (string, error) {
	if err := browserContextError(ctx); err != nil {
		return "", err
	}
	target, err := normalizeWriteTarget(request.Target)
	if err != nil {
		return "", err
	}
	path := "/zosmf/restfiles/ds/" + url.PathEscape(target)
	req, err := z.newAPIRequest(ctx, http.MethodPut, path, nil, bytes.NewReader(request.Body))
	if err != nil {
		return "", err
	}
	req.Header.Set("X-IBM-Data-Type", "text")
	req.Header.Set("X-IBM-Return-Etag", "true")
	req.Header.Set("Content-Type", "text/plain")
	if request.ETag != "" {
		req.Header.Set("If-Match", request.ETag)
	}
	res, err := z.doRequest(req, target, http.StatusNoContent, http.StatusCreated)
	if err != nil {
		return "", err
	}
	defer res.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(res.Body, maxErrorBody))
	return res.Header.Get("Etag"), nil
}

func splitMemberTarget(value string) (dataSet, member string, found bool) {
	trimmed := strings.TrimSpace(value)
	open := strings.IndexByte(trimmed, '(')
	if open < 0 || !strings.HasSuffix(trimmed, ")") {
		return trimmed, "", false
	}
	return trimmed[:open], trimmed[open+1 : len(trimmed)-1], true
}

// normalizeWriteTarget accepts DSN or DSN(MEMBER) forms, which the browse
// normalizers reject because they treat parentheses as invalid characters.
func normalizeWriteTarget(value string) (string, error) {
	target, member, found := splitMemberTarget(value)
	dataSet, err := normalizeDataSetName(target)
	if err != nil {
		return "", err
	}
	if !found {
		return dataSet, nil
	}
	normalized, err := normalizeMemberValue("member", member, false)
	if err != nil {
		return "", err
	}
	if normalized == "" {
		return "", &RequestError{Field: "member", Message: "must not be empty"}
	}
	return dataSet + "(" + normalized + ")", nil
}

// ReadText lazily initializes the client and retrieves text content with its ETag.
func (l *LazyClient) ReadText(ctx context.Context, dsn string) (TextContent, error) {
	if err := browserContextError(ctx); err != nil {
		return TextContent{}, err
	}
	client, err := l.get()
	if err != nil {
		return TextContent{}, err
	}
	return client.ReadText(ctx, dsn)
}

// WriteText lazily initializes the client and writes text content back.
func (l *LazyClient) WriteText(ctx context.Context, request WriteTextRequest) (string, error) {
	if err := browserContextError(ctx); err != nil {
		return "", err
	}
	client, err := l.get()
	if err != nil {
		return "", err
	}
	return client.WriteText(ctx, request)
}

var (
	_ TextEditor = (*Client)(nil)
	_ TextEditor = (*LazyClient)(nil)
)
