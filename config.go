package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

var userConfigDir = os.UserConfigDir

type config struct {
	DSNSearchPath []string `json:"dsnSearchPath"`
	// Sidecar, when set, is the command cq runs to start a Zowe SDK sidecar
	// for DSN access instead of calling the zowe CLI per data set.
	Sidecar string `json:"sidecar,omitempty"`
}

func loadConfig() (config, error) {
	path, err := defaultConfigFile()
	if err != nil {
		debugLog.Printf("config: no user config directory: %v", err)
		return config{}, nil
	}
	b, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			debugLog.Printf("config %s: not found", path)
			return config{}, nil
		}
		return config{}, fmt.Errorf("read config %q: %w", path, err)
	}
	var cfg config
	if err := json.Unmarshal(b, &cfg); err != nil {
		return config{}, fmt.Errorf("parse config %q: %w", path, err)
	}
	for i, dsn := range cfg.DSNSearchPath {
		dsn = strings.TrimSpace(dsn)
		if dsn == "" {
			return config{}, fmt.Errorf("parse config %q: dsnSearchPath[%d] is empty", path, i)
		}
		if strings.ContainsAny(dsn, "()") {
			return config{}, fmt.Errorf("parse config %q: dsnSearchPath[%d] must name a library without a member: %q", path, i, dsn)
		}
		cfg.DSNSearchPath[i] = dsn
	}
	debugLog.Printf("config %s: dsnSearchPath %v", path, cfg.DSNSearchPath)
	return cfg, nil
}

func defaultConfigFile() (string, error) {
	dir, err := userConfigDir()
	if err != nil {
		return "", fmt.Errorf("locate user config directory: %w", err)
	}
	return filepath.Join(dir, "cq", "config.json"), nil
}
