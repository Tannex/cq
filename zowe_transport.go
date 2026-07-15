package main

import "time"

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
