package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
)

const defaultConfigPath = "cq.json"

type config struct {
	DSNSearchPath []string `json:"DSNSearchPath"`
}

func loadConfig(path string) (config, error) {
	explicit := path != ""
	if path == "" {
		path = defaultConfigPath
	}
	b, err := os.ReadFile(path)
	if err != nil {
		if !explicit && errors.Is(err, os.ErrNotExist) {
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
			return config{}, fmt.Errorf("parse config %q: DSNSearchPath[%d] is empty", path, i)
		}
		if strings.ContainsAny(dsn, "()") {
			return config{}, fmt.Errorf("parse config %q: DSNSearchPath[%d] must name a library without a member: %q", path, i, dsn)
		}
		cfg.DSNSearchPath[i] = dsn
	}
	return cfg, nil
}
