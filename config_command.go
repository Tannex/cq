package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

const initialConfig = "{\n  \"DSNSearchPath\": []\n}\n"

var launchConfigEditor = func(path string) error {
	name, args, err := configEditorInvocation(runtime.GOOS, os.Getenv, path)
	if err != nil {
		return err
	}
	cmd := exec.Command(name, args...)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("open config %q with %s: %w", path, name, err)
	}
	return nil
}

func editConfig() error {
	path, err := defaultConfigFile()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("create config directory %q: %w", filepath.Dir(path), err)
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err == nil {
		if _, writeErr := f.WriteString(initialConfig); writeErr != nil {
			_ = f.Close()
			return fmt.Errorf("initialize config %q: %w", path, writeErr)
		}
		if err := f.Close(); err != nil {
			return fmt.Errorf("initialize config %q: %w", path, err)
		}
	} else if !os.IsExist(err) {
		return fmt.Errorf("initialize config %q: %w", path, err)
	}
	return launchConfigEditor(path)
}

func configEditorInvocation(goos string, getenv func(string) string, path string) (string, []string, error) {
	for _, key := range []string{"VISUAL", "EDITOR"} {
		if spec := strings.TrimSpace(getenv(key)); spec != "" {
			name, args, err := splitEditorSpec(spec)
			if err != nil {
				return "", nil, fmt.Errorf("parse $%s: %w", key, err)
			}
			return name, append(args, path), nil
		}
	}

	switch goos {
	case "darwin":
		return "open", []string{"-t", path}, nil
	case "windows":
		return "notepad.exe", []string{path}, nil
	case "linux", "freebsd", "openbsd", "netbsd":
		return "xdg-open", []string{path}, nil
	default:
		return "", nil, fmt.Errorf("no default editor for %s; set $VISUAL or $EDITOR", goos)
	}
}

func splitEditorSpec(spec string) (string, []string, error) {
	if spec[0] != '\'' && spec[0] != '"' {
		parts := strings.Fields(spec)
		return parts[0], parts[1:], nil
	}
	quote := spec[0]
	end := strings.IndexByte(spec[1:], quote)
	if end < 0 {
		return "", nil, fmt.Errorf("unterminated quoted executable")
	}
	end++
	name := spec[1:end]
	if name == "" {
		return "", nil, fmt.Errorf("editor executable is empty")
	}
	return name, strings.Fields(spec[end+1:]), nil
}
