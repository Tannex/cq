package zowe

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
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
	return "", ErrSecretNotFound
}

func zoweTestOptions(home, work string, keyring Keyring, env map[string]string) LoadOptions {
	return LoadOptions{
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
      "properties": {"basePath": "/api/v1/", "rejectUnauthorized": true},
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
		"Zowe\x00" + zoweSecureAccount: base64.StdEncoding.EncodeToString(vault),
	}}

	session, err := Load(zoweTestOptions(home, work, keyring, nil))
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	want := Session{
		Profile: "lpar1.zosmf", Protocol: "https", Host: "mainframe.example",
		Port: 1443, BasePath: "/api/v1", User: "IBMUSER", Password: "secret",
		RejectUnauthorized: true,
	}
	if !reflect.DeepEqual(session, want) {
		t.Fatalf("Load() = %#v, want %#v", session, want)
	}
	if !reflect.DeepEqual(keyring.calls, []string{"Zowe\x00" + zoweSecureAccount}) {
		t.Fatalf("keyring calls = %q", keyring.calls)
	}
}

func TestSessionFormattingAndJSONDoNotExposeCredentials(t *testing.T) {
	session := Session{
		Profile: "test", Protocol: "https", Host: "host", Port: 443,
		User: "IBMUSER", Password: "password-secret", TokenType: "LtpaToken2", TokenValue: "token-secret",
	}
	for _, formatted := range []string{
		fmt.Sprint(session),
		fmt.Sprintf("%+v", session),
		fmt.Sprintf("%#v", session),
	} {
		if strings.Contains(formatted, "password-secret") || strings.Contains(formatted, "token-secret") {
			t.Fatalf("formatted session exposes credentials: %q", formatted)
		}
	}
	encoded, err := json.Marshal(session)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "password-secret") || strings.Contains(string(encoded), "token-secret") {
		t.Fatalf("session JSON exposes credentials: %s", encoded)
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
		"ZOWE_OPT_PROTOCOL":            "http",
		"ZOWE_OPT_TOKEN_VALUE":         "token-from-env",
		"ZOWE_OPT_REJECT_UNAUTHORIZED": "false",
	}

	session, err := Load(zoweTestOptions(home, work, &fakeZoweKeyring{}, env))
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if session.Profile != "lpar.zosmf" || session.Protocol != "http" || session.Host != "override.example" || session.Port != 7554 {
		t.Fatalf("session selection/overrides = %#v", session)
	}
	if session.BasePath != "/gateway" || session.TokenType != "LtpaToken2" || session.TokenValue != "token-from-env" {
		t.Fatalf("session merged properties = %#v", session)
	}
	if session.RejectUnauthorized {
		t.Fatalf("RejectUnauthorized = true, want environment override false")
	}
}

func TestLoadZoweSessionResolvesEncodingPrecedence(t *testing.T) {
	home := t.TempDir()
	project := t.TempDir()
	writeZoweTestFile(t, filepath.Join(home, ".zowe", "zowe.config.json"), `{
  "profiles": {
    "base":{"type":"base", "properties":{"host":"host", "user":"u", "password":"p", "encoding":"IBM-277"}},
    "base_only":{"type":"zosmf"},
    "lpar":{"properties":{"encoding":"IBM-1047"}, "profiles":{"zosmf":{"type":"zosmf"}}},
    "layered":{"type":"zosmf", "properties":{"encoding":"IBM-037"}}
  },
  "defaults":{"base":"base", "zosmf":"base_only"}
}`)
	writeZoweTestFile(t, filepath.Join(project, "zowe.config.json"), `{
  "profiles":{"layered":{"properties":{"encoding":"IBM-1047"}}}
}`)
	writeZoweTestFile(t, filepath.Join(project, "zowe.config.user.json"), `{
  "profiles":{"layered":{"properties":{"encoding":" IBM-1140 "}}}
}`)

	tests := []struct {
		name string
		env  map[string]string
		want string
	}{
		{name: "base profile", env: map[string]string{"ZOWE_OPT_ZOSMF_PROFILE": "base_only"}, want: "IBM-277"},
		{name: "nested parent", env: map[string]string{"ZOWE_OPT_ZOSMF_PROFILE": "lpar.zosmf"}, want: "IBM-1047"},
		{name: "project user layer", env: map[string]string{"ZOWE_OPT_ZOSMF_PROFILE": "layered"}, want: "IBM-1140"},
		{name: "environment", env: map[string]string{
			"ZOWE_OPT_ZOSMF_PROFILE": "layered",
			"ZOWE_OPT_ENCODING":      "IBM-1142",
		}, want: "IBM-1142"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			session, err := Load(zoweTestOptions(home, project, &fakeZoweKeyring{}, tt.env))
			if err != nil {
				t.Fatalf("Load() error = %v", err)
			}
			if session.Encoding != tt.want {
				t.Fatalf("session.Encoding = %q, want %q", session.Encoding, tt.want)
			}
		})
	}
}

func TestLoadZoweSessionRequiresStringEncoding(t *testing.T) {
	home := t.TempDir()
	work := t.TempDir()
	writeZoweTestFile(t, filepath.Join(home, ".zowe", "zowe.config.json"), `{
  "profiles": {
    "base":{"type":"base", "properties":{"host":"host", "user":"u", "password":"p"}},
    "zosmf":{"type":"zosmf", "properties":{"encoding":1047}}
  },
  "defaults":{"base":"base", "zosmf":"zosmf"}
}`)

	_, err := Load(zoweTestOptions(home, work, &fakeZoweKeyring{}, nil))
	if err == nil || !strings.Contains(err.Error(), "Zowe property encoding must be a string") {
		t.Fatalf("Load() error = %v, want string encoding error", err)
	}
}

func TestLoadZoweSessionAllowsDisabledTLSVerification(t *testing.T) {
	home := t.TempDir()
	work := t.TempDir()
	writeZoweTestFile(t, filepath.Join(home, ".zowe", "zowe.config.json"), `{
  "profiles": {
    "base":{"type":"base", "properties":{"host":"mainframe.example", "user":"u", "password":"p"}},
    "zosmf":{"type":"zosmf", "properties":{"rejectUnauthorized":false}}
  },
  "defaults":{"base":"base", "zosmf":"zosmf"}
}`)

	session, err := Load(zoweTestOptions(home, work, &fakeZoweKeyring{}, nil))
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if session.RejectUnauthorized {
		t.Fatal("RejectUnauthorized = true, want false")
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

	session, err := Load(zoweTestOptions(home, project, &fakeZoweKeyring{}, nil))
	if err != nil {
		t.Fatalf("Load() error = %v", err)
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

	session, err := Load(zoweTestOptions(home, work, &fakeZoweKeyring{}, map[string]string{
		"ZOWE_OPT_ZOSMF_PROFILE": "second",
	}))
	if err != nil {
		t.Fatalf("Load() error = %v", err)
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

func TestZoweIntPropertyRejectsNativeIntOverflow(t *testing.T) {
	tooLarge := "2147483648"
	if strconv.IntSize == 64 {
		tooLarge = "9223372036854775808"
	}

	_, err := zoweIntProperty(map[string]any{"port": json.Number(tooLarge)}, "port")
	if err == nil || !strings.Contains(err.Error(), "must be an integer") {
		t.Fatalf("zoweIntProperty() error = %v, want native int overflow", err)
	}
}

func TestLoadZoweSessionReportsMissingSecureStore(t *testing.T) {
	home := t.TempDir()
	work := t.TempDir()
	writeZoweTestFile(t, filepath.Join(home, ".zowe", "zowe.config.json"), `{
  "profiles":{"base":{"type":"base", "properties":{"host":"host"}, "secure":["user","password"]}, "zosmf":{"type":"zosmf"}},
  "defaults":{"base":"base", "zosmf":"zosmf"}
}`)

	_, err := Load(zoweTestOptions(home, work, &fakeZoweKeyring{}, nil))
	if err == nil || !strings.Contains(err.Error(), "secure credential store") || !strings.Contains(err.Error(), zoweSecureAccount) {
		t.Fatalf("Load() error = %v, want secure-store guidance", err)
	}
}

func TestLoadZoweSessionReportsMissingConfig(t *testing.T) {
	_, err := Load(zoweTestOptions(t.TempDir(), t.TempDir(), &fakeZoweKeyring{}, nil))
	if err == nil || !strings.Contains(err.Error(), "no Zowe team configuration") {
		t.Fatalf("Load() error = %v, want missing-config guidance", err)
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

	session, err := Load(opts)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if session.Host != "custom.example" {
		t.Fatalf("session host = %q, want config from ZOWE_CLI_HOME", session.Host)
	}
}

func TestLoadZoweVaultUsesLegacyServiceAndWindowsChunks(t *testing.T) {
	want := map[string]map[string]any{
		`C:\Users\me\.zowe\zowe.config.json`: {"profiles.base.properties.password": "secret"},
	}
	vaultJSON, err := json.Marshal(want)
	if err != nil {
		t.Fatal(err)
	}
	encoded := []byte(base64.StdEncoding.EncodeToString(vaultJSON))
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

func TestListProfilesReturnsSortedZosmfProfilesWithDefaultFirst(t *testing.T) {
	home := t.TempDir()
	work := t.TempDir()
	writeZoweTestFile(t, filepath.Join(home, ".zowe", "zowe.config.json"), `{
  "profiles": {
    "base": {"type": "base", "properties": {"host": "host", "user": "u", "password": "p"}},
    "first": {"type": "zosmf", "properties": {"host": "first.example"}},
    "second": {"type": "zosmf", "properties": {"host": "second.example"}},
    "lpar": {"properties": {"host": "lpar.example"}, "profiles": {"zosmf": {"type": "zosmf", "properties": {}}}},
    "ignored": {"type": "tso", "properties": {"host": "tso.example"}}
  },
  "defaults": {"base": "base", "zosmf": "second"}
}`)

	names, err := ListProfiles(zoweTestOptions(home, work, &fakeZoweKeyring{}, nil))
	if err != nil {
		t.Fatalf("ListProfiles() error = %v", err)
	}
	want := []string{"second", "first", "lpar.zosmf"}
	if !reflect.DeepEqual(names, want) {
		t.Fatalf("ListProfiles() = %v, want %v", names, want)
	}
}

func TestListProfilesRespectsEnvDefault(t *testing.T) {
	home := t.TempDir()
	work := t.TempDir()
	writeZoweTestFile(t, filepath.Join(home, ".zowe", "zowe.config.json"), `{
  "profiles": {
    "alpha": {"type": "zosmf", "properties": {"host": "alpha.example"}},
    "beta": {"type": "zosmf", "properties": {"host": "beta.example"}},
    "gamma": {"type": "zosmf", "properties": {"host": "gamma.example"}}
  },
  "defaults": {"zosmf": "gamma"}
}`)

	env := map[string]string{"ZOWE_OPT_ZOSMF_PROFILE": "beta"}
	names, err := ListProfiles(zoweTestOptions(home, work, &fakeZoweKeyring{}, env))
	if err != nil {
		t.Fatalf("ListProfiles() error = %v", err)
	}
	want := []string{"beta", "alpha", "gamma"}
	if !reflect.DeepEqual(names, want) {
		t.Fatalf("ListProfiles() = %v, want %v", names, want)
	}
}

func TestListProfilesDoesNotRequireVault(t *testing.T) {
	home := t.TempDir()
	work := t.TempDir()
	writeZoweTestFile(t, filepath.Join(home, ".zowe", "zowe.config.json"), `{
  "profiles": {
    "secure_zosmf": {"type": "zosmf", "properties": {}, "secure": ["user", "password"]}
  },
  "defaults": {"zosmf": "secure_zosmf"}
}`)

	keyring := &fakeZoweKeyring{errs: map[string]error{
		"Zowe\x00" + zoweSecureAccount: errors.New("vault should not be opened"),
	}}
	names, err := ListProfiles(zoweTestOptions(home, work, keyring, nil))
	if err != nil {
		t.Fatalf("ListProfiles() error = %v", err)
	}
	if len(keyring.calls) != 0 {
		t.Fatalf("ListProfiles() opened the vault unexpectedly: calls = %q", keyring.calls)
	}
	want := []string{"secure_zosmf"}
	if !reflect.DeepEqual(names, want) {
		t.Fatalf("ListProfiles() = %v, want %v", names, want)
	}
}

func TestLoadNamedLoadsNonDefaultProfileWithBaseMerge(t *testing.T) {
	home := t.TempDir()
	work := t.TempDir()
	writeZoweTestFile(t, filepath.Join(home, ".zowe", "zowe.config.json"), `{
  "profiles": {
    "base": {"type": "base", "properties": {"host": "mainframe.example", "port": 1443, "user": "IBMUSER", "password": "secret"}},
    "first": {"type": "zosmf", "properties": {"host": "first.example"}},
    "second": {"type": "zosmf", "properties": {"host": "second.example", "basePath": "/api/v1/"}}
  },
  "defaults": {"base": "base", "zosmf": "first"}
}`)

	session, err := LoadNamed(zoweTestOptions(home, work, &fakeZoweKeyring{}, nil), "second")
	if err != nil {
		t.Fatalf("LoadNamed() error = %v", err)
	}
	want := Session{
		Profile: "second", Protocol: "https", Host: "second.example",
		Port: 1443, BasePath: "/api/v1", User: "IBMUSER", Password: "secret",
		RejectUnauthorized: true,
	}
	if !reflect.DeepEqual(session, want) {
		t.Fatalf("LoadNamed() = %#v, want %#v", session, want)
	}
}

func TestLoadNamedAppliesEnvironmentOverrides(t *testing.T) {
	home := t.TempDir()
	work := t.TempDir()
	writeZoweTestFile(t, filepath.Join(home, ".zowe", "zowe.config.json"), `{
  "profiles": {
    "base": {"type": "base", "properties": {"host": "host", "user": "u", "password": "p"}},
    "first": {"type": "zosmf", "properties": {"host": "first.example"}},
    "second": {"type": "zosmf", "properties": {"host": "second.example"}}
  },
  "defaults": {"base": "base", "zosmf": "first"}
}`)

	env := map[string]string{
		"ZOWE_OPT_HOST":     "override.example",
		"ZOWE_OPT_PORT":     "7554",
		"ZOWE_OPT_PROTOCOL": "http",
	}
	session, err := LoadNamed(zoweTestOptions(home, work, &fakeZoweKeyring{}, env), "second")
	if err != nil {
		t.Fatalf("LoadNamed() error = %v", err)
	}
	if session.Profile != "second" || session.Host != "override.example" || session.Port != 7554 || session.Protocol != "http" {
		t.Fatalf("LoadNamed() = %#v, want environment overrides", session)
	}
}

func TestLoadNamedUnknownProfileReturnsError(t *testing.T) {
	home := t.TempDir()
	work := t.TempDir()
	writeZoweTestFile(t, filepath.Join(home, ".zowe", "zowe.config.json"), `{
  "profiles": {
    "base": {"type": "base", "properties": {"host": "host", "user": "u", "password": "p"}},
    "zosmf": {"type": "zosmf", "properties": {"host": "host.example"}}
  },
  "defaults": {"base": "base", "zosmf": "zosmf"}
}`)

	_, err := LoadNamed(zoweTestOptions(home, work, &fakeZoweKeyring{}, nil), "missing")
	if err == nil || !strings.Contains(err.Error(), `zosmf profile "missing" does not exist`) {
		t.Fatalf("LoadNamed() error = %v, want does-not-exist error", err)
	}
}

func TestLoadNamedEmptyProfileReturnsError(t *testing.T) {
	_, err := LoadNamed(zoweTestOptions(t.TempDir(), t.TempDir(), &fakeZoweKeyring{}, nil), "")
	if err == nil || !strings.Contains(err.Error(), "no zosmf profile name provided") {
		t.Fatalf("LoadNamed() error = %v, want empty-name error", err)
	}
}

func TestLoadNamedIgnoresZOWE_OPT_ZOSMF_PROFILE(t *testing.T) {
	home := t.TempDir()
	work := t.TempDir()
	writeZoweTestFile(t, filepath.Join(home, ".zowe", "zowe.config.json"), `{
  "profiles": {
    "base": {"type": "base", "properties": {"host": "host", "user": "u", "password": "p"}},
    "first": {"type": "zosmf", "properties": {"host": "first.example"}},
    "second": {"type": "zosmf", "properties": {"host": "second.example"}}
  },
  "defaults": {"base": "base", "zosmf": "first"}
}`)

	env := map[string]string{"ZOWE_OPT_ZOSMF_PROFILE": "first"}
	session, err := LoadNamed(zoweTestOptions(home, work, &fakeZoweKeyring{}, env), "second")
	if err != nil {
		t.Fatalf("LoadNamed() error = %v", err)
	}
	if session.Profile != "second" || session.Host != "second.example" {
		t.Fatalf("LoadNamed() = %#v, want named second profile", session)
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
