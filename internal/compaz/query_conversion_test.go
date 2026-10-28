package compaz

import (
	"context"
	"reflect"
	"testing"

	"github.com/Tannex/cq/internal/query"
)

// Compare the input and jq outputs to the original JSON round trip. In
// particular, json.Number spelling and duplicate object keys affect aggregates.
func TestDecodeQueryValueMatchesJSONRoundTrip(t *testing.T) {
	const book = `01 REC.
 05 NAME PIC X(8).
 05 BIG PIC 9(20).
 05 BALANCE PIC S9(5)V99 COMP-3.
 05 DETAIL.
  10 FLAG PIC X.
  10 ITEMS OCCURS 2 TIMES.
   15 CODE PIC X(2).
   15 COUNT PIC 9(3).
 05 DUP PIC X(2).
 05 DUP PIC X(2).
 05 FILLER PIC X(2).
`
	data := append([]byte("A\"\\\xe9    12345678901234567890"), 0x00, 0x12, 0x34, 0x5c)
	data = append(data, []byte("YAB001CD002XXYY  ")...)
	invalid := append([]byte(nil), data...)
	invalid[8] = 'X'
	invalid[0] = 0
	for _, tc := range []struct {
		name, book string
		data       []byte
	}{
		{"nested_and_exact_numbers", book, data},
		{"localized_decode_errors", book, invalid},
		{"variable_occurs", `01 REC.
 05 N PIC 9.
 05 ITEMS OCCURS 0 TO 3 TIMES DEPENDING ON N.
  10 VAL PIC X(2).
`, []byte("2AABB")},
		{"empty_occurs", `01 REC.
 05 N PIC 9.
 05 ITEMS OCCURS 0 TO 3 TIMES DEPENDING ON N.
  10 VAL PIC X(2).
`, []byte("0")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ov, err := buildOverlay(context.Background(), CopybookSource{Local: "conversion.cpy", Format: "free"}, "latin1", nil, func(context.Context, string) ([]byte, error) { return []byte(tc.book), nil }, nil)
			if err != nil {
				t.Fatal(err)
			}
			decoded, err := ov.Decoder.DecodeDisplay(tc.data)
			if err != nil {
				t.Fatal(err)
			}
			encoded, err := decoded.JSON()
			if err != nil {
				t.Fatal(err)
			}
			want, err := query.FromJSON(encoded)
			if err != nil {
				t.Fatal(err)
			}
			var msg queryEvalMsg
			got, ok := decodeQueryValue(ov, tc.data, &msg)
			if !ok || !reflect.DeepEqual(got, want) {
				t.Fatalf("decoded query value = %#v, want %#v; status=%+v", got, want, msg)
			}
			for _, expr := range []string{`length`, `unique`, `unique | length`, `map(.NAME) | unique`, `sort`, `group_by(.NAME) | map(length)`, `map(.BIG) | unique`, `map(.BALANCE) | unique`, `map(.DUP) | unique`, `.[]`} {
				q, err := query.Compile(expr)
				if err != nil {
					t.Fatal(err)
				}
				// Repeated values force whole-array deduplication and group behavior.
				evaluate := func(v any) []string {
					var lines []string
					if err := q.Run([]any{v, v, v}, func(out any) error { lines = append(lines, query.Marshal(out)); return nil }); err != nil {
						t.Fatal(err)
					}
					return lines
				}
				if result, expected := evaluate(got), evaluate(want); !reflect.DeepEqual(result, expected) {
					t.Fatalf("%s: result=%v, want %v", expr, result, expected)
				}
			}
		})
	}
}

func TestQueryAggregatesPreserveDistinctNumbers(t *testing.T) {
	const book = `01 REC.
 05 BIG PIC 9(20).
 05 AMOUNT PIC S9(5)V99 COMP-3.
`
	ov, err := buildOverlay(context.Background(), CopybookSource{Local: "numbers.cpy", Format: "free"}, "latin1", nil, func(context.Context, string) ([]byte, error) { return []byte(book), nil }, nil)
	if err != nil {
		t.Fatal(err)
	}
	var values, original []any
	for _, tc := range []struct {
		big    string
		amount []byte
	}{
		{"12345678901234567890", []byte{0x00, 0x00, 0x12, 0x3c}},
		{"12345678901234567891", []byte{0x00, 0x00, 0x12, 0x3c}},
		{"12345678901234567890", []byte{0x00, 0x00, 0x12, 0x4c}},
		{"12345678901234567890", []byte{0x00, 0x00, 0x12, 0x3c}},
		{"12345678901234567890", []byte{0x00, 0x00, 0x12, 0x3d}},
	} {
		data := append([]byte(tc.big), tc.amount...)
		var msg queryEvalMsg
		value, ok := decodeQueryValue(ov, data, &msg)
		if !ok {
			t.Fatalf("decode failed: %+v", msg)
		}
		values = append(values, value)
		decoded, err := ov.Decoder.DecodeDisplay(data)
		if err != nil {
			t.Fatal(err)
		}
		encoded, err := decoded.JSON()
		if err != nil {
			t.Fatal(err)
		}
		prior, err := query.FromJSON(encoded)
		if err != nil {
			t.Fatal(err)
		}
		original = append(original, prior)
	}
	for _, tc := range []struct{ expr, want string }{
		{`length`, `5`},
		{`unique | length`, `4`},
		{`map(.BIG) | unique`, `[12345678901234567890,12345678901234567891]`},
		{`map(.AMOUNT) | unique`, `[-1.23,1.23,1.24]`},
		{`group_by(.BIG) | map(length)`, `[4,1]`},
	} {
		t.Run(tc.expr, func(t *testing.T) {
			compiled, err := query.Compile(tc.expr)
			if err != nil {
				t.Fatal(err)
			}
			for _, input := range [][]any{original, values} {
				var lines []string
				if err := compiled.Run(input, func(out any) error {
					lines = append(lines, query.Marshal(out))
					return nil
				}); err != nil {
					t.Fatal(err)
				}
				if len(lines) != 1 || lines[0] != tc.want {
					t.Fatalf("%s = %v, want %s", tc.expr, lines, tc.want)
				}
			}
		})
	}
}
