package recordset

import (
	"context"
	"testing"

	"github.com/Tannex/cq/internal/query"
)

// The DEPENDING ON table may sit inside a top-level group. That group then
// has a fixed Span in QueryView, but its decoded value depends on the
// counter outside it, and its declared extent reaches past the end of a
// record that holds fewer table entries.
const nestedODOCopybook = `01 NESTED-RECORD.
 05 ZCNT PIC 9.
 05 MID PIC X.
 05 G.
  10 T PIC X OCCURS 0 TO 3 TIMES DEPENDING ON ZCNT.
`

// Fixed-length records (an FB data set with an ODO table): the bytes of G
// are equal, the counters differ. jq compares G first (keys G, MID, ZCNT)
// and G of the first record has more elements, so the first record is the
// greater one; CompareLazy skips G as "bytes equal" and decides on MID.
func TestCompareLazyNestedODOFixedLength(t *testing.T) {
	view := testView(t, nestedODOCopybook)
	records := [][]byte{[]byte("2AXY "), []byte("1BXY ")}
	set := openSet(t, writeFrames(t, records), view)
	Workers = func(int) int { return 1 }
	for _, expr := range []string{
		`.[0] < .[1]`, `.[0] > .[1]`, `sort | map(.MID)`, `[.[1], .[0]] | sort | map(.MID)`,
		`min | .MID`, `max | .MID`, `.[0] == .[1]`,
	} {
		compiled, err := query.Compile(expr)
		if err != nil {
			t.Fatal(err)
		}
		want := reference(t, view, records, expr, 10)
		if diff := sameResult(set.Query(context.Background(), compiled, 10), want); diff != "" {
			t.Errorf("%s: %s", expr, diff)
		}
	}
}

// Variable-length records: the last record is shorter than G's declared
// extent, so CompareLazy slices past the end of the mapping and panics.
func TestCompareLazyNestedODOShortLastRecord(t *testing.T) {
	view := testView(t, nestedODOCopybook)
	records := [][]byte{[]byte("2AXY"), []byte("1BX")}
	set := openSet(t, writeFrames(t, records), view)
	Workers = func(int) int { return 1 }
	for _, expr := range []string{`.[0] < .[1]`, `sort | map(.MID)`, `unique | length`} {
		compiled, err := query.Compile(expr)
		if err != nil {
			t.Fatal(err)
		}
		want := reference(t, view, records, expr, 10)
		if diff := sameResult(set.Query(context.Background(), compiled, 10), want); diff != "" {
			t.Errorf("%s: %s", expr, diff)
		}
	}
}

// Chunk workers run one compiled query concurrently. Constant objects are
// shared by every run of the compiled code, and del/delpaths rewrites every
// map and array it walks after deleting (deleteEmpty), including constant
// ones it did not change: two workers writing the same map is a fatal
// "concurrent map writes" (not recoverable) or, at best, a data race.
func TestConcurrentChunksDoNotWriteSharedConstants(t *testing.T) {
	view := testView(t, fixedCopybook)
	records := make([][]byte, 60000)
	for i := range records {
		records[i] = fixedRecord(i)
	}
	set := openSet(t, writeFrames(t, records), view)
	for _, expr := range []string{
		`.[] | {a: {b: 1, c: [1, {d: 2}]}} | del(.x) | .a.b`,
		`.[] | [{b: 1}, [2]] | del(.[5]) | length`,
	} {
		compiled, err := query.Compile(expr)
		if err != nil {
			t.Fatal(err)
		}
		want := reference(t, view, records, expr, 10)
		Workers = func(int) int { return 8 }
		if diff := sameResult(set.Query(context.Background(), compiled, 10), want); diff != "" {
			t.Errorf("%s: %s", expr, diff)
		}
	}
}
