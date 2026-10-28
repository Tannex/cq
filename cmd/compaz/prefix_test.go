package main

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strconv"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/Tannex/cq/internal/browserstate"
	"github.com/Tannex/cq/internal/compaz"
	"github.com/Tannex/cq/internal/zowe"
)

func TestPrefixScopeSeparatesConnectionsWithoutCredentials(t *testing.T) {
	base := zowe.Session{Profile: "dev", Protocol: "https", Host: "example.com", Port: 443, BasePath: "/gateway", User: "USER", Password: "secret", TokenValue: "token"}
	scope := prefixScope(base)
	for _, secret := range []string{"secret", "token"} {
		if strings.Contains(scope, secret) {
			t.Fatal("scope contains credential")
		}
	}
	for _, change := range []func(*zowe.Session){
		func(s *zowe.Session) { s.Profile = "prod" },
		func(s *zowe.Session) { s.Protocol = "http" },
		func(s *zowe.Session) { s.Host = "other.example.com" },
		func(s *zowe.Session) { s.Port = 8443 },
		func(s *zowe.Session) { s.BasePath = "/other" },
		func(s *zowe.Session) { s.User = "OTHER" },
	} {
		other := base
		change(&other)
		if prefixScope(other) == scope {
			t.Fatal("distinct connection shares prefix scope")
		}
	}
	base.Password = "rotated"
	base.TokenValue = "rotated"
	base.Encoding = "cp277"
	if prefixScope(base) != scope {
		t.Fatal("credential or encoding change lost prefix scope")
	}
}

// isolatePrefixState keeps command tests independent of os.UserConfigDir's
// platform-specific environment handling, especially macOS's HOME fallback.
func isolatePrefixState(t *testing.T) *browserstate.Store {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	t.Setenv("APPDATA", dir)
	store := &browserstate.Store{UserConfigDir: func() (string, error) { return dir, nil }}
	original := newPrefixStore
	t.Cleanup(func() { newPrefixStore = original })
	newPrefixStore = func(browserstate.Diagnostic) *browserstate.Store { return store }
	return store
}

func TestRunRemembersAppliedPrefixAcrossRestarts(t *testing.T) {
	store := isolatePrefixState(t)
	requests := make(chan string, 10)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests <- r.URL.Query().Get("dslevel")
		_, _ = io.WriteString(w, `{"items":[],"returnedRows":0,"moreRows":false}`)
	}))
	defer server.Close()
	u, err := url.Parse(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	port, err := strconv.Atoi(u.Port())
	if err != nil {
		t.Fatal(err)
	}
	session := zowe.Session{Profile: "dev", Protocol: u.Scheme, Host: u.Hostname(), Port: port, User: "USER", Encoding: "latin1"}
	oldRunner, oldList, oldLoad := runProgram, listZoweProfiles, loadNamedSession
	t.Cleanup(func() { runProgram = oldRunner; listZoweProfiles = oldList; loadNamedSession = oldLoad })
	listZoweProfiles = func() ([]string, error) { return []string{"dev"}, nil }
	loadNamedSession = func(string) (zowe.Session, error) { return session, nil }
	for runIndex := 0; runIndex < 2; runIndex++ {
		runProgram = func(m *compaz.Model) error {
			_, cmd := m.Update(tea.WindowSizeMsg{Width: 90, Height: 24})
			drain(t, m, cmd)
			drain(t, m, m.Init())
			want := "USER.*"
			if runIndex == 1 {
				want = "PROJECT.*"
			}
			select {
			case got := <-requests:
				if got != want {
					t.Fatalf("initial prefix=%q, want %q", got, want)
				}
			default:
				t.Fatal("startup did not list datasets")
			}
			if runIndex == 0 {
				for _, msg := range []tea.Msg{
					tea.KeyPressMsg(tea.Key{Code: '/', Text: "/"}),
					tea.KeyPressMsg(tea.Key{Code: 'u', Mod: tea.ModCtrl}),
					tea.PasteMsg{Content: "project.*"},
					tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter}),
				} {
					_, cmd := m.Update(msg)
					drain(t, m, cmd)
				}
				select {
				case got := <-requests:
					if got != "PROJECT.*" {
						t.Fatalf("applied prefix=%q", got)
					}
				default:
					t.Fatal("apply did not list datasets")
				}
			}
			return nil
		}
		var stdout, stderr bytes.Buffer
		if err := run(nil, &stdout, &stderr); err != nil {
			t.Fatal(err)
		}
	}
	if got := store.Prefix(prefixScope(session)); got != "PROJECT.*" {
		t.Fatalf("saved prefix=%q", got)
	}
}

func TestDemoDoesNotSavePrefixes(t *testing.T) {
	store := isolatePrefixState(t)
	oldRunner := runProgram
	t.Cleanup(func() { runProgram = oldRunner })
	runProgram = func(m *compaz.Model) error {
		// No terminal geometry: exercise apply without dispatching any row request.
		drain(t, m, m.Init())
		for _, msg := range []tea.Msg{
			tea.KeyPressMsg(tea.Key{Code: '/', Text: "/"}),
			tea.KeyPressMsg(tea.Key{Code: 'u', Mod: tea.ModCtrl}),
			tea.PasteMsg{Content: "CUSTOM.*"},
			tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter}),
		} {
			_, cmd := m.Update(msg)
			drain(t, m, cmd)
		}
		return nil
	}
	var stdout, stderr bytes.Buffer
	if err := run([]string{"--demo"}, &stdout, &stderr); err != nil {
		t.Fatal(err)
	}
	path, err := store.File()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("demo created state: %v", err)
	}
}

func TestLoadedSessionCarriesResolvedPrefixScope(t *testing.T) {
	loaded := zowe.Session{Profile: "resolved", Protocol: "https", Host: "host", Port: 443, User: "USER"}
	original := loadDefaultSession
	t.Cleanup(func() { loadDefaultSession = original })
	loadDefaultSession = func() (zowe.Session, error) { return loaded, nil }
	session, err := loadSession(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	if session.PrefixScope != prefixScope(loaded) {
		t.Fatal("resolved connection scope missing from session")
	}
}
