// Package appconfig loads cq's user configuration.
package appconfig

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// InitialConfig is the content written when cq creates a user configuration.
const InitialConfig = "{\n  \"dsnSearchPath\": []\n}\n"

// Config is cq's user configuration model.
type Config struct {
	DSNSearchPath []string `json:"dsnSearchPath"`
}

// Diagnostic receives non-sensitive configuration diagnostics. A nil function
// disables diagnostics.
type Diagnostic func(format string, args ...any)

// Loader locates and loads cq's user configuration. UserConfigDir is injectable
// so callers can isolate tests from the real user configuration directory.
type Loader struct {
	UserConfigDir func() (string, error)
	Diagnostic    Diagnostic
}

// DefaultLoader returns a loader for the current user's configuration directory.
func DefaultLoader(diagnostic Diagnostic) Loader {
	return Loader{UserConfigDir: os.UserConfigDir, Diagnostic: diagnostic}
}

// File returns the unchanged cq/config.json user configuration path.
func (l Loader) File() (string, error) {
	userConfigDir := l.UserConfigDir
	if userConfigDir == nil {
		userConfigDir = os.UserConfigDir
	}
	dir, err := userConfigDir()
	if err != nil {
		return "", fmt.Errorf("locate user config directory: %w", err)
	}
	return filepath.Join(dir, "cq", "config.json"), nil
}

// Load reads and validates the user configuration. A missing configuration or
// unavailable user configuration directory remains optional.
func (l Loader) Load() (Config, error) {
	path, err := l.File()
	if err != nil {
		l.logf("config: no user config directory: %v", err)
		return Config{}, nil
	}
	b, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			l.logf("config %s: not found", path)
			return Config{}, nil
		}
		return Config{}, fmt.Errorf("read config %q: %w", path, err)
	}
	var cfg Config
	if err := json.Unmarshal(b, &cfg); err != nil {
		return Config{}, fmt.Errorf("parse config %q: %w", path, err)
	}
	for i, dsn := range cfg.DSNSearchPath {
		dsn = strings.TrimSpace(dsn)
		if dsn == "" {
			return Config{}, fmt.Errorf("parse config %q: dsnSearchPath[%d] is empty", path, i)
		}
		if strings.ContainsAny(dsn, "()") {
			return Config{}, fmt.Errorf("parse config %q: dsnSearchPath[%d] must name a library without a member: %q", path, i, dsn)
		}
		cfg.DSNSearchPath[i] = dsn
	}
	l.logf("config %s: dsnSearchPath %v", path, cfg.DSNSearchPath)
	return cfg, nil
}

func (l Loader) logf(format string, args ...any) {
	if l.Diagnostic != nil {
		l.Diagnostic(format, args...)
	}
}
