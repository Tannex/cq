// Command package-sidecar creates the deterministic npm package resource
// embedded by cq. Run it from the repository root through go generate.
package main

import (
	"archive/tar"
	"compress/gzip"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"
)

var files = []string{"package.json", "package-lock.json", "zowe-sidecar.js"}

func main() {
	output := flag.String("output", "resources/cq-zowe-sidecar.tgz", "archive to create")
	flag.Parse()
	if err := run(*output); err != nil {
		fmt.Fprintln(os.Stderr, "package-sidecar:", err)
		os.Exit(1)
	}
}

func run(output string) (result error) {
	if err := os.MkdirAll(filepath.Dir(output), 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(output), ".sidecar-*.tgz")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer func() {
		_ = tmp.Close()
		_ = os.Remove(tmpName)
	}()

	gz := gzip.NewWriter(tmp)
	gz.Header.ModTime = time.Unix(0, 0)
	gz.Header.OS = 255
	tw := tar.NewWriter(gz)
	for _, name := range files {
		path := filepath.Join("sidecar", name)
		info, err := os.Stat(path)
		if err != nil {
			return err
		}
		h := &tar.Header{
			Name:    "package/" + name,
			Mode:    int64(info.Mode().Perm()),
			Size:    info.Size(),
			ModTime: time.Unix(0, 0),
		}
		if err := tw.WriteHeader(h); err != nil {
			return err
		}
		f, err := os.Open(path)
		if err != nil {
			return err
		}
		_, copyErr := io.Copy(tw, f)
		closeErr := f.Close()
		if copyErr != nil {
			return copyErr
		}
		if closeErr != nil {
			return closeErr
		}
	}
	if err := tw.Close(); err != nil {
		return err
	}
	if err := gz.Close(); err != nil {
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpName, output)
}
