package recordset

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/Tannex/cq/internal/copybook"
	"github.com/Tannex/cq/internal/decode"
	"github.com/Tannex/cq/internal/layout"
	"github.com/Tannex/cq/internal/query"
	"github.com/Tannex/cq/internal/record"
)

const fixedCopybook = `01 TRANSACTION-RECORD.
 05 ID PIC 9(8).
 05 NAME PIC X(12).
 05 REGION PIC X(4).
 05 STATUS PIC X.
 05 AMOUNT PIC 9(5)V99.
 05 BALANCE PIC S9(5)V99 COMP-3.
 05 CUSTOMER.
  10 CITY PIC X(10).
  10 TIER PIC 9(2).
 05 ITEMS OCCURS 2 TIMES.
  10 CODE PIC X(4).
  10 QUANTITY PIC 9(3).
`

const variableCopybook = `01 ORDER-RECORD.
 05 ID PIC 9(6).
 05 KIND PIC X(2).
 05 CNT PIC 9.
 05 LINES OCCURS 0 TO 3 TIMES DEPENDING ON CNT.
  10 SKU PIC X(3).
  10 QTY PIC 9(2).
`

func testView(t testing.TB, source string) *record.QueryView {
	t.Helper()
	items, err := copybook.Parse(source, copybook.FormatFree)
	if err != nil {
		t.Fatal(err)
	}
	recs, err := layout.Build(items)
	if err != nil {
		t.Fatal(err)
	}
	cm, err := decode.Codepage("latin1")
	if err != nil {
		t.Fatal(err)
	}
	decoder, err := record.NewDecoder(recs[0], cm)
	if err != nil {
		t.Fatal(err)
	}
	return decoder.QueryView()
}

func fixedRecord(i int) []byte {
	status := 'I'
	if i%10 == 0 {
		status = 'A'
	}
	// Records repeat every 5000 ids apart from the id itself, so sorts,
	// groups and uniques have real ties to keep stable.
	k := i % 5000
	data := fmt.Appendf(nil, "%08d%-12sR%03d%c%07d", i, fmt.Sprintf("Cust %05d", k), k%4, status, k%700*125)
	data = append(data, 0x00, 0x12, byte(k%10)<<4|byte(k%7), 0x4c|byte(k%2)) // 4 bytes: S9(5)V99 COMP-3
	data = fmt.Appendf(data, "%-10s%02d", []string{"Oslo", "Bergen", "Aarhus"}[k%3], k%5)
	for j := range 2 {
		data = fmt.Appendf(data, "IT%02d%03d", (k+j)%20, (k*(j+1))%1000)
	}
	return data
}

func variableRecord(i int) []byte {
	count := i % 4
	data := fmt.Appendf(nil, "%06d%-2s%d", i, []string{"A", "B", "C"}[i%3], count)
	for j := range count {
		data = fmt.Appendf(data, "S%02d%02d", (i+j)%50, j+1)
	}
	return data
}

func writeFrames(t testing.TB, records [][]byte) string {
	t.Helper()
	var file bytes.Buffer
	for _, data := range records {
		var header [4]byte
		binary.BigEndian.PutUint32(header[:], uint32(len(data)))
		file.Write(header[:])
		file.Write(data)
	}
	path := filepath.Join(t.TempDir(), "frames.records")
	if err := os.WriteFile(path, file.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func openSet(t testing.TB, path string, view *record.QueryView) *Set {
	t.Helper()
	set, err := Open(context.Background(), path, view)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = set.Close() })
	return set
}

// reference evaluates the expression the way the console did before lazy
// records: over every decodable record fully decoded into one array.
func reference(t testing.TB, view *record.QueryView, records [][]byte, expr string, lineCap int) Result {
	t.Helper()
	compiled, err := query.Compile(expr)
	if err != nil {
		t.Fatalf("%s: %v", expr, err)
	}
	var values []any
	for _, data := range records {
		if view.Check(data) == nil {
			values = append(values, view.Value(data))
		}
	}
	var result Result
	err = compiled.RunContext(context.Background(), values, func(out any) error {
		result.Matches++
		if len(result.Lines) < lineCap {
			result.Lines = append(result.Lines, query.Marshal(out))
		}
		return nil
	})
	finish(context.Background(), &result, err)
	return result
}

func sameResult(got, want Result) string {
	if got.Matches != want.Matches {
		return fmt.Sprintf("matches = %d, want %d", got.Matches, want.Matches)
	}
	if (got.Err == nil) != (want.Err == nil) || got.Err != nil && got.Err.Error() != want.Err.Error() {
		return fmt.Sprintf("error = %v, want %v", got.Err, want.Err)
	}
	if got.Canceled != want.Canceled {
		return fmt.Sprintf("canceled = %v, want %v", got.Canceled, want.Canceled)
	}
	if !reflect.DeepEqual(got.Lines, want.Lines) {
		for i := range min(len(got.Lines), len(want.Lines)) {
			if got.Lines[i] != want.Lines[i] {
				return fmt.Sprintf("line %d = %s, want %s", i, got.Lines[i], want.Lines[i])
			}
		}
		return fmt.Sprintf("%d lines, want %d", len(got.Lines), len(want.Lines))
	}
	return ""
}

var fixedExpressions = []string{
	`length`,
	`.[]`,
	`.[] | select(.STATUS == "A")`,
	`.[] | select(.STATUS == "A") | {ID, NAME, AMOUNT, BALANCE}`,
	`.[] | select(.ID == 12345)`,
	`.[] | select(.AMOUNT > 800) | .ID`,
	`.[] | select(.BALANCE < 0) | [.ID, .BALANCE]`,
	`.[] | select(.CUSTOMER.TIER == 3 and .ITEMS[1].QUANTITY > 990) | .ID`,
	`.[] | select(.NAME | test("0000[12]$")) | "\(.ID) \(.NAME) \(.CUSTOMER.CITY)"`,
	`.[] | select(.MISSING != null)`,
	`.[] | select(.ID % 4001 == 0) | to_entries | length`,
	`.[] | select(.ID % 4001 == 0) | keys`,
	`.[] | select(.ID % 4001 == 0) | del(.ITEMS) | .NEW = .ID + 1`,
	`.[] | select(.ID % 4001 == 0) | [., .] | unique | length`,
	`.[] | select(.ID % 4001 == 0) | tojson | fromjson | .CUSTOMER`,
	`.[] | select(.ID % 4001 == 0) | [paths] | length`,
	`.[] | select(.ID % 4001 == 0) | with_entries(select(.value | type == "string"))`,
	`.[] | .ITEMS[] | select(.QUANTITY == 999) | .CODE`,
	`.[] | if .ID == 15000 then error("boom at \(.ID)") else empty end`,
	`.[] | if .ID > 15000 then error("late") elif .ID % 5000 == 0 then .ID else empty end`,
	`.[] | if .ID == 9000 then halt else select(.ID % 3000 == 0) | .ID end`,
	`.[] | if .ID == 9000 then halt_error else empty end`,
	`.[] | .NAME | ascii_downcase | select(startswith("cust 0000"))`,
	`.[] | try (if .ID % 6000 == 0 then error(.) else empty end) catch .ID`,
	`.[] | .ID as $id | select($id % 7000 == 0) | [$id, (.NAME | length)]`,
	`.[] | reduce .ITEMS[] as $i (0; . + $i.QUANTITY) | select(. > 1990)`,
	`.[] | label $done | (.ITEMS[] | if .QUANTITY > 995 then ., break $done else empty end)`,
	`.[] | first(.ITEMS[] | select(.QUANTITY > 998)) | .CODE`,
	`.[] | limit(1; .ITEMS[] | select(.QUANTITY > 998))`,
	`.[0]`, `.[-1].ID`, `.[100:103] | map(.ID)`, `.[:2]`, `.[19998:] | map(.ID)`, `first`, `last | .NAME`, `.[5].CUSTOMER.CITY`,
	`first(.[] | select(.STATUS == "A" and .ID > 100)) | .ID`,
	`limit(3; .[] | select(.REGION == "R002")) | .ID`,
	`[limit(3; .[])] | map(.ID)`,
	`isempty(.[] | select(.ID == 77))`,
	`any(.[]; .ID == 19999)`, `all(.[]; .ID < 20000)`,
	`map(.REGION) | unique`,
	`map(.AMOUNT) | add`,
	`[.[] | .BALANCE] | min, max`,
	`map(select(.STATUS == "A")) | length`,
	`map(.CUSTOMER.CITY) | unique | length`,
	`group_by(.REGION) | map({region: .[0].REGION, count: length})`,
	`group_by(.CUSTOMER.CITY) | map([.[0].CUSTOMER.CITY, length, (map(.AMOUNT) | add)])`,
	`group_by(.STATUS) | map(.[0:2] | map(.ID))`,
	`sort_by(.AMOUNT) | .[0:3] | map(.ID)`,
	`sort_by(.AMOUNT, .NAME) | .[-3:] | map([.ID, .AMOUNT])`,
	`sort_by(.NAME) | .[0].ID`,
	`sort_by(.CUSTOMER) | .[0:2] | map(.ID)`,
	`sort | .[0:2] | map(.ID)`,
	`sort | .[0]`,
	`unique | length`,
	`unique_by(.NAME) | length`,
	`unique_by(.CUSTOMER) | map(.CUSTOMER.TIER) | length`,
	`unique_by(.REGION) | map(.ID)`,
	`min_by(.AMOUNT) | .ID`, `max_by(.BALANCE) | [.ID, .BALANCE]`, `min | .ID`, `max | .ID`,
	`reduce .[] as $r ({}; .[$r.REGION] += 1)`,
	`reduce .[] as $r (0; . + $r.AMOUNT)`,
	`[foreach .[] as $r (0; . + 1; select(. % 6000 == 0) | [., $r.ID])]`,
	`. as $all | .[100] | [.ID, ($all | length)]`,
	`.[3] == .[3]`, `.[3] == .[5003]`, `.[3] < .[4]`, `.[3] == (.[3] | tojson | fromjson)`, `[.[3], .[5003]] | unique | length`,
	`[.[1], .[5001], .[1]] | group_by(.) | map(length)`,
	`index(.[7])`, `[.[] | select(.ID < 3)] | indices(.[1])`, `.[0:3] | contains([{"REGION": "R001"}])`,
	`.[0:3] | map(has("NAME"))`, `.[0:2] | add | keys | length`, `.[0:2] | map(keys) | add | unique | length`,
	`.[0] + .[1] | .ID`, `.[0] * {CUSTOMER: {EXTRA: 1}} | .CUSTOMER`, `.[0:2] - .[0:1] | map(.ID)`, `.[0:4] | INDEX(.ID) | keys`,
	`.[0:3] | map(.NAME |= ascii_downcase) | map(.NAME)`, `.[0].NAME |= "x" | .[0:2] | map(.NAME)`, `del(.[1:]) | map(.ID)`,
	`.[0:2] | to_entries | map(.value.ID)`, `.[0:2] | tostream | select(length == 2) | .[0] | join("/")?`, `path(.[2].CUSTOMER.CITY)`,
	`[paths] | length`, `.[0:2] | [paths(scalars)] | length`, `getpath([4, "ITEMS", 1, "CODE"])`, `.[0:2] | walk(if type == "number" then 0 else . end) | .[1].ID`,
	`.[0:3] | flatten | length`, `[.[0:2], .[2:4]] | transpose | map(map(.ID))`, `.[0:3] | @json | length`, `.[0] | @text | length`,
	`.[0:2] | map(tostring | length)`, `.[0:3] | map(type)`, `type`, `.[0] | type`, `.[0:2] | reverse | map(.ID)`, `tojson | length`,
	`.[0] | error`, `.[0] | .[0]`, `.[0] | implode`, `.[0] - 1`, `.NAME`, `.[0] | .NAME.x`, `.[] | .NAME.x`, `.[] | error(.ID)`, `keys | length`,
	`.["a"]`, `.[0] | has(0)`, `.[0] | join(",")`, `.[0] | @csv`, `ascii_downcase`, `.[0] | test("a")`, `.[] | {(.CUSTOMER): 1}`, `.[true]`,
}

func TestSetMatchesFullDecode(t *testing.T) {
	view := testView(t, fixedCopybook)
	records := make([][]byte, 20000)
	for i := range records {
		records[i] = fixedRecord(i)
	}
	set := openSet(t, writeFrames(t, records), view)
	if set.Len() != len(records) || set.Skipped() != 0 || set.offsets != nil {
		t.Fatalf("fixed set: len=%d skipped=%d explicit offsets=%v", set.Len(), set.Skipped(), set.offsets != nil)
	}
	for _, lineCap := range []int{1 << 20, 7} {
		for _, expr := range fixedExpressions {
			compiled, err := query.Compile(expr)
			if err != nil {
				t.Fatalf("%s: %v", expr, err)
			}
			want := reference(t, view, records, expr, lineCap)
			for _, workers := range []int{1, 4} {
				Workers = func(int) int { return workers }
				got := set.Query(context.Background(), compiled, lineCap)
				if diff := sameResult(got, want); diff != "" {
					t.Errorf("%s (workers=%d, cap=%d): %s", expr, workers, lineCap, diff)
				}
			}
		}
	}
}

var variableExpressions = []string{
	`length`, `.[]`, `.[] | select(.CNT == 3) | .LINES | map(.QTY) | add`, `.[] | select(.KIND == "B") | {ID, n: (.LINES | length)}`,
	`.[] | .LINES[] | select(.SKU == "S07") | .QTY`, `map(.LINES | length) | add`, `group_by(.CNT) | map(length)`, `unique_by(.LINES) | length`,
	`sort_by(.LINES) | .[0:2] | map(.ID)`, `sort | .[-1].ID`, `unique | length`, `.[3] == .[3]`, `[.[1], .[2]] | sort | map(.ID)`,
	`map(select(.LINES | length == 0)) | length`, `.[] | select(.ID == 20123)`, `.[20000:] | map(.ID) | length`, `min_by(.LINES) | .ID`,
	`.[] | if .ID == 17001 then error("stop") else empty end`, `reduce .[] as $r ({}; .[$r.KIND] += ($r.LINES | length))`,
}

func TestVariableLengthSetSkipsUndecodableRecords(t *testing.T) {
	view := testView(t, variableCopybook)
	var records [][]byte
	for i := range 21000 {
		records = append(records, variableRecord(i))
		switch i % 2500 {
		case 7:
			records = append(records, []byte("short")) // shorter than the fixed prefix
		case 9:
			records = append(records, []byte("000009A 9")) // counter is not within 0..3
		case 11:
			records = append(records, []byte("000011A 3S0101")) // counter promises more lines than follow
		}
	}
	set := openSet(t, writeFrames(t, records), view)
	if set.Frames() != len(records) || set.Skipped() != 27 || set.Len() != len(records)-27 {
		t.Fatalf("frames=%d skipped=%d len=%d, want %d frames with 27 skipped", set.Frames(), set.Skipped(), set.Len(), len(records))
	}
	for _, expr := range variableExpressions {
		compiled, err := query.Compile(expr)
		if err != nil {
			t.Fatalf("%s: %v", expr, err)
		}
		want := reference(t, view, records, expr, 1<<20)
		for _, workers := range []int{1, 3} {
			Workers = func(int) int { return workers }
			if diff := sameResult(set.Query(context.Background(), compiled, 1<<20), want); diff != "" {
				t.Errorf("%s (workers=%d): %s", expr, workers, diff)
			}
		}
	}
}

// A file whose frames all share a size but where one record cannot be
// decoded has to leave the implicit fixed-stride addressing.
func TestUniformFramesWithAnUndecodableRecord(t *testing.T) {
	view := testView(t, variableCopybook)
	records := [][]byte{[]byte("000001A 1S0101"), []byte("000002B 1S0202"), []byte("000003C 9S0303"), []byte("000004A 1S0404")}
	set := openSet(t, writeFrames(t, records), view)
	if set.Len() != 3 || set.Skipped() != 1 {
		t.Fatalf("len=%d skipped=%d, want 3 and 1", set.Len(), set.Skipped())
	}
	compiled, _ := query.Compile(`map(.ID)`)
	got := set.Query(context.Background(), compiled, 10)
	if len(got.Lines) != 1 || got.Lines[0] != `[1,2,4]` {
		t.Fatalf("ids = %v", got.Lines)
	}
}

func TestOpenRejectsDamagedFrames(t *testing.T) {
	view := testView(t, variableCopybook)
	good := []byte{0, 0, 0, 9, '0', '0', '0', '0', '0', '1', 'A', ' ', '0'}
	for name, tc := range map[string]struct {
		data []byte
		want string
	}{
		"truncated header": {append(append([]byte{}, good...), 0, 0), "truncated record frame header"},
		"truncated data":   {append(append([]byte{}, good...), 0, 0, 0, 9, 'x'), "record frame declares 9 bytes but is truncated"},
		"oversized frame":  {[]byte{0xff, 0xff, 0xff, 0xff}, "exceeds the 16777216 byte bound"},
	} {
		path := filepath.Join(t.TempDir(), "damaged.records")
		if err := os.WriteFile(path, tc.data, 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := Open(context.Background(), path, view); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: error = %v, want one containing %q", name, err, tc.want)
		}
	}
	if _, err := Open(context.Background(), filepath.Join(t.TempDir(), "missing"), view); err == nil {
		t.Error("opening a missing file succeeded")
	}
}

func TestEmptyFile(t *testing.T) {
	view := testView(t, fixedCopybook)
	set := openSet(t, writeFrames(t, nil), view)
	for expr, want := range map[string]string{`length`: `0`, `map(.ID)`: `[]`, `[.[]]`: `[]`, `.[0]`: `null`, `add`: `null`} {
		compiled, _ := query.Compile(expr)
		got := set.Query(context.Background(), compiled, 10)
		if got.Err != nil || len(got.Lines) != 1 || got.Lines[0] != want {
			t.Errorf("%s over an empty set = %v (err %v), want %s", expr, got.Lines, got.Err, want)
		}
	}
}

func TestQueryHonorsCancellation(t *testing.T) {
	view := testView(t, fixedCopybook)
	records := make([][]byte, 60000)
	for i := range records {
		records[i] = fixedRecord(i)
	}
	set := openSet(t, writeFrames(t, records), view)
	for _, expr := range []string{`.[] | select(.ID == -1)`, `map(.AMOUNT) | add`} {
		compiled, _ := query.Compile(expr)
		for _, workers := range []int{1, 4} {
			Workers = func(int) int { return workers }
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			got := set.Query(ctx, compiled, 10)
			if !got.Canceled || got.Err != nil {
				t.Errorf("%s (workers=%d): canceled=%v err=%v, want a cancellation", expr, workers, got.Canceled, got.Err)
			}
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := Open(ctx, writeFrames(t, records), view); !errors.Is(err, context.Canceled) {
		t.Errorf("Open with a cancelled context: %v", err)
	}
}

// Field access must not decode the rest of the record: that is the entire
// performance argument for lazy records.
func TestFieldAccessAllocatesLittle(t *testing.T) {
	view := testView(t, fixedCopybook)
	records := make([][]byte, 2000)
	for i := range records {
		records[i] = fixedRecord(i)
	}
	set := openSet(t, writeFrames(t, records), view)
	Workers = func(int) int { return 1 }
	compiled, _ := query.Compile(`.[] | select(.STATUS == "X")`)
	perRecord := testing.AllocsPerRun(5, func() { set.Query(context.Background(), compiled, 10) }) / float64(len(records))
	// One record handle and one decoded one-byte string; a full decode of
	// this layout costs more than twenty allocations.
	if perRecord > 6 {
		t.Fatalf("a one-field filter allocates %.1f times per record", perRecord)
	}
}
