package recordset

import (
	"context"
	"fmt"
	"os"
	"testing"

	"github.com/Tannex/cq/internal/query"
)

// stagedGrammar builds expressions from heads × per-record bodies × rests,
// including bodies that error, halt, or yield zero or several outputs, with
// failures placed on and around chunk edges. ids are record positions.
func stagedGrammar(count int) []string {
	edges := []int{0, chunkRecords - 1, chunkRecords, 2*chunkRecords - 1, 2 * chunkRecords, count - 1}
	bodies := []string{
		`.ID`, `.ID % 3`, `empty`, `.ID, .REGION`, `select(.ID % 1000 == 0) | .ID`, `.ITEMS[] | .CODE`,
		`.NAME.x`, `.NAME.x?`, `try error(.ID) catch .`, `label $l | .ITEMS[] | ., break $l`,
		`first(.ITEMS[]) | .QUANTITY`, `limit(0; .)`, `def g: .ID; g`, `. as $__cq_each | $__cq_each.ID`,
		`input_line_number`, `$ENV | length`, `path(.ITEMS[0])`, `getpath(["ID"])`, `.`, `.CUSTOMER`, `.ITEMS`,
		`error(null)`, `if .ID % 2 == 0 then empty else .ID, .ID end`, `[.[]?] | length`, `.ID as [$a] | $a`,
		`. as {ID: $i} ?// [$i] | $i`, `reduce .ITEMS[] as $x (0; . + $x.QUANTITY)`, `"\(.ID)"`, `-.ID`, `.ID // 1`,
		`.MISSING // empty`,
	}
	for _, k := range edges {
		bodies = append(bodies,
			fmt.Sprintf(`if .ID == %d then error("e\(.ID)") else .REGION end`, k),
			fmt.Sprintf(`if .ID == %d then halt else .REGION end`, k),
			fmt.Sprintf(`if .ID == %d then (.ID | halt_error) else .REGION end`, k),
			fmt.Sprintf(`if .ID == %d then error({id: .ID}) elif .ID == %d then error("later") else .REGION end`, k, min(k+chunkRecords, count-1)),
			fmt.Sprintf(`if .ID == %d then halt elif .ID == %d then error("later") else .REGION end`, min(k+1, count-1), count-1),
			fmt.Sprintf(`if .ID >= %d then empty else .ID end`, k),
		)
	}
	heads := []string{`map(%s)`, `[.[] | %s]`, `group_by(%s)`, `sort_by(%s)`, `unique_by(%s)`, `min_by(%s)`, `max_by(%s)`}
	rests := []string{
		``, ` | length`, ` | .[0]`, ` | .[-1]`, ` | .[3:5]`, ` | error("rest")`, ` | halt`, ` | first`,
		` | . as $__cq_each | $__cq_each | length`, ` | path(.[0])`, ` | tojson | length`, ` | limit(3; .[])`,
		` | .[] | if type == "array" then length else . end | select(tostring | test("7$"))`,
		` | del(.[0]) | length`, ` | .[1] = 1 | .[0:2]`, ` | (.[0] | error)?`, ` | try error("x") catch length`,
		` | length as $n | [.[-1], $n]`, ` | .[] | halt`, ` | label $o | .[] | ., break $o`, ` | getpath([2])`,
	}
	// Every body with a few rests, and every rest with a few bodies: the
	// full product is too slow to run by default.
	var exprs []string
	for _, head := range heads {
		for _, body := range bodies {
			for _, rest := range []string{``, ` | length`, ` | .[-1]`, ` | .[] | halt`} {
				exprs = append(exprs, fmt.Sprintf(head, body)+rest)
			}
		}
		for _, body := range []string{`.ID % 3`, `empty`, `.ID, .REGION`, fmt.Sprintf(`if .ID == %d then error("e") else .ID end`, count-1)} {
			for _, rest := range rests {
				exprs = append(exprs, fmt.Sprintf(head, body)+rest)
			}
		}
	}
	return exprs
}

func TestStagedGrammarMatchesSequential(t *testing.T) {
	// The whole grammar takes over a minute per set size; by default one
	// in twenty expressions runs. CQ_STAGED_GRAMMAR=1 runs all of them.
	stride := 20
	if os.Getenv("CQ_STAGED_GRAMMAR") != "" {
		stride = 1
	}
	for _, count := range []int{chunkRecords + 1, 2 * chunkRecords, 20000} {
		view := testView(t, fixedCopybook)
		records := make([][]byte, count)
		for i := range records {
			records[i] = fixedRecord(i)
		}
		set := openSet(t, writeFrames(t, records), view)
		values := make([]any, 0, count)
		for _, data := range records {
			values = append(values, view.Value(data))
		}
		calls := countStagedChunks(t)
		failures := 0
		for i, expr := range stagedGrammar(count) {
			if i%stride != 0 {
				continue
			}
			compiled, err := query.Compile(expr)
			if err != nil {
				continue
			}
			want := referenceOver(compiled, values, 7)
			Workers = func(int) int { return 4 }
			calls.Store(0)
			got := set.Query(context.Background(), compiled, 7)
			if compiled.Staged() != nil && calls.Load() == 0 {
				t.Errorf("%s: staged but not run in chunks", expr)
			}
			if diff := sameResult(got, want); diff != "" {
				Workers = func(int) int { return 1 }
				seq := set.Query(context.Background(), compiled, 7)
				t.Errorf("%s (count=%d, staged=%v): parallel %s; sequential-lazy vs reference: %q", expr, count, compiled.Staged() != nil, diff, sameResult(seq, want))
				if failures++; failures > 30 {
					t.FailNow()
				}
			}
		}
	}
}

// The staged values are lazy records, groups and tables collected on other
// goroutines; the rest of the query must find them the same as the values
// the sequential evaluation builds.
func TestStagedLazyValuesMatchFullDecode(t *testing.T) {
	view := testView(t, fixedCopybook)
	records := make([][]byte, 20000)
	for i := range records {
		records[i] = fixedRecord(i)
	}
	set := openSet(t, writeFrames(t, records), view)
	values := make([]any, len(records))
	for i, data := range records {
		values[i] = view.Value(data)
	}
	calls := countStagedChunks(t)
	for _, expr := range []string{
		`map(.) | .[0] == .[0]`, `map(.) | .[3] == .[5003]`, `map(.) | unique | length`, `map(.CUSTOMER) | group_by(.) | length`,
		`map(.) | sort_by(.ID) | .[0]`, `map(.) | to_entries | .[1]`, `map(.) | tostream | first`, `map(.ITEMS) | flatten | length`,
		`map(.) | .[0:2] | tojson`, `map(.) | path(.[0].ITEMS[1])`, `map(.) | getpath([3, "ITEMS", 0])`,
		`map(.) | del(.[0].ITEMS) | .[0]`, `map(.) | .[0].ITEMS[0].CODE |= "x" | .[0:2]`, `[.[] | .CUSTOMER] | unique`, `map(.) | index(.[5])`,
		`map(.) | add | keys`, `map(.) | .[0] + .[1]`, `map(.) | min | .ID`, `map(.) | sort | .[0].ID`, `map(.) | unique_by(.REGION) | map(.ID)`,
		`map(.) | .[0:50] | INDEX(.ID) | length`, `map(.ITEMS[]) | unique | length`, `map(.ITEMS[0]) | group_by(.CODE) | map(length)`,
		`map(.CUSTOMER) | map(.CITY) | unique`, `map(.) | map(.CUSTOMER) == map(.CUSTOMER)`, `map(.) | .[100].ITEMS | to_entries`,
		`map(.) | map(del(.ITEMS)) | .[9]`, `map(.) | .[0] * .[1]`,
		`map(.) | .[2] | with_entries(select(.value | type == "object"))`, `map(.) | .[5] | [.. | scalars] | length`,
		`group_by(.REGION) | map(.[0].CUSTOMER)`, `sort_by(.CUSTOMER) | .[0:3]`, `unique_by(.ITEMS[0]) | map(.ITEMS)`, `min_by(.ITEMS) | .ITEMS`,
		`map(.ID) | .[0:3] | error`, `map(.ITEMS[0]) | .[0] | error`, `map(.) | .[3] | error`, `map(error(.))`, `group_by(error(.CUSTOMER))`,
	} {
		compiled, err := query.Compile(expr)
		if err != nil {
			t.Fatalf("%s: %v", expr, err)
		}
		if compiled.Staged() == nil {
			t.Errorf("%s is not staged", expr)
			continue
		}
		want := referenceOver(compiled, values, 7)
		for _, workers := range []int{1, 4} {
			Workers = func(int) int { return workers }
			calls.Store(0)
			got := set.Query(context.Background(), compiled, 7)
			if diff := sameResult(got, want); diff != "" {
				t.Errorf("%s (workers=%d): %s", expr, workers, diff)
			}
			if workers > 1 && calls.Load() == 0 {
				t.Errorf("%s: not run in chunks", expr)
			}
		}
	}
}

// referenceOver is reference with the records already decoded, for tests
// that evaluate many expressions over one set.
func referenceOver(compiled *query.Query, values []any, lineCap int) Result {
	var result Result
	err := compiled.RunContext(context.Background(), values, func(out any) error {
		result.Matches++
		if len(result.Lines) < lineCap {
			result.Lines = append(result.Lines, query.Marshal(out))
		}
		return nil
	})
	finish(context.Background(), &result, err)
	return result
}

// Undecodable frames are not records: positions, chunk edges and the keys
// of the _by family must line up with the records that remain.
func TestStagedWithSkippedRecordsMatchesFullDecode(t *testing.T) {
	view := testView(t, variableCopybook)
	var records [][]byte
	for i := range 3*chunkRecords + 100 {
		records = append(records, variableRecord(i))
		if i%chunkRecords == chunkRecords-2 || i%1000 == 7 {
			records = append(records, []byte("000009A 9"))
		}
	}
	set := openSet(t, writeFrames(t, records), view)
	var values []any
	for _, data := range records {
		if view.Check(data) == nil {
			values = append(values, view.Value(data))
		}
	}
	if set.Len() != len(values) || set.Skipped() == 0 {
		t.Fatalf("len=%d skipped=%d, want %d", set.Len(), set.Skipped(), len(values))
	}
	for _, expr := range []string{
		`map(.ID) | length`, `map(.ID) | .[8190:8194]`, `map(.ID) | .[16382:16387]`, `[.[] | .ID] | .[-3:]`,
		`sort_by(.LINES) | map(.ID) | .[0:5]`, `group_by(.CNT) | map([length, .[0].ID, .[-1].ID])`, `unique_by(.LINES[0]) | map(.ID) | .[0:9]`,
		`min_by(.LINES[-1]) | .ID`, `max_by(.ID) | .ID`, `sort_by(.LINES[]) | map(.ID) | .[8190:8194]`, `group_by(.LINES[].SKU) | map(length) | .[0:5]`,
		fmt.Sprintf(`map(if .ID == %d then error("e\(.ID)") else .ID end)`, chunkRecords+3),
		fmt.Sprintf(`sort_by(if .ID == %d then error("e\(.ID)") elif .ID == %d then error("first") else .ID end)`, 2*chunkRecords+3, chunkRecords-1),
	} {
		compiled, err := query.Compile(expr)
		if err != nil {
			t.Fatalf("%s: %v", expr, err)
		}
		want := referenceOver(compiled, values, 7)
		for _, workers := range []int{1, 4} {
			Workers = func(int) int { return workers }
			if diff := sameResult(set.Query(context.Background(), compiled, 7), want); diff != "" {
				t.Errorf("%s (workers=%d): %s", expr, workers, diff)
			}
		}
	}
}

// A later chunk that fails first must not decide the result while an
// earlier chunk, slower and failing too, is still running.
func TestEarliestFailureWinsOverFasterLaterChunk(t *testing.T) {
	view := testView(t, fixedCopybook)
	records := make([][]byte, 3*chunkRecords)
	for i := range records {
		records[i] = fixedRecord(i)
	}
	set := openSet(t, writeFrames(t, records), view)
	values := make([]any, len(records))
	for i, data := range records {
		values[i] = view.Value(data)
	}
	slow := fmt.Sprintf(`(if .ID < %d then ([range(300)] | length) else 0 end)`, chunkRecords)
	for _, expr := range []string{
		fmt.Sprintf(`map(%s as $s | if .ID == %d then error("late in chunk 0") elif .ID == %d then error("chunk 1") else .ID end)`, slow, chunkRecords-1, chunkRecords),
		fmt.Sprintf(`map(%s as $s | if .ID == %d then halt elif .ID == %d then error("chunk 1") else .ID end)`, slow, chunkRecords-1, chunkRecords+5),
		fmt.Sprintf(`sort_by(%s as $s | if .ID == %d then error("chunk 2") elif .ID == %d then halt_error else .ID end)`, slow, 2*chunkRecords, chunkRecords+1),
		fmt.Sprintf(`.[] | %s as $s | if .ID == %d then error("late in chunk 0") elif .ID == %d then error("chunk 1") else select(.ID %% 999 == 0) | .ID end`, slow, chunkRecords-1, chunkRecords),
		fmt.Sprintf(`.[] | %s as $s | if .ID == %d then halt elif .ID == %d then error("chunk 2") else select(.ID %% 999 == 0) | .ID end`, slow, chunkRecords+3, 2*chunkRecords),
	} {
		compiled, err := query.Compile(expr)
		if err != nil {
			t.Fatalf("%s: %v", expr, err)
		}
		if compiled.Staged() == nil && !compiled.RowWise() {
			t.Errorf("%s is not run in chunks", expr)
		}
		want := referenceOver(compiled, values, 5)
		Workers = func(int) int { return 3 }
		if diff := sameResult(set.Query(context.Background(), compiled, 5), want); diff != "" {
			t.Errorf("%s: %s", expr, diff)
		}
	}
}
