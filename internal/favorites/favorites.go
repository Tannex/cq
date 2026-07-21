// Package favorites persists data set favorites — exact names or DSN
// wildcard patterns with optional notes — and job owner/prefix filter
// bookmarks, across sessions.
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

// Kind distinguishes what a favorite's Pattern means, since a store holds
// two unrelated families of entry that must never be listed or matched
// against each other.
type Kind string

const (
	// KindDataSet marks a data set name or DSN wildcard/regex pattern. It is
	// the zero value so favorites saved before Kind existed, and files
	// written by versions that never set it, stay valid.
	KindDataSet Kind = ""
	// KindJob marks a job owner/prefix filter bookmark. Pattern is the
	// opaque "OWNER|PREFIX" identity ws.jobIdentity() also uses — never
	// matched against listed rows, only looked up or jumped to directly.
	KindJob Kind = "job"
)

// Favorite marks a data set name/pattern or a job filter bookmark, depending
// on Kind. For KindDataSet, Pattern wildcards follow the shared cq dialect
// implemented by dsnmap.MatchPattern: * matches any run of characters, %
// matches exactly one character, and a /…/ pattern is an anchored,
// case-insensitive regular expression. For KindJob, Pattern is an opaque
// "OWNER|PREFIX" identity with no wildcard semantics of its own.
//
// Profile scopes the favorite to one z/OSMF profile. An empty profile is
// shared across profiles: it is what single-profile sessions write, and what
// favorites saved before profile keying existed carry.
type Favorite struct {
	Pattern  string    `json:"pattern"`
	Kind     Kind      `json:"kind,omitempty"`
	Profile  string    `json:"profile,omitempty"`
	Note     string    `json:"note,omitempty"`
	Created  time.Time `json:"created"`
	LastUsed time.Time `json:"lastUsed"`
}

// visibleTo reports whether the favorite belongs to the profile's view:
// entries keyed to the profile plus the shared (empty-profile) entries.
func (f Favorite) visibleTo(profile string) bool {
	return f.Profile == "" || f.Profile == profile
}

// Wildcard reports whether the favorite is a pattern rather than an exact name.
func (f Favorite) Wildcard() bool {
	return strings.ContainsAny(f.Pattern, "*%") || dsnmap.IsRegexPattern(f.Pattern)
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
		favorite.Pattern = dsnmap.NormalizePattern(favorite.Pattern)
		if favorite.Pattern == "" {
			continue
		}
		if err := dsnmap.ValidatePattern(favorite.Pattern); err != nil {
			s.logf("favorites: skipping favorite %q: %v", favorite.Pattern, err)
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

// Favorites returns a copy of the profile's favorites of the given kind in
// most-recently-used order, so the popup surfaces what the operator touched
// last.
func (s *Store) Favorites(profile string, kind Kind) []Favorite {
	s.load()
	favorites := make([]Favorite, 0, len(s.favorites))
	for _, favorite := range s.favorites {
		if favorite.visibleTo(profile) && favorite.Kind == kind {
			favorites = append(favorites, favorite)
		}
	}
	sort.SliceStable(favorites, func(i, j int) bool {
		if !favorites[i].LastUsed.Equal(favorites[j].LastUsed) {
			return favorites[i].LastUsed.After(favorites[j].LastUsed)
		}
		return favorites[i].Pattern < favorites[j].Pattern
	})
	return favorites
}

// Has reports whether an exact favorite entry of the given kind is stored
// under the name in the profile's view.
func (s *Store) Has(profile string, kind Kind, name string) bool {
	s.load()
	name = strings.ToUpper(strings.TrimSpace(name))
	for _, favorite := range s.favorites {
		if favorite.visibleTo(profile) && favorite.Kind == kind && favorite.Pattern == name {
			return true
		}
	}
	return false
}

// Matches reports whether the name is favorited (of the given kind) in the
// profile's view, either by an exact entry or by any wildcard favorite
// covering it.
func (s *Store) Matches(profile string, kind Kind, name string) bool {
	s.load()
	name = strings.ToUpper(strings.TrimSpace(name))
	if name == "" {
		return false
	}
	for _, favorite := range s.favorites {
		if favorite.visibleTo(profile) && favorite.Kind == kind && dsnmap.MatchPattern(name, favorite.Pattern) {
			return true
		}
	}
	return false
}

// Toggle adds an exact favorite of the given kind for the name under the
// profile, or removes the existing exact entry visible to it (a shared entry
// is removed for every profile). Wildcard favorites covering the name are
// never mutated. It reports whether the name is favorited after the toggle.
func (s *Store) Toggle(profile string, kind Kind, name string) (bool, error) {
	s.load()
	name = strings.ToUpper(strings.TrimSpace(name))
	if name == "" {
		return false, errors.New("favorite name must not be empty")
	}
	for i, favorite := range s.favorites {
		if favorite.visibleTo(profile) && favorite.Kind == kind && favorite.Pattern == name {
			s.favorites = append(s.favorites[:i], s.favorites[i+1:]...)
			return false, s.save()
		}
	}
	now := s.now()
	s.favorites = append(s.favorites, Favorite{Pattern: name, Kind: kind, Profile: profile, Created: now, LastUsed: now})
	sortByPattern(s.favorites)
	return true, s.save()
}

// sortByPattern keeps the persisted order deterministic; display order is
// MRU via Favorites().
func sortByPattern(favorites []Favorite) {
	sort.SliceStable(favorites, func(i, j int) bool { return favorites[i].Pattern < favorites[j].Pattern })
}

func (s *Store) has(profile string, kind Kind, pattern string) bool {
	for _, favorite := range s.favorites {
		if favorite.visibleTo(profile) && favorite.Kind == kind && favorite.Pattern == pattern {
			return true
		}
	}
	return false
}

// Add stores a new favorite of the given kind under the normalized pattern —
// exact name, wildcard, or /…/ regex for KindDataSet; an opaque identity for
// KindJob — keyed to the profile.
func (s *Store) Add(profile string, kind Kind, pattern string) error {
	s.load()
	pattern = dsnmap.NormalizePattern(pattern)
	if pattern == "" {
		return errors.New("favorite pattern must not be empty")
	}
	if err := dsnmap.ValidatePattern(pattern); err != nil {
		return err
	}
	if s.has(profile, kind, pattern) {
		return fmt.Errorf("favorite %s already exists", pattern)
	}
	now := s.now()
	s.favorites = append(s.favorites, Favorite{Pattern: pattern, Kind: kind, Profile: profile, Created: now, LastUsed: now})
	sortByPattern(s.favorites)
	return s.save()
}

// Rename moves the favorite of the given kind visible to the profile under
// the old pattern to a new one, preserving its note, timestamps, and profile
// keying.
func (s *Store) Rename(profile string, kind Kind, oldPattern, newPattern string) error {
	s.load()
	oldPattern = dsnmap.NormalizePattern(oldPattern)
	newPattern = dsnmap.NormalizePattern(newPattern)
	if newPattern == "" {
		return errors.New("favorite pattern must not be empty")
	}
	if err := dsnmap.ValidatePattern(newPattern); err != nil {
		return err
	}
	if newPattern != oldPattern && s.has(profile, kind, newPattern) {
		return fmt.Errorf("favorite %s already exists", newPattern)
	}
	for i, favorite := range s.favorites {
		if favorite.visibleTo(profile) && favorite.Kind == kind && favorite.Pattern == oldPattern {
			s.favorites[i].Pattern = newPattern
			sortByPattern(s.favorites)
			return s.save()
		}
	}
	return fmt.Errorf("no favorite stored for %s", oldPattern)
}

// SetNote stores the note on the profile's favorite of the given kind kept
// under the pattern.
func (s *Store) SetNote(profile string, kind Kind, pattern, note string) error {
	s.load()
	pattern = dsnmap.NormalizePattern(pattern)
	for i, favorite := range s.favorites {
		if favorite.visibleTo(profile) && favorite.Kind == kind && favorite.Pattern == pattern {
			s.favorites[i].Note = strings.TrimSpace(note)
			return s.save()
		}
	}
	return fmt.Errorf("no favorite stored for %s", pattern)
}

// Remove deletes the profile's favorite of the given kind stored under the
// pattern (a shared entry is removed for every profile).
func (s *Store) Remove(profile string, kind Kind, pattern string) (bool, error) {
	s.load()
	pattern = dsnmap.NormalizePattern(pattern)
	for i, favorite := range s.favorites {
		if favorite.visibleTo(profile) && favorite.Kind == kind && favorite.Pattern == pattern {
			s.favorites = append(s.favorites[:i], s.favorites[i+1:]...)
			return true, s.save()
		}
	}
	return false, nil
}

// Touch records a use of the profile's favorite of the given kind stored
// under the pattern. Failures only affect bookkeeping, so callers may ignore
// the error.
func (s *Store) Touch(profile string, kind Kind, pattern string) error {
	s.load()
	pattern = dsnmap.NormalizePattern(pattern)
	for i, favorite := range s.favorites {
		if favorite.visibleTo(profile) && favorite.Kind == kind && favorite.Pattern == pattern {
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
