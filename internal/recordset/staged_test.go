package recordset

import (
	"context"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/Tannex/cq/internal/query"
)

func fixedSet(t *testing.T, count int) *Set {
	t.Helper()
	view := testView(t, fixedCopybook)
	records := make([][]byte, count)
	for i := range records {
		records[i] = fixedRecord(i)
	}
	return openSet(t, writeFrames(t, records), view)
}

func countStagedChunks(t *testing.T) *atomic.Int64 {
	t.Helper()
	var calls atomic.Int64
	onStagedChunk = func() { calls.Add(1) }
	t.Cleanup(func() { onStagedChunk = func() {} })
	return &calls
}

// A staged shape that quietly fell back to one thread would still pass the
// equivalence tests, so the parallel path is checked to be taken, and
// taken only for the shapes it claims.
func TestStagedShapesRunInChunks(t *testing.T) {
	set := fixedSet(t, 20000)
	chunks := int64((set.Len() + chunkRecords - 1) / chunkRecords)
	calls := countStagedChunks(t)
	for expr, staged := range map[string]bool{
		`map(.ID)`:                              true,
		`map(.ID) | add`:                        true,
		`[.[] | .ID]`:                           true,
		`[.[] | .ID] | length`:                  true,
		`group_by(.REGION) | length`:            true,
		`sort_by(.AMOUNT)`:                      true,
		`sort_by(.AMOUNT) | .[0].ID`:            true,
		`unique_by(.NAME) | length`:             true,
		`min_by(.BALANCE)`:                      true,
		`max_by(.BALANCE) | .ID`:                true,
		`.[] | .ID`:                             false, // row-wise instead
		`def map(f): [.[] | f]; map(.ID)`:       false,
		`.[] as $x | [$x.ID]`:                   false,
		`[.[] as $x | $x.ID]`:                   false,
		`.[0:5] | map(.ID)`:                     false,
		`{a: map(.ID)}`:                         false,
		`first(map(.ID)[])`:                     false,
		`map(.ID)[0]`:                           false,
		`map(.ID) as $ids | $ids | length`:      false,
		`map(.ID), 1`:                           false,
		`[.[]]`:                                 false,
		`sort`:                                  false,
		`limit(1; sort_by(.ID)) | length`:       false,
		`. as $__cq_each | map(.ID) | length`:   false,
		`try group_by(.REGION) catch "nothing"`: false,
	} {
		compiled, err := query.Compile(expr)
		if err != nil {
			t.Fatalf("%s: %v", expr, err)
		}
		Workers = func(int) int { return 4 }
		calls.Store(0)
		if got := set.Query(context.Background(), compiled, 10); got.Err != nil {
			t.Fatalf("%s: %v", expr, got.Err)
		}
		want := int64(0)
		if staged {
			want = chunks
		}
		if got := calls.Load(); got != want {
			t.Errorf("%s: %d staged chunks evaluated, want %d", expr, got, want)
		}
	}

	// One worker, or a set no larger than a chunk, stays sequential.
	compiled, _ := query.Compile(`map(.ID) | add`)
	Workers = func(int) int { return 1 }
	calls.Store(0)
	set.Query(context.Background(), compiled, 10)
	small := fixedSet(t, chunkRecords)
	Workers = func(int) int { return 4 }
	small.Query(context.Background(), compiled, 10)
	if got := calls.Load(); got != 0 {
		t.Errorf("%d staged chunks evaluated on the sequential path", got)
	}
}

// Cancelling while the per-element stage runs ends the query as cancelled,
// without the rest of the expression producing anything.
func TestStagedQueryCancelledMidway(t *testing.T) {
	set := fixedSet(t, 100000)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var calls atomic.Int64
	onStagedChunk = func() {
		if calls.Add(1) == 2 {
			cancel()
		}
	}
	t.Cleanup(func() { onStagedChunk = func() {} })
	Workers = func(int) int { return 2 }
	compiled, _ := query.Compile(`sort_by(.AMOUNT) | .[0].ID`)
	got := set.Query(ctx, compiled, 10)
	if !got.Canceled || got.Err != nil || got.Matches != 0 {
		t.Fatalf("canceled=%v err=%v matches=%d, want a cancellation", got.Canceled, got.Err, got.Matches)
	}
}

// A fault in the engine, in either stage, becomes the query's error rather
// than ending the process. Reading a record of a closed set is such a fault.
func TestStagedQueryReportsEngineFaults(t *testing.T) {
	set := fixedSet(t, 20000)
	if err := set.Close(); err != nil {
		t.Fatal(err)
	}
	Workers = func(int) int { return 4 }
	// The first reads records in Each; the second only in Then, as the
	// keys do not depend on the record.
	for _, expr := range []string{`map(.ID)`, `group_by(1) | .[0][0].ID`} {
		compiled, _ := query.Compile(expr)
		if compiled.Staged() == nil {
			t.Fatalf("%s is not staged", expr)
		}
		got := set.Query(context.Background(), compiled, 10)
		if got.Err == nil || !strings.Contains(got.Err.Error(), "query engine fault") {
			t.Errorf("%s over a closed set: err=%v lines=%v, want an engine fault", expr, got.Err, got.Lines)
		}
	}
}
