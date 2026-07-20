package favorites

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func testStore(t *testing.T) (*Store, string) {
	t.Helper()
	dir := t.TempDir()
	now := time.Date(2026, 7, 19, 12, 0, 0, 0, time.UTC)
	store := &Store{
		UserConfigDir: func() (string, error) { return dir, nil },
		Now:           func() time.Time { return now },
	}
	return store, filepath.Join(dir, "cq", StateFileName)
}

func TestToggleAddsAndRemovesExactFavorites(t *testing.T) {
	store, _ := testStore(t)

	added, err := store.Toggle("", "a.customer.data")
	if err != nil || !added {
		t.Fatalf("toggle add = %v, %v", added, err)
	}
	if !store.Has("", "A.CUSTOMER.DATA") || !store.Matches("", "A.CUSTOMER.DATA") {
		t.Fatal("favorite not stored under uppercase exact name")
	}

	added, err = store.Toggle("", "A.CUSTOMER.DATA")
	if err != nil || added {
		t.Fatalf("toggle remove = %v, %v", added, err)
	}
	if store.Has("", "A.CUSTOMER.DATA") {
		t.Fatal("favorite not removed by second toggle")
	}

	if _, err := store.Toggle("", "  "); err == nil {
		t.Fatal("empty name toggled without error")
	}
}

func TestWildcardFavoritesMatchWithoutBeingMutated(t *testing.T) {
	store, _ := testStore(t)
	if _, err := store.Toggle("", "PROD.CUSTOMER.*"); err != nil {
		t.Fatal(err)
	}

	if !store.Matches("", "PROD.CUSTOMER.G0042V00") {
		t.Fatal("wildcard favorite did not match family member")
	}
	if store.Has("", "PROD.CUSTOMER.G0042V00") {
		t.Fatal("wildcard match reported as exact favorite")
	}

	// Toggling a covered name adds an exact entry; the wildcard survives.
	if added, err := store.Toggle("", "PROD.CUSTOMER.G0042V00"); err != nil || !added {
		t.Fatalf("toggle covered name = %v, %v", added, err)
	}
	if !store.Has("", "PROD.CUSTOMER.*") || !store.Has("", "PROD.CUSTOMER.G0042V00") {
		t.Fatalf("favorites after toggle = %#v", store.Favorites(""))
	}

	if !store.Matches("", "PROD.CUSTOMER.X") || store.Matches("", "PROD.ORDER.X") {
		t.Fatal("wildcard matching semantics wrong")
	}
}

func TestPersistenceRoundTripAndUnknownFields(t *testing.T) {
	store, path := testStore(t)
	if _, err := store.Toggle("", "A.ONE"); err != nil {
		t.Fatal(err)
	}
	if err := store.SetNote("", "A.ONE", "monthly billing extract"); err != nil {
		t.Fatal(err)
	}

	reloaded := &Store{UserConfigDir: store.UserConfigDir}
	favorites := reloaded.Favorites("")
	if len(favorites) != 1 || favorites[0].Pattern != "A.ONE" || favorites[0].Note != "monthly billing extract" {
		t.Fatalf("reloaded favorites = %#v", favorites)
	}

	// Unknown fields are tolerated for forward compatibility.
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	augmented := strings.Replace(string(b), "\"favorites\": [", "\"future\": true,\n  \"favorites\": [", 1)
	if err := os.WriteFile(path, []byte(augmented), 0o600); err != nil {
		t.Fatal(err)
	}
	again := &Store{UserConfigDir: store.UserConfigDir}
	if got := again.Favorites(""); len(got) != 1 {
		t.Fatalf("favorites with unknown fields = %#v", got)
	}
}

func TestFavoritesOrderedMostRecentlyUsed(t *testing.T) {
	store, _ := testStore(t)
	now := time.Date(2026, 7, 19, 12, 0, 0, 0, time.UTC)
	store.Now = func() time.Time { return now }
	for _, name := range []string{"A.ONE", "B.TWO", "C.THREE"} {
		if _, err := store.Toggle("", name); err != nil {
			t.Fatal(err)
		}
	}
	now = now.Add(time.Hour)
	if err := store.Touch("", "B.TWO"); err != nil {
		t.Fatal(err)
	}

	favorites := store.Favorites("")
	if favorites[0].Pattern != "B.TWO" {
		t.Fatalf("MRU order = %#v", favorites)
	}
	if favorites[1].Pattern != "A.ONE" || favorites[2].Pattern != "C.THREE" {
		t.Fatalf("tie order not lexicographic: %#v", favorites)
	}
}

func TestCorruptFileDegradesToReadOnly(t *testing.T) {
	store, path := testStore(t)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}

	if got := store.Favorites(""); len(got) != 0 {
		t.Fatalf("corrupt store returned favorites: %#v", got)
	}
	if _, err := store.Toggle("", "A.ONE"); err == nil {
		t.Fatal("corrupt store accepted a write")
	}
	b, err := os.ReadFile(path)
	if err != nil || string(b) != "{not json" {
		t.Fatalf("corrupt file was rewritten: %q, %v", b, err)
	}
}

func TestMissingFileBehavesAsEmpty(t *testing.T) {
	store, _ := testStore(t)
	if got := store.Favorites(""); len(got) != 0 {
		t.Fatalf("missing file returned favorites: %#v", got)
	}
	if store.Matches("", "ANY.NAME") {
		t.Fatal("empty store matched a name")
	}
	if removed, err := store.Remove("", "ANY.NAME"); removed || err != nil {
		t.Fatalf("remove on empty store = %v, %v", removed, err)
	}
}

func TestSetNoteRequiresExistingFavorite(t *testing.T) {
	store, _ := testStore(t)
	if err := store.SetNote("", "NOPE", "note"); err == nil {
		t.Fatal("note stored for missing favorite")
	}
}

func TestRegexFavoritesSurviveLoadAndMatch(t *testing.T) {
	store, path := testStore(t)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	state := `{"favorites":[` +
		`{"pattern":"/prod\\.g\\d{4}v\\d{2}/","note":"generations"},` +
		`{"pattern":"/BAD\\.(/","note":"broken"}]}`
	if err := os.WriteFile(path, []byte(state), 0o600); err != nil {
		t.Fatal(err)
	}

	favorites := store.Favorites("")
	if len(favorites) != 1 {
		t.Fatalf("broken regex favorite not skipped: %+v", favorites)
	}
	// Case is preserved so escape classes like \d keep their meaning.
	if favorites[0].Pattern != `/prod\.g\d{4}v\d{2}/` {
		t.Fatalf("regex favorite mangled on load: %q", favorites[0].Pattern)
	}
	if !favorites[0].Wildcard() {
		t.Fatal("regex favorite not treated as a pattern entry")
	}
	if !store.Matches("", "PROD.G0042V00") {
		t.Fatal("regex favorite did not mark a matching data set")
	}
	if store.Matches("", "PROD.G42V00") {
		t.Fatal("regex favorite matched a non-conforming data set")
	}
}

func TestAddStoresPatternsAndRejectsDuplicatesAndBadRegex(t *testing.T) {
	store, _ := testStore(t)
	if err := store.Add("", " prod.cust.* "); err != nil {
		t.Fatal(err)
	}
	if err := store.Add("", `/^x\d+/`); err != nil {
		t.Fatal(err)
	}
	if err := store.Add("", "PROD.CUST.*"); err == nil {
		t.Fatal("duplicate pattern accepted")
	}
	if err := store.Add("", "/bad(/"); err == nil {
		t.Fatal("invalid regex accepted")
	}
	if err := store.Add("", "  "); err == nil {
		t.Fatal("empty pattern accepted")
	}
	favorites := store.Favorites("")
	if len(favorites) != 2 {
		t.Fatalf("favorites = %#v", favorites)
	}
	// Wildcards normalize to upper case; regexes keep their case.
	reloaded := &Store{UserConfigDir: store.UserConfigDir}
	patterns := []string{reloaded.Favorites("")[0].Pattern, reloaded.Favorites("")[1].Pattern}
	if patterns[0] != `/^x\d+/` && patterns[1] != `/^x\d+/` {
		t.Fatalf("regex case not preserved: %#v", patterns)
	}
	if patterns[0] != "PROD.CUST.*" && patterns[1] != "PROD.CUST.*" {
		t.Fatalf("wildcard not upper-cased: %#v", patterns)
	}
}

func TestRenamePreservesNoteAndTimestamps(t *testing.T) {
	store, _ := testStore(t)
	if err := store.Add("", "A.ONE"); err != nil {
		t.Fatal(err)
	}
	if err := store.SetNote("", "A.ONE", "keep"); err != nil {
		t.Fatal(err)
	}
	created := store.Favorites("")[0].Created
	if err := store.Rename("", "A.ONE", "a.one.*"); err != nil {
		t.Fatal(err)
	}
	favorite := store.Favorites("")[0]
	if favorite.Pattern != "A.ONE.*" || favorite.Note != "keep" || !favorite.Created.Equal(created) {
		t.Fatalf("renamed favorite = %#v", favorite)
	}
	if err := store.Rename("", "MISSING", "X"); err == nil {
		t.Fatal("rename of a missing favorite accepted")
	}
	if err := store.Add("", "B.TWO"); err != nil {
		t.Fatal(err)
	}
	if err := store.Rename("", "B.TWO", "A.ONE.*"); err == nil {
		t.Fatal("rename onto an existing pattern accepted")
	}
}

func TestFavoritesAreKeyedByProfile(t *testing.T) {
	store, _ := testStore(t)
	if err := store.Add("dev", "DEV.ONLY"); err != nil {
		t.Fatal(err)
	}
	if err := store.Add("prod", "PROD.ONLY"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Toggle("", "SHARED.LEGACY"); err != nil {
		t.Fatal(err)
	}

	dev := store.Favorites("dev")
	if len(dev) != 2 {
		t.Fatalf("dev favorites = %#v", dev)
	}
	for _, favorite := range dev {
		if favorite.Pattern == "PROD.ONLY" {
			t.Fatal("prod favorite leaked into the dev view")
		}
	}
	if !store.Matches("dev", "DEV.ONLY") || store.Matches("dev", "PROD.ONLY") {
		t.Fatal("Matches ignored the profile view")
	}
	// Shared (empty-profile) entries are visible to every profile.
	if !store.Matches("dev", "SHARED.LEGACY") || !store.Matches("prod", "SHARED.LEGACY") {
		t.Fatal("shared favorite not visible across profiles")
	}
}

func TestSamePatternCoexistsAcrossProfiles(t *testing.T) {
	store, _ := testStore(t)
	if err := store.Add("dev", "APP.MASTER"); err != nil {
		t.Fatal(err)
	}
	if err := store.Add("prod", "APP.MASTER"); err != nil {
		t.Fatal(err)
	}
	if err := store.Add("prod", "APP.MASTER"); err == nil {
		t.Fatal("duplicate within one profile accepted")
	}
	if err := store.SetNote("dev", "APP.MASTER", "dev note"); err != nil {
		t.Fatal(err)
	}
	prod := store.Favorites("prod")
	if len(prod) != 1 || prod[0].Note != "" {
		t.Fatalf("prod entry affected by dev note: %#v", prod)
	}
	if removed, err := store.Remove("dev", "APP.MASTER"); err != nil || !removed {
		t.Fatalf("remove dev entry: removed=%v err=%v", removed, err)
	}
	if !store.Has("prod", "APP.MASTER") {
		t.Fatal("removing the dev entry deleted prod's")
	}
}

func TestProfileKeyingSurvivesReload(t *testing.T) {
	store, _ := testStore(t)
	if err := store.Add("dev", "DEV.DATA"); err != nil {
		t.Fatal(err)
	}
	reloaded := &Store{UserConfigDir: store.UserConfigDir}
	if !reloaded.Has("dev", "DEV.DATA") || reloaded.Has("prod", "DEV.DATA") {
		t.Fatalf("profile keying lost on reload: %#v", reloaded.Favorites("dev"))
	}
}
