package main

import (
	"context"
	"io"
	"time"
)

// zoweTransport is the data-set boundary used by the native implementation
// and tests. Implementations must be safe for concurrent use. Canceling the
// context abandons an in-flight copybook fetch.
type zoweTransport interface {
	fetchCopybook(ctx context.Context, dsn string) ([]byte, error)
	openDataSet(dsn string, hint downloadHint) (io.ReadCloser, error)
}

// downloadHint bounds a download when cq knows it will not need the whole
// data set. It is only an optimization: transports must fall back safely when
// the server cannot prove that its records have RecordLength bytes.
type downloadHint struct {
	Records      int
	RecordLength int
}

func elapsed(start time.Time) time.Duration {
	return time.Since(start).Round(time.Millisecond)
}
