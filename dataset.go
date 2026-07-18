package main

import (
	"io"

	"github.com/Tannex/cq/internal/dsncopy"
	"github.com/Tannex/cq/internal/zosmf"
)

type dataSetSource interface {
	dsncopy.Fetcher
	Encoding() (string, error)
	OpenDataSet(string, zosmf.DownloadHint) (io.ReadCloser, error)
}
