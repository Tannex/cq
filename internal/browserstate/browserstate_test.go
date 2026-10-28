package browserstate

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
)

func testStore(t *testing.T) (*Store, string) {
	t.Helper()
	dir := t.TempDir()
	return &Store{UserConfigDir: func() (string, error) { return dir, nil }},
		filepath.Join(dir, "cq", StateFileName)
}

func TestPrefixesPersistPerScopeWithoutChangingPatterns(t *testing.T) {
	store, path := testStore(t)
	if got := store.Prefix("first"); got != "" {
		t.Fatalf("missing state prefix = %q", got)
	}
	patterns := map[string]string{
		"first":  "USER.*",
		"second": `/MixedCase\.[a-z]+/`,
		"long":   "/" + strings.Repeat("VeryLongPattern", 30) + "/",
	}
	for scope, pattern := range patterns {
		store.SetPrefix(scope, pattern)
	}
	reopened := &Store{UserConfigDir: store.UserConfigDir}
	for scope, want := range patterns {
		if got := reopened.Prefix(scope); got != want {
			t.Errorf("prefix for %q = %q, want %q", scope, got, want)
		}
	}
	if got := reopened.Prefix("missing"); got != "" {
		t.Fatalf("unknown scope prefix = %q", got)
	}
	assertNoTemporaryFiles(t, path)
	if runtime.GOOS != "windows" {
		for target, want := range map[string]os.FileMode{path: 0o600, filepath.Dir(path): 0o700} {
			info, err := os.Stat(target)
			if err != nil {
				t.Fatal(err)
			}
			if got := info.Mode().Perm(); got != want {
				t.Errorf("permissions for %s = %o, want %o", target, got, want)
			}
		}
	}
}

func TestSequentialStoresReadCurrentStateBeforeSaving(t *testing.T) {
	first, _ := testStore(t)
	second := &Store{UserConfigDir: first.UserConfigDir}
	first.SetPrefix("first", "FIRST.*")
	_ = second.Prefix("first")
	first.SetPrefix("first", "NEW.*")
	second.SetPrefix("second", "SECOND.*")
	first.SetPrefix("third", "THIRD.*")
	for scope, want := range map[string]string{"first": "NEW.*", "second": "SECOND.*", "third": "THIRD.*"} {
		if got := second.Prefix(scope); got != want {
			t.Errorf("prefix for %q = %q, want %q", scope, got, want)
		}
	}
}

func TestInvalidPrefixesDoNotReplaceLastAppliedPrefix(t *testing.T) {
	store, _ := testStore(t)
	store.SetPrefix("scope", "VALID.*")
	for _, prefix := range []string{"", "  ", "BAD\nPREFIX", "\tVALID.*", "BAD\x00PREFIX", "BAD\u0085PREFIX"} {
		store.SetPrefix("scope", prefix)
		if got := store.Prefix("scope"); got != "VALID.*" {
			t.Fatalf("prefix after invalid value %q = %q", prefix, got)
		}
	}
	store.SetPrefix("scope", "  /MixedCase.*/  ")
	if got := store.Prefix("scope"); got != "/MixedCase.*/" {
		t.Fatalf("trimmed prefix = %q", got)
	}
}

func TestMalformedStateFallsBackAndIsPreserved(t *testing.T) {
	for _, contents := range []string{
		`{corrupt`, `null`, `{}`, `[]`, `{"prefixes":null}`,
		`{"prefixes":{"scope":7}}`, `{"prefixes":{"scope":"  "}}`,
		`{"prefixes":{"scope":"BAD\nPREFIX"}}`,
	} {
		t.Run(contents, func(t *testing.T) {
			store, path := testStore(t)
			store.SetPrefix("scope", "ORIGINAL.*")
			if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
				t.Fatal(err)
			}
			var diagnostics []string
			store.Diagnostic = func(format string, args ...any) {
				diagnostics = append(diagnostics, fmt.Sprintf(format, args...))
			}
			if got := store.Prefix("scope"); got != "" {
				t.Fatalf("malformed state prefix = %q", got)
			}
			store.SetPrefix("scope", "NEW.*")
			b, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if string(b) != contents {
				t.Fatalf("malformed state was overwritten: %s", b)
			}
			if len(diagnostics) == 0 {
				t.Fatal("expected failure diagnostics")
			}
			assertNoTemporaryFiles(t, path)
		})
	}
}

func TestUnavailableConfigAndUnreadableStateFallBack(t *testing.T) {
	store := &Store{UserConfigDir: func() (string, error) { return "", os.ErrPermission }}
	store.SetPrefix("scope", "DATA.*")
	if got := store.Prefix("scope"); got != "" {
		t.Fatalf("unavailable config prefix = %q", got)
	}
	store, path := testStore(t)
	if err := os.MkdirAll(path, 0o700); err != nil {
		t.Fatal(err)
	}
	store.SetPrefix("scope", "DATA.*")
	if got := store.Prefix("scope"); got != "" {
		t.Fatalf("unreadable state prefix = %q", got)
	}
	info, err := os.Stat(path)
	if err != nil || !info.IsDir() {
		t.Fatalf("unreadable state directory was replaced: %v", err)
	}
}

func TestSaveFailureKeepsPreviousState(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("requires Unix directory permissions")
	}
	store, path := testStore(t)
	store.SetPrefix("scope", "ORIGINAL.*")
	dir := filepath.Dir(path)
	if err := os.Chmod(dir, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })
	probe, err := os.CreateTemp(dir, "permission-probe-*")
	if err == nil {
		probe.Close()
		os.Remove(probe.Name())
		t.Skip("directory permissions are not enforced for this user")
	}
	store.SetPrefix("scope", "NEW.*")
	if got := store.Prefix("scope"); got != "ORIGINAL.*" {
		t.Fatalf("prefix after failed save = %q", got)
	}
	assertNoTemporaryFiles(t, path)
}

func TestRenameFailureCleansTemporaryFile(t *testing.T) {
	_, path := testStore(t)
	if err := os.MkdirAll(path, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := save(path, stateFile{Prefixes: map[string]string{"scope": "DATA.*"}}); err == nil {
		t.Fatal("expected rename over directory to fail")
	}
	info, err := os.Stat(path)
	if err != nil || !info.IsDir() {
		t.Fatalf("destination directory was replaced: %v", err)
	}
	assertNoTemporaryFiles(t, path)
}

func TestConcurrentOperationsPreserveScopes(t *testing.T) {
	store, _ := testStore(t)
	var wg sync.WaitGroup
	for i := range 10 {
		wg.Go(func() {
			scope := fmt.Sprintf("scope-%d", i)
			store.SetPrefix(scope, "DATA.*")
			if got := store.Prefix(scope); got != "DATA.*" {
				t.Errorf("prefix for %q = %q", scope, got)
			}
		})
	}
	wg.Wait()
	for i := range 10 {
		if got := store.Prefix(fmt.Sprintf("scope-%d", i)); got != "DATA.*" {
			t.Errorf("saved prefix for scope-%d = %q", i, got)
		}
	}
}

func assertNoTemporaryFiles(t *testing.T, path string) {
	t.Helper()
	entries, err := os.ReadDir(filepath.Dir(path))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != StateFileName {
		t.Fatalf("unexpected files in state directory: %v", entries)
	}
}
