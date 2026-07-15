package main

import (
	"archive/tar"
	"compress/gzip"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
)

const (
	sidecarArchive = "resources/cq-zowe-sidecar.tgz"
	sidecarSubdir  = "sidecar"
	sidecarScript  = "zowe-sidecar.js"
	minNodeMajor   = 20
	minNodeMinor   = 9
)

// The sidecar npm package is part of the cq binary. cq init extracts it and
// npm ci installs the exact dependency versions recorded in package-lock.json.
//
//go:generate go run ./internal/cmd/package-sidecar -output resources/cq-zowe-sidecar.tgz
//go:embed resources/cq-zowe-sidecar.tgz
var sidecarResources embed.FS

var (
	findExecutable = exec.LookPath
	nodeVersion    = func(node string) ([]byte, error) {
		return exec.Command(node, "--version").Output()
	}
	installSidecarDependencies = func(npm, dir string) error {
		cmd := exec.Command(npm, "ci")
		cmd.Dir = dir
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		return cmd.Run()
	}
)

func initSidecar(out io.Writer) error {
	node, err := findExecutable("node")
	if err != nil {
		return errors.New("Node.js is required for the Zowe sidecar; install Node.js 20.9.0 or newer")
	}
	versionOutput, err := nodeVersion(node)
	if err != nil {
		return fmt.Errorf("check Node.js version using %q: %w", node, err)
	}
	version, err := parseNodeVersion(string(versionOutput))
	if err != nil {
		return err
	}
	if version[0] < minNodeMajor || version[0] == minNodeMajor && version[1] < minNodeMinor {
		return fmt.Errorf("Node.js %d.%d.%d is too old; the Zowe sidecar requires 20.9.0 or newer", version[0], version[1], version[2])
	}
	npm, err := findExecutable("npm")
	if err != nil {
		return errors.New("npm is required to install the Zowe sidecar dependencies")
	}

	configPath, err := defaultConfigFile()
	if err != nil {
		return err
	}
	configDir := filepath.Dir(configPath)
	installDir := filepath.Join(configDir, sidecarSubdir)
	command := "node " + quoteCommandArg(filepath.Join(installDir, sidecarScript))
	updatedConfig, err := prepareSidecarConfig(configPath, command)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(configDir, 0o700); err != nil {
		return fmt.Errorf("create config directory %q: %w", configDir, err)
	}
	if err := extractSidecar(installDir); err != nil {
		return err
	}
	if err := installSidecarDependencies(npm, installDir); err != nil {
		return fmt.Errorf("install sidecar dependencies with npm ci: %w", err)
	}

	if err := writeSidecarConfig(configPath, updatedConfig); err != nil {
		return err
	}
	_, err = fmt.Fprintf(out, "Installed Zowe sidecar in %s\nUpdated %s\n", installDir, configPath)
	return err
}

func parseNodeVersion(output string) ([3]int, error) {
	var version [3]int
	s := strings.TrimSpace(output)
	s = strings.TrimPrefix(s, "v")
	parts := strings.SplitN(s, ".", 3)
	if len(parts) != 3 {
		return version, fmt.Errorf("cannot parse Node.js version %q", strings.TrimSpace(output))
	}
	patch := strings.FieldsFunc(parts[2], func(r rune) bool { return r < '0' || r > '9' })
	if len(patch) == 0 {
		return version, fmt.Errorf("cannot parse Node.js version %q", strings.TrimSpace(output))
	}
	parts[2] = patch[0]
	for i, part := range parts {
		n, err := strconv.Atoi(part)
		if err != nil {
			return version, fmt.Errorf("cannot parse Node.js version %q", strings.TrimSpace(output))
		}
		version[i] = n
	}
	return version, nil
}

func extractSidecar(dest string) error {
	f, err := sidecarResources.Open(sidecarArchive)
	if err != nil {
		return fmt.Errorf("open bundled sidecar: %w", err)
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		return fmt.Errorf("read bundled sidecar: %w", err)
	}
	defer gz.Close()

	staging, err := os.MkdirTemp(filepath.Dir(dest), ".sidecar-")
	if err != nil {
		return fmt.Errorf("prepare sidecar installation: %w", err)
	}
	defer os.RemoveAll(staging)

	tr := tar.NewReader(gz)
	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return fmt.Errorf("read bundled sidecar: %w", err)
		}
		archiveName := filepath.ToSlash(h.Name)
		if !strings.HasPrefix(archiveName, "package/") {
			return fmt.Errorf("bundled sidecar contains unexpected path %q", h.Name)
		}
		name := filepath.Clean(filepath.FromSlash(strings.TrimPrefix(archiveName, "package/")))
		if name == "." {
			continue
		}
		if filepath.IsAbs(name) || name == ".." || strings.HasPrefix(name, ".."+string(filepath.Separator)) {
			return fmt.Errorf("bundled sidecar contains unsafe path %q", h.Name)
		}
		target := filepath.Join(staging, name)
		switch h.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, 0o755); err != nil {
				return fmt.Errorf("extract bundled sidecar: %w", err)
			}
		case tar.TypeReg, tar.TypeRegA:
			mode := os.FileMode(h.Mode) & 0o777
			file, err := os.OpenFile(target, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, mode)
			if err != nil {
				return fmt.Errorf("extract bundled sidecar: %w", err)
			}
			_, copyErr := io.Copy(file, tr)
			closeErr := file.Close()
			if err := errors.Join(copyErr, closeErr); err != nil {
				return fmt.Errorf("extract bundled sidecar: %w", err)
			}
		default:
			return fmt.Errorf("bundled sidecar contains unsupported entry %q", h.Name)
		}
	}
	if _, err := os.Stat(filepath.Join(staging, sidecarScript)); err != nil {
		return fmt.Errorf("bundled sidecar is missing %s: %w", sidecarScript, err)
	}
	if err := os.RemoveAll(dest); err != nil {
		return fmt.Errorf("replace sidecar installation %q: %w", dest, err)
	}
	if err := os.Rename(staging, dest); err != nil {
		return fmt.Errorf("install sidecar in %q: %w", dest, err)
	}
	return nil
}

func prepareSidecarConfig(path, command string) ([]byte, error) {
	contents, err := os.ReadFile(path)
	var values map[string]json.RawMessage
	if errors.Is(err, os.ErrNotExist) {
		values = make(map[string]json.RawMessage)
	} else if err != nil {
		return nil, fmt.Errorf("read config %q: %w", path, err)
	} else if err := json.Unmarshal(contents, &values); err != nil {
		return nil, fmt.Errorf("parse config %q: %w", path, err)
	}
	if values == nil {
		return nil, fmt.Errorf("parse config %q: expected a JSON object", path)
	}
	encodedCommand, _ := json.Marshal(command)
	values["sidecar"] = encodedCommand
	if _, ok := values["dsnSearchPath"]; !ok {
		values["dsnSearchPath"] = json.RawMessage("[]")
	}
	updated, err := json.MarshalIndent(values, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("encode config %q: %w", path, err)
	}
	updated = append(updated, '\n')
	return updated, nil
}

func writeSidecarConfig(path string, updated []byte) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), ".config-*.json")
	if err != nil {
		return fmt.Errorf("update config %q: %w", path, err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("update config %q: %w", path, err)
	}
	if _, err := tmp.Write(updated); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("update config %q: %w", path, err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("update config %q: %w", path, err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("update config %q: %w", path, err)
	}
	return nil
}

func quoteCommandArg(arg string) string {
	if !strings.ContainsAny(arg, " \t\"") {
		return arg
	}
	return `"` + strings.ReplaceAll(arg, `"`, `\"`) + `"`
}
