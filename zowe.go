package main

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"strings"
)

var zoweCommand = func(args ...string) *exec.Cmd {
	return exec.Command("zowe", args...)
}

func zoweDataSetArgs(dsn string, binary bool) []string {
	args := []string{"zos-files", "view", "data-set", dsn}
	if binary {
		args = append(args, "--binary")
	}
	return args
}

func fetchZoweDataSet(dsn string, binary bool) ([]byte, error) {
	cmd := zoweCommand(zoweDataSetArgs(dsn, binary)...)
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
		return "", fmt.Errorf("COPY %s requires DSNSearchPath in the user config file or --config FILE", member)
	}

	var failures []string
	for _, library := range r.searchPaths {
		dsn := fmt.Sprintf("%s(%s)", library, member)
		src, err := fetchZoweDataSet(dsn, false)
		if err == nil {
			text := string(src)
			r.cache[member] = text
			return text, nil
		}
		failures = append(failures, err.Error())
	}
	return "", fmt.Errorf("COPY %s was not resolved through DSNSearchPath:\n  %s", member, strings.Join(failures, "\n  "))
}

type zoweDataStream struct {
	dsn    string
	cmd    *exec.Cmd
	stdout io.ReadCloser
	stderr bytes.Buffer
}

func openZoweDataSet(dsn string, binary bool) (*zoweDataStream, error) {
	s := &zoweDataStream{dsn: dsn, cmd: zoweCommand(zoweDataSetArgs(dsn, binary)...)}
	s.cmd.Stderr = &s.stderr
	stdout, err := s.cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("prepare Zowe data set %q: %w", dsn, err)
	}
	s.stdout = stdout
	if err := s.cmd.Start(); err != nil {
		return nil, zoweCommandError(dsn, err, s.stderr.String())
	}
	return s, nil
}

func (s *zoweDataStream) Read(p []byte) (int, error) {
	return s.stdout.Read(p)
}

func (s *zoweDataStream) wait() error {
	if err := s.cmd.Wait(); err != nil {
		return zoweCommandError(s.dsn, err, s.stderr.String())
	}
	return nil
}

func (s *zoweDataStream) stop() {
	_ = s.stdout.Close()
	if s.cmd.Process != nil {
		_ = s.cmd.Process.Kill()
	}
	_ = s.cmd.Wait()
}

type eofReader struct {
	io.Reader
	EOF bool
}

func (r *eofReader) Read(p []byte) (int, error) {
	n, err := r.Reader.Read(p)
	if err == io.EOF {
		r.EOF = true
	}
	return n, err
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
