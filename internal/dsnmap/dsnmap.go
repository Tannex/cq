// Package dsnmap persists DSN → copybook mappings so known data sets get
// their display overlay applied automatically across sessions.
package dsnmap

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// StateFileName is the mapping state file kept next to cq/config.json.
const StateFileName = "dsn-copybooks.json"

// Mapping binds a DSN pattern to a copybook source. Pattern wildcards follow
// the existing cq dialect: a trailing or embedded * matches any run of
// characters and % matches exactly one character.
type Mapping struct {
	Pattern  string    `json:"pattern"`
	Local    string    `json:"local,omitempty"`
	DSN      string    `json:"dsn,omitempty"`
	Format   string    `json:"format,omitempty"`
	Record   string    `json:"record,omitempty"`
	Created  time.Time `json:"created"`
	LastUsed time.Time `json:"lastUsed"`
}

type stateFile struct {
	Mappings []Mapping `json:"mappings"`
}

// Diagnostic receives non-sensitive store diagnostics. A nil function
// disables diagnostics.
type Diagnostic func(format string, args ...any)

// Store loads and saves the mapping state file. UserConfigDir and Now are
// injectable so tests can isolate the real configuration directory and clock.
// A missing state file behaves as an empty store; a corrupt one degrades to an
// empty read-only store so browsing keeps working and the broken file is never
// silently overwritten.
type Store struct {
	UserConfigDir func() (string, error)
	Diagnostic    Diagnostic
	Now           func() time.Time

	loaded   bool
	loadErr  error
	mappings []Mapping
}

// DefaultStore returns a store over the current user's configuration directory.
func DefaultStore(diagnostic Diagnostic) *Store {
	return &Store{UserConfigDir: os.UserConfigDir, Diagnostic: diagnostic}
}

// File returns the unchanged cq/dsn-copybooks.json state path.
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

func (s *Store) load() {
	if s.loaded {
		return
	}
	s.loaded = true
	path, err := s.File()
	if err != nil {
		s.logf("dsnmap: no user config directory: %v", err)
		return
	}
	b, err := os.ReadFile(path)
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			s.loadErr = fmt.Errorf("read mappings %q: %w", path, err)
			s.logf("dsnmap: %v", s.loadErr)
		}
		return
	}
	var state stateFile
	if err := json.Unmarshal(b, &state); err != nil {
		s.loadErr = fmt.Errorf("parse mappings %q: %w", path, err)
		s.logf("dsnmap: %v", s.loadErr)
		return
	}
	mappings := make([]Mapping, 0, len(state.Mappings))
	for _, mapping := range state.Mappings {
		mapping.Pattern = strings.ToUpper(strings.TrimSpace(mapping.Pattern))
		if mapping.Pattern == "" {
			continue
		}
		mappings = append(mappings, mapping)
	}
	s.mappings = mappings
}

func (s *Store) save() error {
	if s.loadErr != nil {
		return fmt.Errorf("mappings not saved to protect the unreadable state file: %w", s.loadErr)
	}
	path, err := s.File()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("create state directory: %w", err)
	}
	encoded, err := json.MarshalIndent(stateFile{Mappings: s.mappings}, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(path, append(encoded, '\n'), 0o600); err != nil {
		return fmt.Errorf("write mappings %q: %w", path, err)
	}
	return nil
}

// Mappings returns a copy of the stored mappings in file order.
func (s *Store) Mappings() []Mapping {
	s.load()
	return append([]Mapping(nil), s.mappings...)
}

// Match returns the mapping for a data set name. An exact pattern wins over
// wildcards; among wildcard matches the longest literal prefix wins, with
// lexicographic pattern order as the deterministic tie-breaker.
func (s *Store) Match(name string) (Mapping, bool) {
	s.load()
	name = strings.ToUpper(strings.TrimSpace(name))
	if name == "" {
		return Mapping{}, false
	}
	best := -1
	for i, mapping := range s.mappings {
		if !MatchPattern(name, mapping.Pattern) {
			continue
		}
		if best < 0 || morePrecise(mapping.Pattern, s.mappings[best].Pattern) {
			best = i
		}
	}
	if best < 0 {
		return Mapping{}, false
	}
	return s.mappings[best], true
}

// Put adds or replaces the mapping for its pattern. The created timestamp of a
// replaced mapping is preserved.
func (s *Store) Put(mapping Mapping) error {
	s.load()
	mapping.Pattern = strings.ToUpper(strings.TrimSpace(mapping.Pattern))
	if mapping.Pattern == "" {
		return errors.New("mapping pattern must not be empty")
	}
	now := s.now()
	mapping.Created = now
	mapping.LastUsed = now
	replaced := false
	for i, existing := range s.mappings {
		if existing.Pattern == mapping.Pattern {
			mapping.Created = existing.Created
			s.mappings[i] = mapping
			replaced = true
			break
		}
	}
	if !replaced {
		s.mappings = append(s.mappings, mapping)
		sort.SliceStable(s.mappings, func(i, j int) bool { return s.mappings[i].Pattern < s.mappings[j].Pattern })
	}
	return s.save()
}

// Remove deletes the mapping stored under the exact pattern.
func (s *Store) Remove(pattern string) (bool, error) {
	s.load()
	pattern = strings.ToUpper(strings.TrimSpace(pattern))
	for i, existing := range s.mappings {
		if existing.Pattern == pattern {
			s.mappings = append(s.mappings[:i], s.mappings[i+1:]...)
			return true, s.save()
		}
	}
	return false, nil
}

// Touch records a use of the mapping stored under the exact pattern. Failures
// only affect bookkeeping, so callers may ignore the error.
func (s *Store) Touch(pattern string) error {
	s.load()
	pattern = strings.ToUpper(strings.TrimSpace(pattern))
	for i, existing := range s.mappings {
		if existing.Pattern == pattern {
			s.mappings[i].LastUsed = s.now()
			return s.save()
		}
	}
	return nil
}

func (s *Store) logf(format string, args ...any) {
	if s.Diagnostic != nil {
		s.Diagnostic(format, args...)
	}
}

// morePrecise reports whether pattern a beats pattern b: exact patterns beat
// wildcards, longer literal prefixes beat shorter ones, and lexicographic
// order breaks the remaining ties deterministically.
func morePrecise(a, b string) bool {
	exactA, exactB := !strings.ContainsAny(a, "*%"), !strings.ContainsAny(b, "*%")
	if exactA != exactB {
		return exactA
	}
	prefixA, prefixB := len(literalPrefix(a)), len(literalPrefix(b))
	if prefixA != prefixB {
		return prefixA > prefixB
	}
	return a < b
}

func literalPrefix(pattern string) string {
	if i := strings.IndexAny(pattern, "*%"); i >= 0 {
		return pattern[:i]
	}
	return pattern
}

// MatchPattern reports whether a name matches a DSN pattern using cq's shared
// wildcard dialect: * matches any run of characters, % matches exactly one
// character, and an empty pattern or bare * matches everything. Matching is
// case-insensitive.
func MatchPattern(name, pattern string) bool {
	name = strings.ToUpper(strings.TrimSpace(name))
	pattern = strings.ToUpper(strings.TrimSpace(pattern))
	if pattern == "" || pattern == "*" {
		return true
	}
	if !strings.ContainsAny(pattern, "*%") {
		return name == pattern
	}
	runesName := []rune(name)
	runesPattern := []rune(pattern)
	var match func(i, j int) bool
	match = func(i, j int) bool {
		if j == len(runesPattern) {
			return i == len(runesName)
		}
		switch runesPattern[j] {
		case '*':
			if match(i, j+1) {
				return true
			}
			return i < len(runesName) && match(i+1, j)
		case '%':
			return i < len(runesName) && match(i+1, j+1)
		default:
			return i < len(runesName) && runesName[i] == runesPattern[j] && match(i+1, j+1)
		}
	}
	return match(0, 0)
}
