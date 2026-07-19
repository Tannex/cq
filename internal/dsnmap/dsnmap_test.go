package dsnmap

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func testStore(t *testing.T) (*Store, string) {
	t.Helper()
	dir := t.TempDir()
	store := &Store{UserConfigDir: func() (string, error) { return dir, nil }}
	return store, filepath.Join(dir, "cq", StateFileName)
}

func TestMatchPatternDialect(t *testing.T) {
	cases := []struct {
		name, pattern string
		want          bool
	}{
		{"PROD.CUSTOMER.DATA", "PROD.CUSTOMER.DATA", true},
		{"prod.customer.data", "PROD.CUSTOMER.DATA", true},
		{"PROD.CUSTOMER.DATA", "", true},
		{"PROD.CUSTOMER.DATA", "*", true},
		{"PROD.CUSTOMER.DATA", "PROD.*", true},
		{"PROD.CUSTOMER.DATA", "TEST.*", false},
		{"PROD.CUSTOMER.G0042V00", "PROD.CUSTOMER.G%%%%V%%", true},
		{"PROD.CUSTOMER.G42V00", "PROD.CUSTOMER.G%%%%V%%", false},
		{"PROD.CUSTOMER.DATA", "PROD.*.DATA", true},
		{"PROD.X.Y.DATA", "PROD.*.DATA", true},
		{"PROD.CUSTOMER.OLD", "PROD.*.DATA", false},
		{"PROD.CUSTOMER.DATA", "PROD.CUSTOMER.DAT", false},
		{"A.B(MEMBER)", "A.B(*)", true},
		{"A.B(MEMBER)", "A.B(MEM%%%)", true},
	}
	for _, tc := range cases {
		if got := MatchPattern(tc.name, tc.pattern); got != tc.want {
			t.Errorf("MatchPattern(%q, %q) = %v, want %v", tc.name, tc.pattern, got, tc.want)
		}
	}
}

func TestMatchPrecedenceExactThenLongestLiteralPrefixThenLexicographic(t *testing.T) {
	store, _ := testStore(t)
	for _, pattern := range []string{"PROD.*", "PROD.CUSTOMER.*", "PROD.CUSTOMER.DATA", "*"} {
		if err := store.Put(Mapping{Pattern: pattern, Local: "/" + pattern}); err != nil {
			t.Fatal(err)
		}
	}

	mapping, ok := store.Match("PROD.CUSTOMER.DATA")
	if !ok || mapping.Pattern != "PROD.CUSTOMER.DATA" {
		t.Fatalf("exact match lost: %+v ok=%v", mapping, ok)
	}
	mapping, ok = store.Match("PROD.CUSTOMER.OLD")
	if !ok || mapping.Pattern != "PROD.CUSTOMER.*" {
		t.Fatalf("longest literal prefix lost: %+v ok=%v", mapping, ok)
	}
	mapping, ok = store.Match("TEST.ANYTHING")
	if !ok || mapping.Pattern != "*" {
		t.Fatalf("catch-all lost: %+v ok=%v", mapping, ok)
	}

	// Equal literal prefixes fall back to lexicographic pattern order.
	tie, _ := testStore(t)
	if err := tie.Put(Mapping{Pattern: "A.%X"}); err != nil {
		t.Fatal(err)
	}
	if err := tie.Put(Mapping{Pattern: "A.%B"}); err != nil {
		t.Fatal(err)
	}
	mapping, ok = tie.Match("A.XB")
	if !ok || mapping.Pattern != "A.%B" {
		t.Fatalf("tie-break not deterministic: %+v ok=%v", mapping, ok)
	}
}

func TestPersistenceRoundTrip(t *testing.T) {
	store, path := testStore(t)
	if err := store.Put(Mapping{Pattern: "prod.customer.*", Local: "/tmp/cust.cpy", Format: "fixed", Record: "CUSTOMER-REC"}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("state file not written: %v", err)
	}

	reloaded := &Store{UserConfigDir: store.UserConfigDir}
	mapping, ok := reloaded.Match("PROD.CUSTOMER.G0042V00")
	if !ok {
		t.Fatal("mapping not found after reload")
	}
	if mapping.Pattern != "PROD.CUSTOMER.*" || mapping.Local != "/tmp/cust.cpy" || mapping.Format != "fixed" || mapping.Record != "CUSTOMER-REC" {
		t.Fatalf("mapping mangled after reload: %+v", mapping)
	}
	if mapping.Created.IsZero() || mapping.LastUsed.IsZero() {
		t.Fatalf("timestamps not recorded: %+v", mapping)
	}
}

func TestPutReplacesPatternAndPreservesCreated(t *testing.T) {
	store, _ := testStore(t)
	early := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	late := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	store.Now = func() time.Time { return early }
	if err := store.Put(Mapping{Pattern: "A.B", Local: "/old.cpy"}); err != nil {
		t.Fatal(err)
	}
	store.Now = func() time.Time { return late }
	if err := store.Put(Mapping{Pattern: "A.B", Local: "/new.cpy"}); err != nil {
		t.Fatal(err)
	}
	mappings := store.Mappings()
	if len(mappings) != 1 {
		t.Fatalf("expected replacement, got %d mappings", len(mappings))
	}
	if mappings[0].Local != "/new.cpy" || !mappings[0].Created.Equal(early) || !mappings[0].LastUsed.Equal(late) {
		t.Fatalf("replacement bookkeeping wrong: %+v", mappings[0])
	}
}

func TestRemoveAndTouch(t *testing.T) {
	store, _ := testStore(t)
	if err := store.Put(Mapping{Pattern: "A.B", Local: "/a.cpy"}); err != nil {
		t.Fatal(err)
	}
	used := time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC)
	store.Now = func() time.Time { return used }
	if err := store.Touch("a.b"); err != nil {
		t.Fatal(err)
	}
	if mappings := store.Mappings(); !mappings[0].LastUsed.Equal(used) {
		t.Fatalf("touch did not update last-used: %+v", mappings[0])
	}

	removed, err := store.Remove("A.B")
	if err != nil || !removed {
		t.Fatalf("remove failed: removed=%v err=%v", removed, err)
	}
	removed, err = store.Remove("A.B")
	if err != nil || removed {
		t.Fatalf("second remove should be a no-op: removed=%v err=%v", removed, err)
	}
	if _, ok := store.Match("A.B"); ok {
		t.Fatal("removed mapping still matches")
	}
}

func TestMissingFileBehavesAsEmptyStore(t *testing.T) {
	store, _ := testStore(t)
	if _, ok := store.Match("ANY.DSN"); ok {
		t.Fatal("empty store matched something")
	}
	if mappings := store.Mappings(); len(mappings) != 0 {
		t.Fatalf("expected no mappings, got %d", len(mappings))
	}
}

func TestCorruptFileDegradesAndIsNeverOverwritten(t *testing.T) {
	store, path := testStore(t)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("{corrupt"), 0o600); err != nil {
		t.Fatal(err)
	}

	if _, ok := store.Match("ANY.DSN"); ok {
		t.Fatal("corrupt store matched something")
	}
	if err := store.Put(Mapping{Pattern: "A.B", Local: "/a.cpy"}); err == nil {
		t.Fatal("put against a corrupt state file must fail")
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(b) != "{corrupt" {
		t.Fatalf("corrupt state file was overwritten: %q", b)
	}
}

func TestUnknownFieldsTolerated(t *testing.T) {
	store, path := testStore(t)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	state := `{"version": 99, "mappings": [{"pattern": "A.B", "local": "/a.cpy", "futureField": true}]}`
	if err := os.WriteFile(path, []byte(state), 0o600); err != nil {
		t.Fatal(err)
	}
	mapping, ok := store.Match("A.B")
	if !ok || mapping.Local != "/a.cpy" {
		t.Fatalf("forward-compatible read failed: %+v ok=%v", mapping, ok)
	}
}

func TestMatchPatternRegexDialect(t *testing.T) {
	cases := []struct {
		name, pattern string
		want          bool
	}{
		{"PROD.CUSTOMER.G0042V00", `/PROD\.CUSTOMER\.G\d{4}V\d{2}/`, true},
		{"prod.customer.g0042v00", `/PROD\.CUSTOMER\.G\d{4}V\d{2}/`, true},
		{"PROD.CUSTOMER.G42V00", `/PROD\.CUSTOMER\.G\d{4}V\d{2}/`, false},
		// Anchored: a substring match is not enough.
		{"XPROD.CUSTOMER.G0042V00X", `/PROD\.CUSTOMER\.G\d{4}V\d{2}/`, false},
		// User-supplied anchors are tolerated.
		{"PROD.CUSTOMER.DATA", `/^PROD\..*$/`, true},
		{"A.B(MEMBER)", `/A\.B\(MEM.*\)/`, true},
		// Alternation spans the whole name thanks to anchoring.
		{"TEST.CUSTOMER.DATA", `/(PROD|TEST)\.CUSTOMER\.DATA/`, true},
		{"QA.CUSTOMER.DATA", `/(PROD|TEST)\.CUSTOMER\.DATA/`, false},
		// Invalid regexes match nothing instead of panicking.
		{"PROD.CUSTOMER.DATA", `/PROD\.(/`, false},
	}
	for _, tc := range cases {
		if got := MatchPattern(tc.name, tc.pattern); got != tc.want {
			t.Errorf("MatchPattern(%q, %q) = %v, want %v", tc.name, tc.pattern, got, tc.want)
		}
	}
}

func TestValidateAndNormalizePattern(t *testing.T) {
	if err := ValidatePattern(`/PROD\.G\d{4}/`); err != nil {
		t.Fatalf("valid regex rejected: %v", err)
	}
	if err := ValidatePattern(`/PROD\.(/`); err == nil {
		t.Fatal("invalid regex accepted")
	}
	if err := ValidatePattern("prod.*"); err != nil {
		t.Fatalf("wildcard pattern rejected: %v", err)
	}
	if got := NormalizePattern("  prod.customer.* "); got != "PROD.CUSTOMER.*" {
		t.Fatalf("wildcard not upper-cased: %q", got)
	}
	// Regex patterns keep their case so escape classes like \d survive.
	if got := NormalizePattern(` /prod\.g\d{4}/ `); got != `/prod\.g\d{4}/` {
		t.Fatalf("regex pattern mangled: %q", got)
	}
}

func TestPutRejectsInvalidRegexAndLoadSkipsIt(t *testing.T) {
	store, path := testStore(t)
	if err := store.Put(Mapping{Pattern: `/PROD\.(/`, Local: "/x.cpy"}); err == nil {
		t.Fatal("Put accepted an invalid regex pattern")
	}

	// A state file edited by hand to contain a broken regex loads without the
	// broken entry instead of failing or panicking.
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	broken := `{"mappings":[` +
		`{"pattern":"/BAD\\.(/","local":"/bad.cpy"},` +
		`{"pattern":"/PROD\\..*/","local":"/ok.cpy"}]}`
	if err := os.WriteFile(path, []byte(broken), 0o600); err != nil {
		t.Fatal(err)
	}
	var notes []string
	reloaded := &Store{UserConfigDir: store.UserConfigDir, Diagnostic: func(format string, args ...any) {
		notes = append(notes, format)
	}}
	mappings := reloaded.Mappings()
	if len(mappings) != 1 || mappings[0].Pattern != `/PROD\..*/` {
		t.Fatalf("broken regex not skipped on load: %+v", mappings)
	}
	if len(notes) == 0 {
		t.Fatal("skipping a broken regex produced no diagnostic")
	}
	if _, ok := reloaded.Match("PROD.CUSTOMER.DATA"); !ok {
		t.Fatal("valid regex mapping lost after reload")
	}
}

func TestMatchPrecedenceAcrossAllThreeTiers(t *testing.T) {
	store, _ := testStore(t)
	for _, pattern := range []string{`/PROD\.CUSTOMER\..*/`, "PROD.CUSTOMER.*", "PROD.CUSTOMER.DATA"} {
		if err := store.Put(Mapping{Pattern: pattern, Local: "/" + pattern}); err != nil {
			t.Fatal(err)
		}
	}
	if mapping, ok := store.Match("PROD.CUSTOMER.DATA"); !ok || mapping.Pattern != "PROD.CUSTOMER.DATA" {
		t.Fatalf("exact should beat wildcard and regex: %+v ok=%v", mapping, ok)
	}
	if mapping, ok := store.Match("PROD.CUSTOMER.OLD"); !ok || mapping.Pattern != "PROD.CUSTOMER.*" {
		t.Fatalf("wildcard should beat regex: %+v ok=%v", mapping, ok)
	}
	if _, err := store.Remove("PROD.CUSTOMER.*"); err != nil {
		t.Fatal(err)
	}
	if mapping, ok := store.Match("PROD.CUSTOMER.OLD"); !ok || mapping.Pattern != `/PROD\.CUSTOMER\..*/` {
		t.Fatalf("regex should match once wildcard removed: %+v ok=%v", mapping, ok)
	}

	// Among regexes: longest literal prefix, then lexicographic.
	regexes, _ := testStore(t)
	for _, pattern := range []string{`/PROD\..*/`, `/PROD\.CUSTOMER.*/`, `/PROD\.CUSTOMEQ.*/`} {
		if err := regexes.Put(Mapping{Pattern: pattern, Local: "/" + pattern}); err != nil {
			t.Fatal(err)
		}
	}
	if mapping, ok := regexes.Match("PROD.CUSTOMER.DATA"); !ok || mapping.Pattern != `/PROD\.CUSTOMER.*/` {
		t.Fatalf("regex specificity tie-break wrong: %+v ok=%v", mapping, ok)
	}
}

func TestRegexPersistenceRoundTrip(t *testing.T) {
	store, _ := testStore(t)
	if err := store.Put(Mapping{Pattern: `/PROD\.CUST\.G\d{4}V\d{2}/`, Local: "/cust.cpy", Format: "fixed"}); err != nil {
		t.Fatal(err)
	}
	reloaded := &Store{UserConfigDir: store.UserConfigDir}
	mapping, ok := reloaded.Match("PROD.CUST.G0042V00")
	if !ok {
		t.Fatal("regex mapping not found after reload")
	}
	if mapping.Pattern != `/PROD\.CUST\.G\d{4}V\d{2}/` || mapping.Local != "/cust.cpy" {
		t.Fatalf("regex mapping mangled after reload: %+v", mapping)
	}
}
