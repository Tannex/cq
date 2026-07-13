package main

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
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

type dsnCopyResolver struct {
	searchPaths []string
	cache       map[string]string
}

func newDSNCopyResolver(searchPaths []string) *dsnCopyResolver {
	return &dsnCopyResolver{searchPaths: searchPaths, cache: make(map[string]string)}
}

func (r *dsnCopyResolver) Resolve(member string) (string, error) {
	member = strings.ToUpper(strings.TrimSpace(member))
	if src, ok := r.cache[member]; ok {
		return src, nil
	}
	if len(r.searchPaths) == 0 {
		return "", fmt.Errorf("COPY %s requires DSNSearchPath in the user config file", member)
	}

	var failures []string
	for _, library := range r.searchPaths {
		dsn := fmt.Sprintf("%s(%s)", library, member)
		src, err := fetchZoweCopybook(dsn)
		if err == nil {
			text := string(src)
			r.cache[member] = text
			return text, nil
		}
		failures = append(failures, err.Error())
	}
	return "", fmt.Errorf("COPY %s was not resolved through DSNSearchPath:\n  %s", member, strings.Join(failures, "\n  "))
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
