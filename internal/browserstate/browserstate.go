// Package browserstate remembers the last applied data set prefix for each
// connection scope. State failures are diagnostic only and never stop browsing.
package browserstate

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"unicode"
)

// StateFileName is the browser state file kept next to cq/config.json.
const StateFileName = "browser-state.json"

type stateFile struct {
	Prefixes map[string]string `json:"prefixes"`
}

// Diagnostic receives non-sensitive store diagnostics. A nil function
// disables diagnostics.
type Diagnostic func(format string, args ...any)

// Store loads and saves the last prefix per opaque connection scope. A missing
// file behaves as an empty store; unreadable or malformed files are preserved.
// Reads and writes are serialized within a store. Each save rereads the file
// to preserve other scopes saved by earlier instances, but simultaneous saves
// by separate instances are not locked against each other.
type Store struct {
	UserConfigDir func() (string, error)
	Diagnostic    Diagnostic

	mu sync.Mutex
}

// DefaultStore returns a store over the current user's configuration directory.
func DefaultStore(diagnostic Diagnostic) *Store {
	return &Store{UserConfigDir: os.UserConfigDir, Diagnostic: diagnostic}
}

// File returns the cq/browser-state.json state path.
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

// Prefix returns the last applied prefix for the scope, or an empty string
// when none is available. Scopes are supplied by the resolved connection.
func (s *Store) Prefix(scope string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	path, err := s.File()
	if err == nil {
		var state stateFile
		state, err = read(path)
		if err == nil {
			return strings.TrimSpace(state.Prefixes[scope])
		}
	}
	s.logf("browser state: %v", err)
	return ""
}

// SetPrefix remembers a nonempty prefix without changing its case or regex
// syntax. Invalid prefixes and persistence failures only emit diagnostics.
func (s *Store) SetPrefix(scope, prefix string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !validPrefix(prefix) {
		s.logf("browser state: prefix is empty or contains control characters")
		return
	}
	path, err := s.File()
	if err == nil {
		var state stateFile
		state, err = read(path)
		if err == nil {
			state.Prefixes[scope] = strings.TrimSpace(prefix)
			err = save(path, state)
		}
	}
	if err != nil {
		s.logf("browser state: prefix not saved: %v", err)
	}
}

func validPrefix(prefix string) bool {
	return strings.TrimSpace(prefix) != "" && strings.IndexFunc(prefix, unicode.IsControl) < 0
}

func read(path string) (stateFile, error) {
	var state stateFile
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return stateFile{Prefixes: make(map[string]string)}, nil
	}
	if err != nil {
		return state, fmt.Errorf("read %q: %w", path, err)
	}
	if err := json.Unmarshal(b, &state); err != nil {
		return state, fmt.Errorf("parse %q: %w", path, err)
	}
	if state.Prefixes == nil {
		return state, fmt.Errorf("invalid prefixes in %q", path)
	}
	for _, prefix := range state.Prefixes {
		if !validPrefix(prefix) {
			return state, fmt.Errorf("invalid prefix in %q", path)
		}
	}
	return state, nil
}

// save replaces the state through a private temporary file in the same
// directory, so a failed write cannot truncate the previous state.
func save(path string, state stateFile) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("create state directory: %w", err)
	}
	b, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".browser-state-*")
	if err != nil {
		return fmt.Errorf("create temporary state file: %w", err)
	}
	defer os.Remove(f.Name())
	_, err = f.Write(append(b, '\n'))
	if err == nil {
		err = f.Sync()
	}
	if closeErr := f.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return fmt.Errorf("write temporary state file: %w", err)
	}
	if err := os.Rename(f.Name(), path); err != nil {
		return fmt.Errorf("replace state file: %w", err)
	}
	return nil
}

func (s *Store) logf(format string, args ...any) {
	if s.Diagnostic != nil {
		s.Diagnostic(format, args...)
	}
}
