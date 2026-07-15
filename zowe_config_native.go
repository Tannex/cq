package main

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
)

const zoweSecureAccount = "secure_config_props"

var zoweCredentialServices = []string{"Zowe", "@zowe/cli", "Zowe-Plugin", "Broadcom-Plugin"}

// zoweSession is the fully resolved subset of a Zowe zosmf profile needed by
// cq. Password and token values are deliberately kept out of diagnostics.
type zoweSession struct {
	Profile            string
	Protocol           string
	Host               string
	Port               int
	BasePath           string
	Encoding           string
	User               string
	Password           string
	TokenType          string
	TokenValue         string
	RejectUnauthorized bool
}

type zoweLoadOptions struct {
	HomeDir    string
	WorkingDir string
	GOOS       string
	Getenv     func(string) string
	Keyring    zoweKeyring
}

type zoweClientConfig struct {
	Profiles map[string]zoweProfile `json:"profiles"`
	Defaults map[string]string      `json:"defaults"`
}

type zoweProfile struct {
	Type       string                 `json:"type"`
	Properties map[string]any         `json:"properties"`
	Secure     []string               `json:"secure"`
	Profiles   map[string]zoweProfile `json:"profiles"`
}

type zoweConfigLayer struct {
	path   string
	global bool
	config zoweClientConfig
}

type resolvedZoweProfile struct {
	typeName   string
	properties map[string]any
}

// loadDefaultZoweSession is a variable so integration tests can put the
// network boundary behind a deterministic session without touching a user's
// real Zowe configuration or keyring.
var loadDefaultZoweSession = func() (zoweSession, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return zoweSession{}, fmt.Errorf("locate home directory: %w", err)
	}
	work, err := os.Getwd()
	if err != nil {
		return zoweSession{}, fmt.Errorf("locate working directory: %w", err)
	}
	return loadZoweSession(zoweLoadOptions{
		HomeDir: home, WorkingDir: work, GOOS: runtime.GOOS,
		Getenv: os.Getenv, Keyring: systemZoweKeyring{},
	})
}

// loadZoweSession resolves the Zowe team configuration surface:
// global and project team/user configs, nested profiles, the default base and
// zosmf profiles, secure properties, and ZOWE_OPT_* overrides.
func loadZoweSession(opts zoweLoadOptions) (zoweSession, error) {
	if opts.Getenv == nil {
		opts.Getenv = func(string) string { return "" }
	}
	if opts.GOOS == "" {
		opts.GOOS = runtime.GOOS
	}
	if opts.Keyring == nil {
		opts.Keyring = systemZoweKeyring{}
	}

	layers, err := readZoweConfigLayers(opts)
	if err != nil {
		return zoweSession{}, err
	}
	if len(layers) == 0 {
		return zoweSession{}, errors.New("no Zowe team configuration found; run 'zowe config init --global-config' or create zowe.config.json in this project")
	}

	if zoweLayersHaveSecureFields(layers) {
		vault, err := loadZoweVault(opts.Keyring, opts.GOOS)
		if err != nil {
			return zoweSession{}, fmt.Errorf("cannot open the secure credential store used by the Zowe configuration: %w", err)
		}
		for i := range layers {
			applyZoweSecureValues(&layers[i], vault)
		}
	}

	var globalLayers []zoweConfigLayer
	for _, layer := range layers {
		if layer.global {
			globalLayers = append(globalLayers, layer)
		}
	}
	globalConfig := mergeZoweLayers(globalLayers)
	allConfig := mergeZoweLayers(layers)

	profileName := strings.TrimSpace(opts.Getenv("ZOWE_OPT_ZOSMF_PROFILE"))
	if profileName == "" {
		profileName = allConfig.Defaults["zosmf"]
	}
	if profileName == "" {
		return zoweSession{}, errors.New("no default zosmf profile found in the Zowe configuration; run 'zowe config init' or set ZOWE_OPT_ZOSMF_PROFILE")
	}

	// A profile which only exists globally must use the global default base,
	// even when an unrelated project config is present. This matches Zowe's
	// layer-aware ProfileInfo behavior.
	scope := globalConfig
	if zoweProfileDefinedInProject(layers, profileName) {
		scope = allConfig
	}
	profiles := flattenZoweProfiles(scope.Profiles)
	profile, ok := profiles[profileName]
	if !ok || profile.typeName != "zosmf" {
		if wanted := strings.TrimSpace(opts.Getenv("ZOWE_OPT_ZOSMF_PROFILE")); wanted != "" {
			return zoweSession{}, fmt.Errorf("ZOWE_OPT_ZOSMF_PROFILE names zosmf profile %q, but the Zowe configuration has no such profile", wanted)
		}
		return zoweSession{}, fmt.Errorf("default zosmf profile %q does not exist", profileName)
	}

	props := map[string]any{}
	if baseName := scope.Defaults["base"]; baseName != "" {
		base, ok := profiles[baseName]
		if !ok || base.typeName != "base" {
			return zoweSession{}, fmt.Errorf("default base profile %q does not exist", baseName)
		}
		props = mergeZoweProperties(props, base.properties)
	}
	props = mergeZoweProperties(props, profile.properties)
	applyZoweEnvironment(props, opts.Getenv)
	return makeZoweSession(profileName, props)
}

func readZoweConfigLayers(opts zoweLoadOptions) ([]zoweConfigLayer, error) {
	cliHome := strings.TrimSpace(opts.Getenv("ZOWE_CLI_HOME"))
	if cliHome == "" {
		cliHome = filepath.Join(opts.HomeDir, ".zowe")
	} else if !filepath.IsAbs(cliHome) {
		cliHome = filepath.Join(opts.WorkingDir, cliHome)
	}
	cliHome = filepath.Clean(cliHome)

	var candidates []struct {
		path   string
		global bool
	}
	// Low-to-high precedence: global team, global user, project team,
	// project user.
	candidates = append(candidates,
		struct {
			path   string
			global bool
		}{filepath.Join(cliHome, "zowe.config.json"), true},
		struct {
			path   string
			global bool
		}{filepath.Join(cliHome, "zowe.config.user.json"), true},
	)

	work, err := filepath.Abs(opts.WorkingDir)
	if err != nil {
		return nil, fmt.Errorf("resolve working directory %q: %w", opts.WorkingDir, err)
	}
	for dir := filepath.Clean(work); ; dir = filepath.Dir(dir) {
		team := filepath.Join(dir, "zowe.config.json")
		user := filepath.Join(dir, "zowe.config.user.json")
		if fileExists(team) || fileExists(user) {
			candidates = append(candidates,
				struct {
					path   string
					global bool
				}{team, false},
				struct {
					path   string
					global bool
				}{user, false},
			)
			break
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
	}

	seen := make(map[string]bool)
	var layers []zoweConfigLayer
	for _, candidate := range candidates {
		path, err := filepath.Abs(candidate.path)
		if err != nil {
			return nil, fmt.Errorf("resolve Zowe config path %q: %w", candidate.path, err)
		}
		path = filepath.Clean(path)
		if seen[path] || !fileExists(path) {
			continue
		}
		seen[path] = true
		cfg, err := readZoweConfigFile(path)
		if err != nil {
			return nil, err
		}
		layers = append(layers, zoweConfigLayer{path: path, global: candidate.global, config: cfg})
	}
	return layers, nil
}

func fileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}

func readZoweConfigFile(path string) (zoweClientConfig, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return zoweClientConfig{}, fmt.Errorf("read Zowe config %q: %w", path, err)
	}
	normalized, err := normalizeJSONC(b)
	if err != nil {
		return zoweClientConfig{}, fmt.Errorf("parse Zowe config %q: %w", path, err)
	}
	var cfg zoweClientConfig
	dec := json.NewDecoder(bytes.NewReader(normalized))
	dec.UseNumber()
	if err := dec.Decode(&cfg); err != nil {
		return zoweClientConfig{}, fmt.Errorf("parse Zowe config %q: %w", path, err)
	}
	if cfg.Profiles == nil {
		cfg.Profiles = make(map[string]zoweProfile)
	}
	if cfg.Defaults == nil {
		cfg.Defaults = make(map[string]string)
	}
	return cfg, nil
}

func zoweLayersHaveSecureFields(layers []zoweConfigLayer) bool {
	for _, layer := range layers {
		if zoweProfilesHaveSecureFields(layer.config.Profiles) {
			return true
		}
	}
	return false
}

func zoweProfilesHaveSecureFields(profiles map[string]zoweProfile) bool {
	for _, profile := range profiles {
		if len(profile.Secure) > 0 || zoweProfilesHaveSecureFields(profile.Profiles) {
			return true
		}
	}
	return false
}

func loadZoweVault(keyring zoweKeyring, goos string) (map[string]map[string]any, error) {
	var value string
	for _, service := range zoweCredentialServices {
		v, err := loadZoweCredential(keyring, service, zoweSecureAccount, goos)
		if err == nil {
			value = v
			break
		}
		if !errors.Is(err, errZoweSecretNotFound) {
			return nil, fmt.Errorf("load service %q account %q: %w", service, zoweSecureAccount, err)
		}
	}
	if value == "" {
		return nil, fmt.Errorf("no entry found for service %q account %q", zoweCredentialServices[0], zoweSecureAccount)
	}
	// Imperative's AbstractCredentialManager Base64-encodes every value before
	// handing it to the OS-specific credential manager. The keyring adapters
	// return that stored representation, so mirror AbstractCredentialManager.load
	// before parsing ConfigSecure's JSON object.
	decoded, err := base64.StdEncoding.DecodeString(strings.TrimSpace(value))
	if err != nil {
		return nil, fmt.Errorf("decode secure Zowe properties: %w", err)
	}
	normalized, err := normalizeJSONC(decoded)
	if err != nil {
		return nil, fmt.Errorf("parse secure Zowe properties: %w", err)
	}
	var vault map[string]map[string]any
	dec := json.NewDecoder(bytes.NewReader(normalized))
	dec.UseNumber()
	if err := dec.Decode(&vault); err != nil {
		return nil, fmt.Errorf("parse secure Zowe properties: %w", err)
	}
	return vault, nil
}

func loadZoweCredential(keyring zoweKeyring, service, account, goos string) (string, error) {
	value, err := keyring.Get(service, account)
	if err == nil || goos != "windows" || !errors.Is(err, errZoweSecretNotFound) {
		return value, err
	}

	// Zowe splits values longer than 2560 characters across numbered Windows
	// Credential Manager entries and terminates the final chunk with NUL.
	var chunks strings.Builder
	for i := 1; i <= 4096; i++ {
		chunk, chunkErr := keyring.Get(service, fmt.Sprintf("%s-%d", account, i))
		if chunkErr != nil {
			if errors.Is(chunkErr, errZoweSecretNotFound) && i == 1 {
				return "", errZoweSecretNotFound
			}
			return "", fmt.Errorf("load chunk %d: %w", i, chunkErr)
		}
		chunks.WriteString(chunk)
		if strings.HasSuffix(chunk, "\x00") {
			return strings.TrimSuffix(chunks.String(), "\x00"), nil
		}
	}
	return "", errors.New("secure Zowe properties use more than 4096 Windows credential chunks")
}

func applyZoweSecureValues(layer *zoweConfigLayer, vault map[string]map[string]any) {
	values := vault[layer.path]
	if values == nil {
		for path, candidate := range vault {
			if filepath.Clean(path) == layer.path {
				values = candidate
				break
			}
		}
	}
	applyZoweProfileSecrets(layer.config.Profiles, "profiles", values)
}

func applyZoweProfileSecrets(profiles map[string]zoweProfile, prefix string, values map[string]any) {
	for name, profile := range profiles {
		profilePath := prefix + "." + name
		if profile.Properties == nil {
			profile.Properties = make(map[string]any)
		}
		for _, property := range profile.Secure {
			if value, ok := values[profilePath+".properties."+property]; ok {
				profile.Properties[property] = value
			}
		}
		applyZoweProfileSecrets(profile.Profiles, profilePath+".profiles", values)
		profiles[name] = profile
	}
}

func mergeZoweLayers(layers []zoweConfigLayer) zoweClientConfig {
	merged := zoweClientConfig{Profiles: make(map[string]zoweProfile), Defaults: make(map[string]string)}
	for _, layer := range layers {
		for name, profile := range layer.config.Profiles {
			merged.Profiles[name] = mergeZoweProfile(merged.Profiles[name], profile)
		}
		for kind, name := range layer.config.Defaults {
			merged.Defaults[kind] = name
		}
	}
	return merged
}

func mergeZoweProfile(low, high zoweProfile) zoweProfile {
	out := low
	if high.Type != "" {
		out.Type = high.Type
	}
	out.Properties = mergeZoweProperties(low.Properties, high.Properties)
	if high.Secure != nil {
		out.Secure = append([]string(nil), high.Secure...)
	}
	if out.Profiles == nil {
		out.Profiles = make(map[string]zoweProfile)
	}
	for name, profile := range high.Profiles {
		out.Profiles[name] = mergeZoweProfile(out.Profiles[name], profile)
	}
	return out
}

func mergeZoweProperties(low, high map[string]any) map[string]any {
	out := make(map[string]any, len(low)+len(high))
	for name, value := range low {
		out[name] = value
	}
	for name, value := range high {
		out[name] = value
	}
	return out
}

func flattenZoweProfiles(profiles map[string]zoweProfile) map[string]resolvedZoweProfile {
	out := make(map[string]resolvedZoweProfile)
	var walk func(map[string]zoweProfile, string, map[string]any)
	walk = func(current map[string]zoweProfile, prefix string, inherited map[string]any) {
		for name, profile := range current {
			fullName := name
			if prefix != "" {
				fullName = prefix + "." + name
			}
			properties := mergeZoweProperties(inherited, profile.Properties)
			if profile.Type != "" {
				out[fullName] = resolvedZoweProfile{typeName: profile.Type, properties: properties}
			}
			walk(profile.Profiles, fullName, properties)
		}
	}
	walk(profiles, "", nil)
	return out
}

func zoweProfileDefinedInProject(layers []zoweConfigLayer, wanted string) bool {
	for _, layer := range layers {
		if !layer.global && zoweProfileDefined(layer.config.Profiles, "", wanted) {
			return true
		}
	}
	return false
}

func zoweProfileDefined(profiles map[string]zoweProfile, prefix, wanted string) bool {
	for name, profile := range profiles {
		fullName := name
		if prefix != "" {
			fullName = prefix + "." + name
		}
		if fullName == wanted || zoweProfileDefined(profile.Profiles, fullName, wanted) {
			return true
		}
	}
	return false
}

func applyZoweEnvironment(properties map[string]any, getenv func(string) string) {
	for _, name := range []string{
		"host", "port", "basePath", "protocol", "user", "password",
		"tokenType", "tokenValue", "rejectUnauthorized", "encoding",
	} {
		key := "ZOWE_OPT_" + camelToEnvironment(name)
		if value := getenv(key); value != "" {
			switch name {
			case "port", "rejectUnauthorized":
				properties[name] = parseZoweEnvironmentValue(value)
			default:
				properties[name] = value
			}
		}
	}
}

func camelToEnvironment(name string) string {
	var out strings.Builder
	for i, r := range name {
		if i > 0 && r >= 'A' && r <= 'Z' {
			out.WriteByte('_')
		}
		out.WriteRune(r)
	}
	return strings.ToUpper(out.String())
}

func parseZoweEnvironmentValue(value string) any {
	if strings.EqualFold(value, "true") {
		return true
	}
	if strings.EqualFold(value, "false") {
		return false
	}
	if number, err := strconv.ParseFloat(value, 64); err == nil {
		return number
	}
	return value
}

func makeZoweSession(profileName string, properties map[string]any) (zoweSession, error) {
	protocol, err := zoweStringProperty(properties, "protocol")
	if err != nil {
		return zoweSession{}, err
	}
	if protocol == "" {
		protocol = "https"
	}
	protocol = strings.ToLower(protocol)
	if protocol != "https" && protocol != "http" {
		return zoweSession{}, fmt.Errorf("zosmf profile %q has unsupported protocol %q", profileName, protocol)
	}

	host, err := zoweStringProperty(properties, "host")
	if err != nil {
		return zoweSession{}, err
	}
	if host == "" {
		return zoweSession{}, fmt.Errorf("zosmf profile %q has no host", profileName)
	}
	if strings.Contains(host, "://") {
		return zoweSession{}, fmt.Errorf("zosmf profile %q host must not include a URL scheme: %q", profileName, host)
	}
	port, err := zoweIntProperty(properties, "port")
	if err != nil {
		return zoweSession{}, err
	}
	if port == 0 {
		if protocol == "http" {
			port = 80
		} else {
			port = 443
		}
	}
	if port < 1 || port > 65535 {
		return zoweSession{}, fmt.Errorf("zosmf profile %q has invalid port %d", profileName, port)
	}

	basePath, err := zoweStringProperty(properties, "basePath")
	if err != nil {
		return zoweSession{}, err
	}
	basePath = strings.TrimRight(strings.TrimSpace(basePath), "/")
	if basePath != "" && !strings.HasPrefix(basePath, "/") {
		basePath = "/" + basePath
	}
	user, err := zoweStringProperty(properties, "user")
	if err != nil {
		return zoweSession{}, err
	}
	password, err := zoweStringProperty(properties, "password")
	if err != nil {
		return zoweSession{}, err
	}
	tokenType, err := zoweStringProperty(properties, "tokenType")
	if err != nil {
		return zoweSession{}, err
	}
	tokenValue, err := zoweStringProperty(properties, "tokenValue")
	if err != nil {
		return zoweSession{}, err
	}
	encoding, err := zoweStringProperty(properties, "encoding")
	if err != nil {
		return zoweSession{}, err
	}
	encoding = strings.TrimSpace(encoding)
	rejectUnauthorized, err := zoweBoolProperty(properties, "rejectUnauthorized", true)
	if err != nil {
		return zoweSession{}, err
	}
	if protocol == "https" && !rejectUnauthorized {
		return zoweSession{}, fmt.Errorf("zosmf profile %q disables TLS certificate verification with rejectUnauthorized=false; trust the z/OSMF certificate authority instead", profileName)
	}
	if tokenValue == "" && user == "" {
		return zoweSession{}, fmt.Errorf("zosmf profile %q has neither a token nor a user; log in with 'zowe auth login' or add credentials to the profile", profileName)
	}
	if tokenValue == "" && password == "" {
		return zoweSession{}, fmt.Errorf("zosmf profile %q has a user but no password; add the password to the profile or its secure credential store", profileName)
	}
	return zoweSession{
		Profile: profileName, Protocol: protocol, Host: host, Port: port,
		BasePath: basePath, Encoding: encoding, User: user, Password: password,
		TokenType: tokenType, TokenValue: tokenValue,
		RejectUnauthorized: rejectUnauthorized,
	}, nil
}

func zoweStringProperty(properties map[string]any, name string) (string, error) {
	value, ok := properties[name]
	if !ok || value == nil {
		return "", nil
	}
	text, ok := value.(string)
	if !ok {
		return "", fmt.Errorf("Zowe property %s must be a string, got %T", name, value)
	}
	return text, nil
}

func zoweIntProperty(properties map[string]any, name string) (int, error) {
	value, ok := properties[name]
	if !ok || value == nil {
		return 0, nil
	}
	var n int64
	var err error
	switch value := value.(type) {
	case json.Number:
		n, err = value.Int64()
	case float64:
		if value != float64(int64(value)) {
			err = errors.New("not an integer")
		} else {
			n = int64(value)
		}
	case string:
		n, err = strconv.ParseInt(value, 10, 64)
	default:
		err = fmt.Errorf("got %T", value)
	}
	if err != nil {
		return 0, fmt.Errorf("Zowe property %s must be an integer: %w", name, err)
	}
	if n < int64(math.MinInt) || n > int64(math.MaxInt) {
		return 0, fmt.Errorf("Zowe property %s must fit in a %d-bit integer", name, strconv.IntSize)
	}
	return int(n), nil
}

func zoweBoolProperty(properties map[string]any, name string, fallback bool) (bool, error) {
	value, ok := properties[name]
	if !ok || value == nil {
		return fallback, nil
	}
	switch value := value.(type) {
	case bool:
		return value, nil
	case string:
		parsed, err := strconv.ParseBool(value)
		if err == nil {
			return parsed, nil
		}
	}
	return false, fmt.Errorf("Zowe property %s must be a boolean, got %T", name, value)
}

// normalizeJSONC removes comments and trailing commas while preserving JSON
// strings. Zowe's config reader accepts JSONC, so a Go replacement must not
// reject configs that the Zowe CLI itself accepts.
func normalizeJSONC(input []byte) ([]byte, error) {
	input = bytes.TrimPrefix(input, []byte{0xef, 0xbb, 0xbf})
	withoutComments := make([]byte, 0, len(input))
	inString, escaped := false, false
	for i := 0; i < len(input); i++ {
		c := input[i]
		if inString {
			withoutComments = append(withoutComments, c)
			if escaped {
				escaped = false
			} else if c == '\\' {
				escaped = true
			} else if c == '"' {
				inString = false
			}
			continue
		}
		if c == '"' {
			inString = true
			withoutComments = append(withoutComments, c)
			continue
		}
		if c == '/' && i+1 < len(input) && input[i+1] == '/' {
			i += 2
			for i < len(input) && input[i] != '\n' && input[i] != '\r' {
				i++
			}
			if i < len(input) {
				withoutComments = append(withoutComments, input[i])
			}
			continue
		}
		if c == '/' && i+1 < len(input) && input[i+1] == '*' {
			i += 2
			closed := false
			for i+1 < len(input) {
				if input[i] == '*' && input[i+1] == '/' {
					i++
					closed = true
					break
				}
				if input[i] == '\n' || input[i] == '\r' {
					withoutComments = append(withoutComments, input[i])
				}
				i++
			}
			if !closed {
				return nil, errors.New("unterminated block comment")
			}
			continue
		}
		withoutComments = append(withoutComments, c)
	}
	if inString {
		return nil, errors.New("unterminated string")
	}

	out := make([]byte, 0, len(withoutComments))
	inString, escaped = false, false
	for i := 0; i < len(withoutComments); i++ {
		c := withoutComments[i]
		if inString {
			out = append(out, c)
			if escaped {
				escaped = false
			} else if c == '\\' {
				escaped = true
			} else if c == '"' {
				inString = false
			}
			continue
		}
		if c == '"' {
			inString = true
			out = append(out, c)
			continue
		}
		if c == ',' {
			j := i + 1
			for j < len(withoutComments) && (withoutComments[j] == ' ' || withoutComments[j] == '\t' || withoutComments[j] == '\n' || withoutComments[j] == '\r') {
				j++
			}
			if j < len(withoutComments) && (withoutComments[j] == '}' || withoutComments[j] == ']') {
				continue
			}
		}
		out = append(out, c)
	}
	return out, nil
}
