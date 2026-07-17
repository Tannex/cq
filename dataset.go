package main

import "io"

type dataSetSource interface {
	copybookFetcher
	encoding() (string, error)
	openDataSet(string, downloadHint) (io.ReadCloser, error)
}

// downloadHint bounds a download when cq knows it will not need the whole
// data set. It is only an optimization: sources must fall back safely when
// the server cannot prove that its records have RecordLength bytes.
type downloadHint struct {
	Records      int
	RecordLength int
}
