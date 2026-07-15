// Sidecar transport: cq starts a single long-lived sidecar (see sidecar/)
// that resolves the user's existing Zowe configuration through the Zowe Node
// SDK and streams data sets over the z/OSMF REST API. cq and the sidecar
// speak newline-delimited JSON over stdin/stdout:
//
//	cq -> sidecar: {"id":1,"op":"view","dsn":"HQ.CPY(CUSTOMER)"}
//	               {"id":2,"op":"download","dsn":"HQ.CUSTOMER.DATA","records":10,"reclen":80}
//	               {"id":2,"op":"cancel"}
//	sidecar -> cq: {"ready":true}                       (once, at startup)
//	               {"id":2,"data":"<base64 chunk>"}     (zero or more)
//	               {"id":2,"end":true}                  (or {"id":2,"error":"..."})
//
// Requests are multiplexed: frames for different ids may interleave, which
// lets the copy resolver keep probing libraries concurrently over one warm
// HTTP session. records/reclen on a download let the sidecar bound the
// transfer server-side with X-IBM-Record-Range; the sidecar transparently
// falls back to a full transfer when the data set's records do not match.
package main

import (
	"bufio"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"os/exec"
	"sync"
	"time"
)

const sidecarStartTimeout = 30 * time.Second

// defaultSidecarCommand preserves compatibility with globally installed
// sidecars when cq init has not written a command to the config yet.
const defaultSidecarCommand = "cq-zowe-sidecar"

// zoweTransport is what the copy resolver and main need from the sidecar;
// tests substitute a fake sidecar process behind the same interface.
// Implementations must be safe for concurrent use. Canceling the context
// abandons an in-flight fetch, so a resolver that already has its answer can
// stop probes it no longer needs.
type zoweTransport interface {
	fetchCopybook(ctx context.Context, dsn string) ([]byte, error)
	openDataSet(dsn string, hint downloadHint) (io.ReadCloser, error)
}

// downloadHint bounds a download when cq knows it will not need the whole
// data set: Records > 0 asks the sidecar for a server-side record range of
// Records records of RecordLength bytes each. The sidecar verifies the data
// set's records really have that length and falls back to a full transfer
// when they do not, so the hint can never truncate output.
type downloadHint struct {
	Records      int
	RecordLength int
}

type sidecarRequest struct {
	ID      uint64 `json:"id"`
	Op      string `json:"op"`
	DSN     string `json:"dsn,omitempty"`
	Records int    `json:"records,omitempty"`
	Reclen  int    `json:"reclen,omitempty"`
}

type sidecarFrame struct {
	Ready bool   `json:"ready,omitempty"`
	ID    uint64 `json:"id,omitempty"`
	Data  string `json:"data,omitempty"`
	End   bool   `json:"end,omitempty"`
	Error string `json:"error,omitempty"`
}

type zoweSidecar struct {
	cmd   *exec.Cmd
	stdin io.WriteCloser

	writeMu sync.Mutex // serializes request lines on stdin

	mu      sync.Mutex
	pending map[uint64]sidecarWaiter
	nextID  uint64
	readErr error // why the stdout reader stopped; set before channels close
}

// sidecarWaiter is one request's receiving end. done is closed when the
// consumer abandons the request (cancel/early close), so the frame router
// never blocks sending to a reader that has gone away.
type sidecarWaiter struct {
	ch   chan sidecarFrame
	done chan struct{}
}

// startSidecar launches the sidecar command (an executable — quoted when its
// path has spaces — followed by whitespace-separated arguments) and waits for
// its ready frame, so configuration problems surface before any data set is
// requested.
func startSidecar(command string) (*zoweSidecar, error) {
	name, args, err := splitCommandSpec(command)
	if err != nil {
		return nil, fmt.Errorf("sidecar command %q: %w", command, err)
	}
	cmd := exec.Command(name, args...)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, fmt.Errorf("start sidecar %q: %w", command, err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("start sidecar %q: %w", command, err)
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return nil, fmt.Errorf("start sidecar %q: %w", command, err)
	}
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start sidecar %q: %w", command, err)
	}
	go forwardSidecarStderr(stderr)

	s := &zoweSidecar{
		cmd:     cmd,
		stdin:   stdin,
		pending: make(map[uint64]sidecarWaiter),
	}

	frames := json.NewDecoder(stdout)
	ready := make(chan error, 1)
	go func() {
		var f sidecarFrame
		if err := frames.Decode(&f); err != nil {
			ready <- fmt.Errorf("sidecar %q exited before it was ready: %w", command, err)
			return
		}
		if f.Error != "" {
			ready <- fmt.Errorf("sidecar: %s", f.Error)
			return
		}
		if !f.Ready {
			ready <- fmt.Errorf("sidecar %q sent %+v before a ready frame", command, f)
			return
		}
		ready <- nil
		s.readFrames(frames)
	}()
	select {
	case err := <-ready:
		if err != nil {
			_ = stdin.Close()
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
			return nil, err
		}
	case <-time.After(sidecarStartTimeout):
		_ = stdin.Close()
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		return nil, fmt.Errorf("sidecar %q did not become ready within %s", command, sidecarStartTimeout)
	}
	debugLog.Printf("sidecar ready: %s", command)
	return s, nil
}

// readFrames routes response frames to the requests waiting on them. Frames
// for unknown or abandoned requests (e.g. late chunks after a cancel) are
// dropped.
func (s *zoweSidecar) readFrames(dec *json.Decoder) {
	for {
		var f sidecarFrame
		if err := dec.Decode(&f); err != nil {
			s.mu.Lock()
			if err == io.EOF {
				s.readErr = fmt.Errorf("sidecar exited")
			} else {
				s.readErr = fmt.Errorf("sidecar protocol error: %v", err)
			}
			for id, w := range s.pending {
				close(w.ch)
				delete(s.pending, id)
			}
			s.mu.Unlock()
			return
		}
		terminal := f.End || f.Error != ""
		s.mu.Lock()
		w, ok := s.pending[f.ID]
		if ok && terminal {
			delete(s.pending, f.ID)
		}
		s.mu.Unlock()
		if !ok {
			continue
		}
		select {
		case w.ch <- f:
		case <-w.done:
			// The consumer abandoned this request; drop the frame.
		}
		if terminal {
			close(w.ch)
		}
	}
}

func (s *zoweSidecar) send(req sidecarRequest) error {
	b, err := json.Marshal(req)
	if err != nil {
		return err
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	_, err = s.stdin.Write(append(b, '\n'))
	return err
}

// open registers a request and returns the waiter its frames arrive on.
func (s *zoweSidecar) open(req sidecarRequest) (uint64, sidecarWaiter, error) {
	w := sidecarWaiter{ch: make(chan sidecarFrame, 16), done: make(chan struct{})}
	s.mu.Lock()
	if s.readErr != nil {
		err := s.readErr
		s.mu.Unlock()
		return 0, w, err
	}
	s.nextID++
	id := s.nextID
	s.pending[id] = w
	s.mu.Unlock()

	req.ID = id
	if err := s.send(req); err != nil {
		s.mu.Lock()
		delete(s.pending, id)
		s.mu.Unlock()
		return 0, w, fmt.Errorf("sidecar request for %q: %w", req.DSN, err)
	}
	return id, w, nil
}

// abandon tells the sidecar to stop a request and unblocks the frame router
// if it is mid-delivery. Safe to call once per request.
func (s *zoweSidecar) abandon(id uint64, w sidecarWaiter) {
	_ = s.send(sidecarRequest{ID: id, Op: "cancel"})
	s.mu.Lock()
	delete(s.pending, id)
	s.mu.Unlock()
	close(w.done)
}

func (s *zoweSidecar) exitError() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.readErr != nil {
		return s.readErr
	}
	return fmt.Errorf("sidecar closed the response stream")
}

func elapsed(start time.Time) time.Duration {
	return time.Since(start).Round(time.Millisecond)
}

func (s *zoweSidecar) fetchCopybook(ctx context.Context, dsn string) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	start := time.Now()
	id, w, err := s.open(sidecarRequest{Op: "view", DSN: dsn})
	if err != nil {
		return nil, err
	}
	var out []byte
	for {
		var f sidecarFrame
		var ok bool
		select {
		case f, ok = <-w.ch:
		case <-ctx.Done():
			s.abandon(id, w)
			debugLog.Printf("sidecar view %s: canceled after %s", dsn, elapsed(start))
			return nil, ctx.Err()
		}
		if !ok {
			return nil, s.exitError()
		}
		if f.Error != "" {
			debugLog.Printf("sidecar view %s: failed after %s", dsn, elapsed(start))
			return nil, fmt.Errorf("Zowe data set %q: %s", dsn, f.Error)
		}
		if f.Data != "" {
			chunk, err := base64.StdEncoding.DecodeString(f.Data)
			if err != nil {
				s.abandon(id, w)
				return nil, fmt.Errorf("sidecar sent bad data for %q: %w", dsn, err)
			}
			out = append(out, chunk...)
		}
		if f.End {
			debugLog.Printf("sidecar view %s: %d bytes in %s", dsn, len(out), elapsed(start))
			return out, nil
		}
	}
}

func (s *zoweSidecar) openDataSet(dsn string, hint downloadHint) (io.ReadCloser, error) {
	id, w, err := s.open(sidecarRequest{
		Op: "download", DSN: dsn,
		Records: hint.Records, Reclen: hint.RecordLength,
	})
	if err != nil {
		return nil, err
	}
	st := &sidecarStream{s: s, id: id, dsn: dsn, w: w}
	// Wait for the first frame so failures (not cataloged, permissions)
	// surface before any decoding starts.
	f, ok := <-w.ch
	switch {
	case !ok:
		return nil, s.exitError()
	case f.Error != "":
		return nil, fmt.Errorf("Zowe data set %q: %s", dsn, f.Error)
	case f.End:
		st.done = true
		st.err = io.EOF
	default:
		chunk, err := base64.StdEncoding.DecodeString(f.Data)
		if err != nil {
			st.Close()
			return nil, fmt.Errorf("sidecar sent bad data for %q: %w", dsn, err)
		}
		st.buf = chunk
	}
	if hint.Records > 0 {
		debugLog.Printf("sidecar download %s: streaming (up to %d records of %d bytes)", dsn, hint.Records, hint.RecordLength)
	} else {
		debugLog.Printf("sidecar download %s: streaming", dsn)
	}
	return st, nil
}

// sidecarStream adapts one download's frames to io.ReadCloser so records
// decode straight off the wire, with no temporary file. Close before the end
// frame cancels the transfer, so -max style early exits stop the download.
type sidecarStream struct {
	s    *zoweSidecar
	id   uint64
	dsn  string
	w    sidecarWaiter
	buf  []byte
	err  error
	done bool // the request reached a terminal state or was abandoned
}

func (st *sidecarStream) Read(p []byte) (int, error) {
	for len(st.buf) == 0 {
		if st.err != nil {
			return 0, st.err
		}
		f, ok := <-st.w.ch
		switch {
		case !ok:
			st.done = true
			st.err = st.s.exitError()
		case f.Error != "":
			st.done = true
			st.err = fmt.Errorf("Zowe data set %q: %s", st.dsn, f.Error)
		case f.End:
			st.done = true
			st.err = io.EOF
		default:
			chunk, err := base64.StdEncoding.DecodeString(f.Data)
			if err != nil {
				st.err = fmt.Errorf("sidecar sent bad data for %q: %w", st.dsn, err)
				st.Close()
			} else {
				st.buf = chunk
			}
		}
		if st.err != nil {
			return 0, st.err
		}
	}
	n := copy(p, st.buf)
	st.buf = st.buf[n:]
	return n, nil
}

// Close cancels the transfer when it has not already finished, so early
// exits (-max, decode errors) stop the download instead of pulling the rest
// of the data set. Reads after Close fail instead of waiting for frames that
// the abandoned request will never receive.
func (st *sidecarStream) Close() error {
	if st.done {
		return nil
	}
	st.done = true
	if st.err == nil {
		st.err = io.ErrClosedPipe
	}
	st.s.abandon(st.id, st.w)
	return nil
}

// Close shuts the sidecar down by closing its stdin; the sidecar exits on
// EOF. The wait is bounded so a stuck sidecar cannot hang cq.
func (s *zoweSidecar) Close() error {
	_ = s.stdin.Close()
	done := make(chan error, 1)
	go func() { done <- s.cmd.Wait() }()
	select {
	case <-done:
		return nil
	case <-time.After(5 * time.Second):
		_ = s.cmd.Process.Kill()
		<-done
		return nil
	}
}

// lazySidecar starts the sidecar process on first data set access, so runs
// that never touch a DSN (local copybook, local data) pay no Node startup
// and work without the sidecar installed.
type lazySidecar struct {
	command string
	once    sync.Once
	s       *zoweSidecar
	err     error
}

func newLazySidecar(command string) *lazySidecar {
	if command == "" {
		command = defaultSidecarCommand
	}
	return &lazySidecar{command: command}
}

func (l *lazySidecar) get() (*zoweSidecar, error) {
	l.once.Do(func() {
		l.s, l.err = startSidecar(l.command)
		if l.err != nil && l.command == defaultSidecarCommand {
			l.err = fmt.Errorf("%w\n  (data set access needs the Zowe sidecar: run cq init, or point \"sidecar\" at a custom command in the cq config file)", l.err)
		}
	})
	return l.s, l.err
}

func (l *lazySidecar) fetchCopybook(ctx context.Context, dsn string) ([]byte, error) {
	s, err := l.get()
	if err != nil {
		return nil, err
	}
	return s.fetchCopybook(ctx, dsn)
}

func (l *lazySidecar) openDataSet(dsn string, hint downloadHint) (io.ReadCloser, error) {
	s, err := l.get()
	if err != nil {
		return nil, err
	}
	return s.openDataSet(dsn, hint)
}

// Close shuts down the sidecar if one was started.
func (l *lazySidecar) Close() error {
	if l.s != nil {
		return l.s.Close()
	}
	return nil
}

func forwardSidecarStderr(r io.Reader) {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		if line := sc.Text(); line != "" {
			debugLog.Printf("sidecar: %s", line)
		}
	}
}
