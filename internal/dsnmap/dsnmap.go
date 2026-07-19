// Package dsnmap persists DSN → copybook mappings so known data sets get
// their display overlay applied automatically across sessions.
package dsnmap

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
)

// StateFileName is the mapping state file kept next to cq/config.json.
const StateFileName = "dsn-copybooks.json"

// Mapping binds a DSN pattern to a copybook source. Pattern wildcards follow
// the existing cq dialect: a trailing or embedded * matches any run of
// characters and % matches exactly one character. A pattern wrapped in
// slashes (/…/) is instead an anchored, case-insensitive Go regular
// expression, e.g. /PROD\.CUSTOMER\.G\d{4}V\d{2}/.
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
	seen := make(map[string]struct{}, len(state.Mappings))
	for _, mapping := range state.Mappings {
		mapping.Pattern = NormalizePattern(mapping.Pattern)
		if mapping.Pattern == "" {
			continue
		}
		if err := ValidatePattern(mapping.Pattern); err != nil {
			s.logf("dsnmap: skipping mapping %q: %v", mapping.Pattern, err)
			continue
		}
		if strings.TrimSpace(mapping.Local) == "" && strings.TrimSpace(mapping.DSN) == "" {
			s.logf("dsnmap: skipping mapping %q: no copybook source", mapping.Pattern)
			continue
		}
		if _, dup := seen[mapping.Pattern]; dup {
			s.logf("dsnmap: skipping duplicate mapping %q", mapping.Pattern)
			continue
		}
		seen[mapping.Pattern] = struct{}{}
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
// wildcards, and wildcards win over regexes; within a tier the longest
// literal prefix wins, with lexicographic pattern order as the deterministic
// tie-breaker.
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

// Matches returns every stored mapping whose pattern matches the data set
// name, ordered most precise first (the first entry is the one Match would
// pick). The slice is a copy.
func (s *Store) Matches(name string) []Mapping {
	s.load()
	name = strings.ToUpper(strings.TrimSpace(name))
	if name == "" {
		return nil
	}
	var matches []Mapping
	for _, mapping := range s.mappings {
		if MatchPattern(name, mapping.Pattern) {
			matches = append(matches, mapping)
		}
	}
	sort.SliceStable(matches, func(i, j int) bool { return morePrecise(matches[i].Pattern, matches[j].Pattern) })
	return matches
}

// Put adds or replaces the mapping for its pattern. The created timestamp of a
// replaced mapping is preserved.
func (s *Store) Put(mapping Mapping) error {
	s.load()
	mapping.Pattern = NormalizePattern(mapping.Pattern)
	if mapping.Pattern == "" {
		return errors.New("mapping pattern must not be empty")
	}
	if err := ValidatePattern(mapping.Pattern); err != nil {
		return err
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
	pattern = NormalizePattern(pattern)
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
	pattern = NormalizePattern(pattern)
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
// wildcards, wildcards beat regexes, longer literal prefixes beat shorter
// ones within a tier, and lexicographic order breaks the remaining ties
// deterministically.
func morePrecise(a, b string) bool {
	tierA, tierB := patternTier(a), patternTier(b)
	if tierA != tierB {
		return tierA < tierB
	}
	prefixA, prefixB := len(literalPrefix(a)), len(literalPrefix(b))
	if prefixA != prefixB {
		return prefixA > prefixB
	}
	return a < b
}

// patternTier ranks pattern kinds for precedence: exact < wildcard < regex.
func patternTier(pattern string) int {
	switch {
	case IsRegexPattern(pattern):
		return 2
	case strings.ContainsAny(pattern, "*%"):
		return 1
	default:
		return 0
	}
}

func literalPrefix(pattern string) string {
	if IsRegexPattern(pattern) {
		return regexLiteralPrefix(pattern[1 : len(pattern)-1])
	}
	if i := strings.IndexAny(pattern, "*%"); i >= 0 {
		return pattern[:i]
	}
	return pattern
}

// regexLiteralPrefix returns the leading run of a regex body with no special
// meaning, used only to rank competing regex patterns by specificity. Escapes
// of punctuation (\., \() count as one literal character; escapes of letters
// or digits (\d, \w) are character classes and end the literal prefix.
func regexLiteralPrefix(body string) string {
	var prefix []byte
	for i := 0; i < len(body); i++ {
		c := body[i]
		switch {
		case c == '\\':
			if i+1 >= len(body) || isAlphanumeric(body[i+1]) {
				return string(prefix)
			}
			i++
			prefix = append(prefix, body[i])
		case strings.IndexByte(`.[]{}()*+?|^$`, c) >= 0:
			return string(prefix)
		default:
			prefix = append(prefix, c)
		}
	}
	return string(prefix)
}

func isAlphanumeric(c byte) bool {
	return c >= '0' && c <= '9' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z'
}

// IsRegexPattern reports whether a pattern selects the regex dialect by being
// wrapped in slashes, e.g. /PROD\.CUST\d+/.
func IsRegexPattern(pattern string) bool {
	return len(pattern) >= 2 && strings.HasPrefix(pattern, "/") && strings.HasSuffix(pattern, "/")
}

// NormalizePattern trims a pattern and upper-cases the wildcard/exact
// dialects. Regex patterns keep their case because upper-casing would corrupt
// escape classes like \d; matching is case-insensitive either way.
func NormalizePattern(pattern string) string {
	pattern = strings.TrimSpace(pattern)
	if IsRegexPattern(pattern) {
		return pattern
	}
	return strings.ToUpper(pattern)
}

// ValidatePattern reports whether a pattern is usable: regex patterns must
// compile, everything else is always valid.
func ValidatePattern(pattern string) error {
	pattern = strings.TrimSpace(pattern)
	if !IsRegexPattern(pattern) {
		return nil
	}
	_, err := compiledRegex(pattern)
	return err
}

var (
	regexCacheMu sync.Mutex
	regexCache   = map[string]*regexp.Regexp{}
)

// compiledRegex compiles a /…/ pattern as an anchored, case-insensitive Go
// regexp, caching compilations for hot paths that re-match every rendered row.
func compiledRegex(pattern string) (*regexp.Regexp, error) {
	regexCacheMu.Lock()
	defer regexCacheMu.Unlock()
	if re, ok := regexCache[pattern]; ok {
		return re, nil
	}
	re, err := regexp.Compile("(?i)^(?:" + pattern[1:len(pattern)-1] + ")$")
	if err != nil {
		return nil, fmt.Errorf("invalid regex pattern %s: %w", pattern, err)
	}
	regexCache[pattern] = re
	return re, nil
}

// MatchPattern reports whether a name matches a DSN pattern using cq's shared
// dialect: * matches any run of characters, % matches exactly one character,
// an empty pattern or bare * matches everything, and a /…/ pattern is an
// anchored, case-insensitive regular expression (invalid regexes match
// nothing). Matching is case-insensitive.
func MatchPattern(name, pattern string) bool {
	pattern = strings.TrimSpace(pattern)
	if IsRegexPattern(pattern) {
		re, err := compiledRegex(pattern)
		return err == nil && re.MatchString(strings.TrimSpace(name))
	}
	name = strings.ToUpper(strings.TrimSpace(name))
	pattern = strings.ToUpper(pattern)
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
