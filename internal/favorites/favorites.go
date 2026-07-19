// Package favorites persists data set favorites — exact names or DSN
// wildcard patterns with optional notes — across sessions.
package favorites

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/Tannex/cq/internal/dsnmap"
)

// StateFileName is the favorites state file kept next to cq/config.json.
const StateFileName = "favorites.json"

// Favorite marks a data set name or DSN pattern. Pattern wildcards follow the
// shared cq dialect implemented by dsnmap.MatchPattern: * matches any run of
// characters and % matches exactly one character.
type Favorite struct {
	Pattern  string    `json:"pattern"`
	Note     string    `json:"note,omitempty"`
	Created  time.Time `json:"created"`
	LastUsed time.Time `json:"lastUsed"`
}

// Wildcard reports whether the favorite is a pattern rather than an exact name.
func (f Favorite) Wildcard() bool {
	return strings.ContainsAny(f.Pattern, "*%")
}

type stateFile struct {
	Favorites []Favorite `json:"favorites"`
}

// Diagnostic receives non-sensitive store diagnostics. A nil function
// disables diagnostics.
type Diagnostic func(format string, args ...any)

// Store loads and saves the favorites state file. UserConfigDir and Now are
// injectable so tests can isolate the real configuration directory and clock.
// A missing state file behaves as an empty store; a corrupt one degrades to an
// empty read-only store so browsing keeps working and the broken file is never
// silently overwritten.
type Store struct {
	UserConfigDir func() (string, error)
	Diagnostic    Diagnostic
	Now           func() time.Time

	loaded    bool
	loadErr   error
	favorites []Favorite
}

// DefaultStore returns a store over the current user's configuration directory.
func DefaultStore(diagnostic Diagnostic) *Store {
	return &Store{UserConfigDir: os.UserConfigDir, Diagnostic: diagnostic}
}

// File returns the cq/favorites.json state path.
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
		s.logf("favorites: no user config directory: %v", err)
		return
	}
	b, err := os.ReadFile(path)
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			s.loadErr = fmt.Errorf("read favorites %q: %w", path, err)
			s.logf("favorites: %v", s.loadErr)
		}
		return
	}
	var state stateFile
	if err := json.Unmarshal(b, &state); err != nil {
		s.loadErr = fmt.Errorf("parse favorites %q: %w", path, err)
		s.logf("favorites: %v", s.loadErr)
		return
	}
	favorites := make([]Favorite, 0, len(state.Favorites))
	for _, favorite := range state.Favorites {
		favorite.Pattern = strings.ToUpper(strings.TrimSpace(favorite.Pattern))
		if favorite.Pattern == "" {
			continue
		}
		favorites = append(favorites, favorite)
	}
	s.favorites = favorites
}

func (s *Store) save() error {
	if s.loadErr != nil {
		return fmt.Errorf("favorites not saved to protect the unreadable state file: %w", s.loadErr)
	}
	path, err := s.File()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("create state directory: %w", err)
	}
	encoded, err := json.MarshalIndent(stateFile{Favorites: s.favorites}, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(path, append(encoded, '\n'), 0o600); err != nil {
		return fmt.Errorf("write favorites %q: %w", path, err)
	}
	return nil
}

// Favorites returns a copy of the stored favorites in most-recently-used
// order, so the popup surfaces what the operator touched last.
func (s *Store) Favorites() []Favorite {
	s.load()
	favorites := append([]Favorite(nil), s.favorites...)
	sort.SliceStable(favorites, func(i, j int) bool {
		if !favorites[i].LastUsed.Equal(favorites[j].LastUsed) {
			return favorites[i].LastUsed.After(favorites[j].LastUsed)
		}
		return favorites[i].Pattern < favorites[j].Pattern
	})
	return favorites
}

// Has reports whether an exact favorite entry is stored under the name.
func (s *Store) Has(name string) bool {
	s.load()
	name = strings.ToUpper(strings.TrimSpace(name))
	for _, favorite := range s.favorites {
		if favorite.Pattern == name {
			return true
		}
	}
	return false
}

// Matches reports whether the name is favorited, either by an exact entry or
// by any wildcard favorite covering it.
func (s *Store) Matches(name string) bool {
	s.load()
	name = strings.ToUpper(strings.TrimSpace(name))
	if name == "" {
		return false
	}
	for _, favorite := range s.favorites {
		if dsnmap.MatchPattern(name, favorite.Pattern) {
			return true
		}
	}
	return false
}

// Toggle adds an exact favorite for the name, or removes the existing exact
// entry. Wildcard favorites covering the name are never mutated. It reports
// whether the name is favorited after the toggle.
func (s *Store) Toggle(name string) (bool, error) {
	s.load()
	name = strings.ToUpper(strings.TrimSpace(name))
	if name == "" {
		return false, errors.New("favorite name must not be empty")
	}
	for i, favorite := range s.favorites {
		if favorite.Pattern == name {
			s.favorites = append(s.favorites[:i], s.favorites[i+1:]...)
			return false, s.save()
		}
	}
	now := s.now()
	s.favorites = append(s.favorites, Favorite{Pattern: name, Created: now, LastUsed: now})
	sort.SliceStable(s.favorites, func(i, j int) bool { return s.favorites[i].Pattern < s.favorites[j].Pattern })
	return true, s.save()
}

// SetNote stores the note on the favorite kept under the exact pattern.
func (s *Store) SetNote(pattern, note string) error {
	s.load()
	pattern = strings.ToUpper(strings.TrimSpace(pattern))
	for i, favorite := range s.favorites {
		if favorite.Pattern == pattern {
			s.favorites[i].Note = strings.TrimSpace(note)
			return s.save()
		}
	}
	return fmt.Errorf("no favorite stored for %s", pattern)
}

// Remove deletes the favorite stored under the exact pattern.
func (s *Store) Remove(pattern string) (bool, error) {
	s.load()
	pattern = strings.ToUpper(strings.TrimSpace(pattern))
	for i, favorite := range s.favorites {
		if favorite.Pattern == pattern {
			s.favorites = append(s.favorites[:i], s.favorites[i+1:]...)
			return true, s.save()
		}
	}
	return false, nil
}

// Touch records a use of the favorite stored under the exact pattern. Failures
// only affect bookkeeping, so callers may ignore the error.
func (s *Store) Touch(pattern string) error {
	s.load()
	pattern = strings.ToUpper(strings.TrimSpace(pattern))
	for i, favorite := range s.favorites {
		if favorite.Pattern == pattern {
			s.favorites[i].LastUsed = s.now()
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
