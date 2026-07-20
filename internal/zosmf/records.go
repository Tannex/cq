package zosmf

import (
	"context"
	"fmt"
	"io"
)

// RecordStreamer downloads every record of a data set in one request, keeping
// z/OSMF's four-byte length-prefixed record frames. It is deliberately outside
// Browser: interactive browsing must stay bounded, and only callers that have
// decided a single bulk transfer beats further paging — like a long-running
// search — should reach for it.
type RecordStreamer interface {
	OpenRecords(ctx context.Context, dsn string) (io.ReadCloser, error)
}

var (
	_ RecordStreamer = (*Client)(nil)
	_ RecordStreamer = (*LazyClient)(nil)
)

// OpenRecords streams the whole data set (or member) in record mode without a
// record range. The body delivers four-byte big-endian length headers before
// each record, exactly as ReadRecords consumes them.
func (z *Client) OpenRecords(ctx context.Context, dsn string) (io.ReadCloser, error) {
	if err := browserContextError(ctx); err != nil {
		return nil, err
	}
	res, err := z.openRequest(ctx, dsn, "record", "")
	if err != nil {
		return nil, fmt.Errorf("z/OSMF data set %q: %w", dsn, err)
	}
	z.logf("z/OSMF bulk records %s: streaming", dsn)
	return res.Body, nil
}

// OpenRecords lazily initializes the client and streams the whole data set in
// record mode.
func (l *LazyClient) OpenRecords(ctx context.Context, dsn string) (io.ReadCloser, error) {
	if err := browserContextError(ctx); err != nil {
		return nil, err
	}
	client, err := l.get()
	if err != nil {
		return nil, err
	}
	return client.OpenRecords(ctx, dsn)
}
