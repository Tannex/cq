package events

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func testStore(t *testing.T) (*Store, string) {
	t.Helper()
	dir := t.TempDir()
	clock := time.Date(2026, 7, 19, 12, 0, 0, 0, time.UTC)
	store := &Store{
		UserConfigDir: func() (string, error) { return dir, nil },
		Now: func() time.Time {
			clock = clock.Add(time.Second)
			return clock
		},
	}
	return store, filepath.Join(dir, "cq", StateFileName)
}

func TestAppendsAreOneLineEach(t *testing.T) {
	store, path := testStore(t)
	store.RecordOpen("A.CUSTOMER.DATA")
	store.RecordQuery(`.NAME`, true)
	store.RecordQuery(`bad(`, false)

	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimRight(string(b), "\n"), "\n")
	if len(lines) != 3 {
		t.Fatalf("lines = %d:\n%s", len(lines), b)
	}
	if !strings.Contains(lines[0], `"kind":"open"`) || !strings.Contains(lines[0], "A.CUSTOMER.DATA") {
		t.Fatalf("open line = %q", lines[0])
	}
	if !strings.Contains(lines[1], `"outcome":"ok"`) || !strings.Contains(lines[2], `"outcome":"error"`) {
		t.Fatalf("query outcomes wrong:\n%q\n%q", lines[1], lines[2])
	}
	for _, forbidden := range []string{"copybook", "record"} {
		if strings.Contains(strings.ToLower(string(b)), forbidden) {
			t.Fatalf("log leaks %q:\n%s", forbidden, b)
		}
	}
}

func TestEventsReturnsInOrderAndSkipsCorruptLines(t *testing.T) {
	store, path := testStore(t)
	store.RecordOpen("FIRST")
	store.RecordOpen("SECOND")
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString("{corrupt\n"); err != nil {
		t.Fatal(err)
	}
	f.Close()
	store.RecordOpen("THIRD")

	events := store.Events()
	if len(events) != 3 {
		t.Fatalf("events = %#v", events)
	}
	if events[0].Name != "FIRST" || events[2].Name != "THIRD" {
		t.Fatalf("order wrong: %#v", events)
	}
	if !events[1].Time.After(events[0].Time) {
		t.Fatal("timestamps not monotonic")
	}
}

func TestCompactionKeepsTheNewestThousand(t *testing.T) {
	store, path := testStore(t)
	for i := range 2001 {
		store.RecordOpen(fmt.Sprintf("DS%04d", i))
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Count(string(b), "\n")
	if lines != 1000 {
		t.Fatalf("post-compaction lines = %d, want 1000", lines)
	}
	events := store.Events()
	if len(events) != 1000 || events[0].Name != "DS1001" || events[999].Name != "DS2000" {
		t.Fatalf("kept window wrong: first=%v last=%v n=%d", events[0].Name, events[len(events)-1].Name, len(events))
	}
}

func TestFailuresDegradeSilently(t *testing.T) {
	var diags []string
	store := &Store{
		UserConfigDir: func() (string, error) { return "", os.ErrPermission },
		Diagnostic:    func(format string, args ...any) { diags = append(diags, fmt.Sprintf(format, args...)) },
	}
	store.RecordOpen("A.B") // must not panic or error
	if store.Events() != nil {
		t.Fatal("events from a broken store should be empty")
	}
	if len(diags) == 0 {
		t.Fatal("failures should reach the diagnostic")
	}
}
