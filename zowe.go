package main

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"sync"
)

var zoweCommand = func(args ...string) *exec.Cmd {
	return exec.Command("zowe", args...)
}

func fetchZoweCopybook(dsn string) ([]byte, error) {
	cmd := zoweCommand("zos-files", "view", "data-set", dsn)
	out, err := cmd.Output()
	if err != nil {
		return nil, zoweCommandError(dsn, err, exitStderr(err))
	}
	return out, nil
}

// maxConcurrentZoweFetches bounds the zowe processes a resolver runs at once;
// each invocation starts a Node.js process and a z/OSMF request.
const maxConcurrentZoweFetches = 8

type dsnCopyResolver struct {
	searchPaths []string
	slots       chan struct{}

	mu    sync.Mutex
	cache map[string]copyResult
}

type copyResult struct {
	text string
	err  error
}

func newDSNCopyResolver(searchPaths []string) *dsnCopyResolver {
	return &dsnCopyResolver{
		searchPaths: searchPaths,
		slots:       make(chan struct{}, maxConcurrentZoweFetches),
		cache:       make(map[string]copyResult),
	}
}

// Resolve is safe for concurrent use so COPY expansion can prefetch the
// members of a level in parallel.
func (r *dsnCopyResolver) Resolve(member string) (string, error) {
	member = strings.ToUpper(strings.TrimSpace(member))
	r.mu.Lock()
	res, ok := r.cache[member]
	r.mu.Unlock()
	if ok {
		return res.text, res.err
	}
	if len(r.searchPaths) == 0 {
		return "", fmt.Errorf("COPY %s requires DSNSearchPath in the user config file", member)
	}
	res = r.probeSearchPaths(member)
	r.mu.Lock()
	r.cache[member] = res
	r.mu.Unlock()
	return res.text, res.err
}

// probeSearchPaths queries every library concurrently and keeps the result
// from the earliest library in search order that has the member, so the
// configured precedence still decides which copy wins.
func (r *dsnCopyResolver) probeSearchPaths(member string) copyResult {
	results := make([]copyResult, len(r.searchPaths))
	var wg sync.WaitGroup
	for i, library := range r.searchPaths {
		wg.Add(1)
		go func() {
			defer wg.Done()
			r.slots <- struct{}{}
			defer func() { <-r.slots }()
			src, err := fetchZoweCopybook(fmt.Sprintf("%s(%s)", library, member))
			results[i] = copyResult{text: string(src), err: err}
		}()
	}
	wg.Wait()

	var failures []string
	for _, res := range results {
		if res.err == nil {
			return copyResult{text: res.text}
		}
		failures = append(failures, res.err.Error())
	}
	return copyResult{err: fmt.Errorf("COPY %s was not resolved through DSNSearchPath:\n  %s", member, strings.Join(failures, "\n  "))}
}

type temporaryDataFile struct {
	*os.File
	path string
}

func downloadZoweDataSet(dsn string) (*temporaryDataFile, error) {
	temp, err := os.CreateTemp("", "cq-zowe-*.bin")
	if err != nil {
		return nil, fmt.Errorf("create temporary file for Zowe data set %q: %w", dsn, err)
	}
	path := temp.Name()
	if err := temp.Close(); err != nil {
		_ = os.Remove(path)
		return nil, fmt.Errorf("prepare temporary file for Zowe data set %q: %w", dsn, err)
	}
	ok := false
	defer func() {
		if !ok {
			_ = os.Remove(path)
		}
	}()

	cmd := zoweCommand("zos-files", "download", "data-set", dsn,
		"--binary", "--file", path, "--overwrite")
	if _, err := cmd.Output(); err != nil {
		return nil, zoweCommandError(dsn, err, exitStderr(err))
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open downloaded Zowe data set %q: %w", dsn, err)
	}
	ok = true
	return &temporaryDataFile{File: f, path: path}, nil
}

func (f *temporaryDataFile) Close() error {
	closeErr := f.File.Close()
	removeErr := os.Remove(f.path)
	if errors.Is(removeErr, os.ErrNotExist) {
		removeErr = nil
	}
	return errors.Join(closeErr, removeErr)
}

func exitStderr(err error) string {
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return string(exitErr.Stderr)
	}
	return ""
}

func zoweCommandError(dsn string, err error, stderr string) error {
	if message := strings.TrimSpace(stderr); message != "" {
		return fmt.Errorf("Zowe data set %q: %s", dsn, message)
	}
	return fmt.Errorf("Zowe data set %q: %w", dsn, err)
}
