package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

const initialConfig = "{\n  \"dsnSearchPath\": []\n}\n"

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
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create config directory %q: %w", dir, err)
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
			name, args, err := splitCommandSpec(spec)
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

// splitCommandSpec splits an editor command into an executable and its
// arguments. Single- or double-quoted segments keep their spaces, so paths
// containing spaces survive.
func splitCommandSpec(spec string) (string, []string, error) {
	var tokens []string
	var cur strings.Builder
	inToken := false
	for i := 0; i < len(spec); i++ {
		switch c := spec[i]; {
		case c == '\'' || c == '"':
			end := strings.IndexByte(spec[i+1:], c)
			if end < 0 {
				return "", nil, fmt.Errorf("unterminated %c-quoted string", c)
			}
			cur.WriteString(spec[i+1 : i+1+end])
			inToken = true
			i += end + 1
		case c == ' ' || c == '\t':
			if inToken {
				tokens = append(tokens, cur.String())
				cur.Reset()
				inToken = false
			}
		default:
			cur.WriteByte(c)
			inToken = true
		}
	}
	if inToken {
		tokens = append(tokens, cur.String())
	}
	if len(tokens) == 0 || tokens[0] == "" {
		return "", nil, fmt.Errorf("empty executable")
	}
	return tokens[0], tokens[1:], nil
}
