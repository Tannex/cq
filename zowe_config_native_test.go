package main

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
)

type fakeZoweKeyring struct {
	values map[string]string
	errs   map[string]error
	calls  []string
}

func (k *fakeZoweKeyring) Get(service, account string) (string, error) {
	key := service + "\x00" + account
	k.calls = append(k.calls, key)
	if err := k.errs[key]; err != nil {
		return "", err
	}
	if value, ok := k.values[key]; ok {
		return value, nil
	}
	return "", errZoweSecretNotFound
}

func zoweTestOptions(home, work string, keyring zoweKeyring, env map[string]string) zoweLoadOptions {
	return zoweLoadOptions{
		HomeDir: home, WorkingDir: work, GOOS: "linux", Keyring: keyring,
		Getenv: func(name string) string { return env[name] },
	}
}

func writeZoweTestFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestLoadZoweSessionResolvesNestedProfileAndSecureBase(t *testing.T) {
	home := t.TempDir()
	work := t.TempDir()
	configPath := filepath.Join(home, ".zowe", "zowe.config.json")
	writeZoweTestFile(t, configPath, `{
  // Zowe accepts JSON with comments and trailing commas.
  "profiles": {
    "global_base": {
      "type": "base",
      "properties": {"host": "mainframe.example", "port": 1443},
      "secure": ["user", "password"],
    },
    "lpar1": {
      "properties": {"basePath": "/api/v1/", "rejectUnauthorized": false},
      "profiles": {
        "zosmf": {"type": "zosmf", "properties": {}},
      },
    },
  },
  "defaults": {"base": "global_base", "zosmf": "lpar1.zosmf"},
}`)
	vault, err := json.Marshal(map[string]map[string]any{
		configPath: {
			"profiles.global_base.properties.user":     "IBMUSER",
			"profiles.global_base.properties.password": "secret",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	keyring := &fakeZoweKeyring{values: map[string]string{
		"Zowe\x00" + zoweSecureAccount: string(vault),
	}}

	session, err := loadZoweSession(zoweTestOptions(home, work, keyring, nil))
	if err != nil {
		t.Fatalf("loadZoweSession() error = %v", err)
	}
	want := zoweSession{
		Profile: "lpar1.zosmf", Protocol: "https", Host: "mainframe.example",
		Port: 1443, BasePath: "/api/v1", User: "IBMUSER", Password: "secret",
		RejectUnauthorized: false,
	}
	if !reflect.DeepEqual(session, want) {
		t.Fatalf("loadZoweSession() = %#v, want %#v", session, want)
	}
	if !reflect.DeepEqual(keyring.calls, []string{"Zowe\x00" + zoweSecureAccount}) {
		t.Fatalf("keyring calls = %q", keyring.calls)
	}
}

func TestLoadZoweSessionMergesProjectUserConfigAndEnvironment(t *testing.T) {
	home := t.TempDir()
	project := filepath.Join(t.TempDir(), "project")
	work := filepath.Join(project, "src", "cmd")
	if err := os.MkdirAll(work, 0o700); err != nil {
		t.Fatal(err)
	}
	writeZoweTestFile(t, filepath.Join(home, ".zowe", "zowe.config.json"), `{
  "profiles": {
    "global_base": {"type":"base", "properties":{"host":"global.example", "user":"global", "password":"global-secret"}},
    "global_zosmf": {"type":"zosmf", "properties":{}}
  },
  "defaults":{"base":"global_base", "zosmf":"global_zosmf"}
}`)
	writeZoweTestFile(t, filepath.Join(project, "zowe.config.json"), `{
  "profiles": {
    "project_base": {"type":"base", "properties":{"host":"project.example", "port":443, "user":"project", "password":"project-secret"}},
    "lpar": {"properties":{"basePath":"gateway"}, "profiles":{"zosmf":{"type":"zosmf", "properties":{}}}}
  },
  "defaults":{"base":"project_base", "zosmf":"lpar.zosmf"}
}`)
	writeZoweTestFile(t, filepath.Join(project, "zowe.config.user.json"), `{
  "profiles":{"lpar":{"profiles":{"zosmf":{"properties":{"tokenType":"LtpaToken2"}}}}}
}`)
	env := map[string]string{
		"ZOWE_OPT_HOST":                "override.example",
		"ZOWE_OPT_PORT":                "7554",
		"ZOWE_OPT_TOKEN_VALUE":         "token-from-env",
		"ZOWE_OPT_REJECT_UNAUTHORIZED": "false",
	}

	session, err := loadZoweSession(zoweTestOptions(home, work, &fakeZoweKeyring{}, env))
	if err != nil {
		t.Fatalf("loadZoweSession() error = %v", err)
	}
	if session.Profile != "lpar.zosmf" || session.Host != "override.example" || session.Port != 7554 {
		t.Fatalf("session selection/overrides = %#v", session)
	}
	if session.BasePath != "/gateway" || session.TokenType != "LtpaToken2" || session.TokenValue != "token-from-env" {
		t.Fatalf("session merged properties = %#v", session)
	}
	if session.RejectUnauthorized {
		t.Fatalf("RejectUnauthorized = true, want environment override false")
	}
}

func TestLoadZoweSessionUsesGlobalBaseForGlobalProfile(t *testing.T) {
	home := t.TempDir()
	project := t.TempDir()
	writeZoweTestFile(t, filepath.Join(home, ".zowe", "zowe.config.json"), `{
  "profiles": {
    "global_base":{"type":"base", "properties":{"host":"global.example", "user":"global", "password":"secret"}},
    "only_global":{"type":"zosmf", "properties":{}}
  },
  "defaults":{"base":"global_base", "zosmf":"only_global"}
}`)
	writeZoweTestFile(t, filepath.Join(project, "zowe.config.json"), `{
  "profiles":{"project_base":{"type":"base", "properties":{"host":"wrong.example", "user":"project", "password":"wrong"}}},
  "defaults":{"base":"project_base"}
}`)

	session, err := loadZoweSession(zoweTestOptions(home, project, &fakeZoweKeyring{}, nil))
	if err != nil {
		t.Fatalf("loadZoweSession() error = %v", err)
	}
	if session.Profile != "only_global" || session.Host != "global.example" || session.User != "global" {
		t.Fatalf("global session = %#v, want global base properties", session)
	}
}

func TestLoadZoweSessionSelectsNamedProfile(t *testing.T) {
	home := t.TempDir()
	work := t.TempDir()
	writeZoweTestFile(t, filepath.Join(home, ".zowe", "zowe.config.json"), `{
  "profiles": {
    "base":{"type":"base", "properties":{"user":"u", "password":"p"}},
    "first":{"type":"zosmf", "properties":{"host":"first.example"}},
    "second":{"type":"zosmf", "properties":{"host":"second.example"}}
  },
  "defaults":{"base":"base", "zosmf":"first"}
}`)

	session, err := loadZoweSession(zoweTestOptions(home, work, &fakeZoweKeyring{}, map[string]string{
		"ZOWE_OPT_ZOSMF_PROFILE": "second",
	}))
	if err != nil {
		t.Fatalf("loadZoweSession() error = %v", err)
	}
	if session.Profile != "second" || session.Host != "second.example" {
		t.Fatalf("session = %#v, want named second profile", session)
	}
}

func TestApplyZoweEnvironmentKeepsStringPropertiesAsStrings(t *testing.T) {
	properties := map[string]any{}
	values := map[string]string{
		"ZOWE_OPT_USER":     "12345",
		"ZOWE_OPT_PASSWORD": "000007",
		"ZOWE_OPT_PORT":     "1443",
	}
	applyZoweEnvironment(properties, func(name string) string { return values[name] })
	if properties["user"] != "12345" || properties["password"] != "000007" {
		t.Fatalf("string properties = %#v", properties)
	}
	if properties["port"] != float64(1443) {
		t.Fatalf("port = %#v, want numeric 1443", properties["port"])
	}
}

func TestLoadZoweSessionReportsMissingSecureStore(t *testing.T) {
	home := t.TempDir()
	work := t.TempDir()
	writeZoweTestFile(t, filepath.Join(home, ".zowe", "zowe.config.json"), `{
  "profiles":{"base":{"type":"base", "properties":{"host":"host"}, "secure":["user","password"]}, "zosmf":{"type":"zosmf"}},
  "defaults":{"base":"base", "zosmf":"zosmf"}
}`)

	_, err := loadZoweSession(zoweTestOptions(home, work, &fakeZoweKeyring{}, nil))
	if err == nil || !strings.Contains(err.Error(), "secure credential store") || !strings.Contains(err.Error(), zoweSecureAccount) {
		t.Fatalf("loadZoweSession() error = %v, want secure-store guidance", err)
	}
}

func TestLoadZoweSessionReportsMissingConfig(t *testing.T) {
	_, err := loadZoweSession(zoweTestOptions(t.TempDir(), t.TempDir(), &fakeZoweKeyring{}, nil))
	if err == nil || !strings.Contains(err.Error(), "no Zowe team configuration") {
		t.Fatalf("loadZoweSession() error = %v, want missing-config guidance", err)
	}
}

func TestLoadZoweSessionHonorsZoweCLIHome(t *testing.T) {
	home := t.TempDir()
	cliHome := filepath.Join(t.TempDir(), "custom-zowe-home")
	work := t.TempDir()
	writeZoweTestFile(t, filepath.Join(cliHome, "zowe.config.json"), `{
  "profiles": {
    "base":{"type":"base", "properties":{"host":"custom.example", "user":"u", "password":"p"}},
    "zosmf":{"type":"zosmf"}
  },
  "defaults":{"base":"base", "zosmf":"zosmf"}
}`)
	opts := zoweTestOptions(home, work, &fakeZoweKeyring{}, map[string]string{"ZOWE_CLI_HOME": cliHome})

	session, err := loadZoweSession(opts)
	if err != nil {
		t.Fatalf("loadZoweSession() error = %v", err)
	}
	if session.Host != "custom.example" {
		t.Fatalf("session host = %q, want config from ZOWE_CLI_HOME", session.Host)
	}
}

func TestLoadZoweVaultUsesLegacyServiceAndWindowsChunks(t *testing.T) {
	want := map[string]map[string]any{
		`C:\Users\me\.zowe\zowe.config.json`: {"profiles.base.properties.password": "secret"},
	}
	encoded, err := json.Marshal(want)
	if err != nil {
		t.Fatal(err)
	}
	encoded = append(encoded, 0)
	keyring := &fakeZoweKeyring{values: make(map[string]string)}
	const chunkSize = 11
	for i, start := 1, 0; start < len(encoded); i, start = i+1, start+chunkSize {
		end := min(start+chunkSize, len(encoded))
		keyring.values["@zowe/cli\x00"+zoweSecureAccount+"-"+strconv.Itoa(i)] = string(encoded[start:end])
	}

	got, err := loadZoweVault(keyring, "windows")
	if err != nil {
		t.Fatalf("loadZoweVault() error = %v", err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("loadZoweVault() = %#v, want %#v", got, want)
	}
	if len(keyring.calls) < 4 || keyring.calls[0] != "Zowe\x00"+zoweSecureAccount {
		t.Fatalf("keyring calls = %q, want current service before legacy chunks", keyring.calls)
	}
}

func TestLoadZoweVaultPropagatesKeyringFailure(t *testing.T) {
	keyring := &fakeZoweKeyring{errs: map[string]error{
		"Zowe\x00" + zoweSecureAccount: errors.New("vault is locked"),
	}}
	_, err := loadZoweVault(keyring, "linux")
	if err == nil || !strings.Contains(err.Error(), "vault is locked") {
		t.Fatalf("loadZoweVault() error = %v", err)
	}
}

func TestNormalizeJSONCPreservesCommentMarkersInStrings(t *testing.T) {
	input := []byte(`{"url":"https://example/*literal*/",/* comment */"list":[1,2,],}`)
	normalized, err := normalizeJSONC(input)
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal(normalized, &got); err != nil {
		t.Fatalf("normalized JSON %q: %v", normalized, err)
	}
	if got["url"] != "https://example/*literal*/" {
		t.Fatalf("url = %q", got["url"])
	}
}
