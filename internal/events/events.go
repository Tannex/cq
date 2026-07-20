// Package events records local usage — data set opens and executed jq
// queries — in an append-only JSONL log. The log feeds the future suggestion
// engine (recency-weighted scoring needs per-event timestamps) and doubles as
// the query console's command history. Nothing recorded ever leaves the
// machine, and the file stays human-readable so the user can audit exactly
// what is tracked.
package events

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// StateFileName is the event log kept next to cq/config.json.
const StateFileName = "events.jsonl"

const (
	// keep is how many events survive compaction and how many Events returns.
	keep = 1000
	// slack is the line count that triggers compaction, so appends stay O(1)
	// instead of rewriting the file on every event.
	slack = 2000
)

// Kind labels an event line.
type Kind string

const (
	KindOpen  Kind = "open"
	KindQuery Kind = "query"
)

// Outcome reports whether a query ran or failed to compile.
type Outcome string

const (
	OutcomeOK    Outcome = "ok"
	OutcomeError Outcome = "error"
)

// Event is one JSONL line. Name is set for data set opens, Query and Outcome
// for executed queries. Query events never carry record data, copybook
// contents, or field values — the expression text only.
type Event struct {
	Kind    Kind      `json:"kind"`
	Time    time.Time `json:"time"`
	Name    string    `json:"name,omitempty"`
	Query   string    `json:"query,omitempty"`
	Outcome Outcome   `json:"outcome,omitempty"`
}

// Diagnostic receives non-sensitive store diagnostics. A nil function
// disables diagnostics.
type Diagnostic func(format string, args ...any)

// Store appends usage events to the JSONL log. Each event is a single
// O_APPEND write, durable immediately and safe when several instances
// interleave appends. Every failure degrades silently to the diagnostic so
// tracking can never disturb the TUI. UserConfigDir and Now are injectable
// for tests.
type Store struct {
	UserConfigDir func() (string, error)
	Diagnostic    Diagnostic
	Now           func() time.Time

	mu      sync.Mutex
	counted bool
	count   int
}

// DefaultStore returns a store over the current user's configuration directory.
func DefaultStore(diagnostic Diagnostic) *Store {
	return &Store{UserConfigDir: os.UserConfigDir, Diagnostic: diagnostic}
}

// File returns the cq/events.jsonl log path.
func (s *Store) File() (string, error) {
	userConfigDir := s.UserConfigDir
	if userConfigDir == nil {
		userConfigDir = os.UserConfigDir
	}
	dir, err := userConfigDir()
	if err != nil {
		return "", fmt.Errorf("locate user config directory: %w", err)
	}
	return filepath.Join(dir, "cq", StateFileName), nil
}

func (s *Store) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now().UTC()
}

// RecordOpen appends a data set open event.
func (s *Store) RecordOpen(name string) {
	s.append(Event{Kind: KindOpen, Name: name})
}

// RecordQuery appends an executed-query event with its outcome.
func (s *Store) RecordQuery(expr string, ok bool) {
	outcome := OutcomeError
	if ok {
		outcome = OutcomeOK
	}
	s.append(Event{Kind: KindQuery, Query: expr, Outcome: outcome})
}

// Events returns the retained events, oldest first, tolerating corrupt lines.
func (s *Store) Events() []Event {
	s.mu.Lock()
	defer s.mu.Unlock()
	path, err := s.File()
	if err != nil {
		s.logf("events: no user config directory: %v", err)
		return nil
	}
	events, _ := s.read(path)
	if len(events) > keep {
		events = events[len(events)-keep:]
	}
	return events
}

func (s *Store) append(event Event) {
	s.mu.Lock()
	defer s.mu.Unlock()
	event.Time = s.now()
	path, err := s.File()
	if err != nil {
		s.logf("events: no user config directory: %v", err)
		return
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		s.logf("events: create state directory: %v", err)
		return
	}
	if !s.counted {
		s.count = countLines(path)
		s.counted = true
	}
	line, err := json.Marshal(event)
	if err != nil {
		s.logf("events: encode event: %v", err)
		return
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		s.logf("events: open %q: %v", path, err)
		return
	}
	_, writeErr := f.Write(append(line, '\n'))
	if closeErr := f.Close(); writeErr == nil {
		writeErr = closeErr
	}
	if writeErr != nil {
		s.logf("events: append to %q: %v", path, writeErr)
		return
	}
	s.count++
	if s.count > slack {
		s.compact(path)
	}
}

// compact rewrites the log with the newest events only. The rewrite goes
// through a temp file and rename so a crash cannot truncate the log.
func (s *Store) compact(path string) {
	events, ok := s.read(path)
	if !ok {
		return
	}
	if len(events) > keep {
		events = events[len(events)-keep:]
	}
	var buf bytes.Buffer
	for _, event := range events {
		line, err := json.Marshal(event)
		if err != nil {
			continue
		}
		buf.Write(line)
		buf.WriteByte('\n')
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, buf.Bytes(), 0o600); err != nil {
		s.logf("events: write compacted log: %v", err)
		return
	}
	if err := os.Rename(tmp, path); err != nil {
		s.logf("events: replace log: %v", err)
		return
	}
	s.count = len(events)
}

// read parses the log, skipping corrupt lines. ok is false only when the file
// exists but cannot be read at all.
func (s *Store) read(path string) (events []Event, ok bool) {
	f, err := os.Open(path)
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			s.logf("events: read %q: %v", path, err)
			return nil, false
		}
		return nil, true
	}
	defer f.Close()
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for scanner.Scan() {
		line := bytes.TrimSpace(scanner.Bytes())
		if len(line) == 0 {
			continue
		}
		var event Event
		if err := json.Unmarshal(line, &event); err != nil {
			s.logf("events: skipping corrupt line: %v", err)
			continue
		}
		events = append(events, event)
	}
	if err := scanner.Err(); err != nil {
		// A partial read must not report ok: compact would rewrite the log
		// from the truncated slice and drop the unread tail.
		s.logf("events: scan %q: %v", path, err)
		return events, false
	}
	return events, true
}

func countLines(path string) int {
	b, err := os.ReadFile(path)
	if err != nil {
		return 0
	}
	return bytes.Count(b, []byte("\n"))
}

func (s *Store) logf(format string, args ...any) {
	if s.Diagnostic != nil {
		s.Diagnostic(format, args...)
	}
}
