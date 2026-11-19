package recordset

import (
	"fmt"
	"math/rand/v2"
	"reflect"
	"sort"
	"testing"

	"github.com/Tannex/cq/internal/copybook"
	"github.com/Tannex/cq/internal/decode"
	"github.com/Tannex/cq/internal/gojq"
	"github.com/Tannex/cq/internal/layout"
	"github.com/Tannex/cq/internal/record"
)

// nestedCopybooks have the shapes lazy groups and tables must get right:
// FILLER, a duplicate name inside a group, OCCURS of scalars and of groups
// nested in each other, and a DEPENDING ON table inside a group.
var nestedCopybooks = map[string]string{
	"fixed": `
01 REC.
   05 ID PIC 9(4).
   05 FILLER PIC X(2).
   05 CUSTOMER.
      10 CITY PIC X(6).
      10 FILLER PIC X.
      10 TIER PIC S9(3) COMP-3.
      10 TIER PIC 9(2).
   05 ITEMS OCCURS 3 TIMES.
      10 CODE PIC X(2).
      10 QTY PIC S9(2)V9.
      10 PARTS OCCURS 2 TIMES.
         15 PART PIC X.
         15 W PIC 9 COMP-3.
   05 TAGS PIC X(2) OCCURS 2 TIMES.
   05 AMT PIC 9(16)V9 COMP-3.
   05 RATE COMP-1.
   05 TRAILER PIC X(3).
`,
	"scalar table inside a group": `
01 REC.
   05 ZCNT PIC 9.
   05 MID PIC X.
   05 G.
      10 T PIC X OCCURS 0 TO 3 TIMES DEPENDING ON ZCNT.
`,
	"table inside a group": `
01 REC.
   05 HDR.
      10 A PIC X.
      10 CNT PIC 9.
      10 NAME PIC X(3).
   05 BODY.
      10 K PIC X.
      10 ROWS OCCURS 0 TO 3 TIMES DEPENDING ON CNT.
         15 RK PIC X(2).
         15 RV PIC S9(3) COMP-3.
`,
}

func nestedView(t testing.TB, source string, fillers bool) *record.QueryView {
	t.Helper()
	items, err := copybook.Parse(source, copybook.FormatFree)
	if err != nil {
		t.Fatal(err)
	}
	recs, err := layout.Build(items)
	if err != nil {
		t.Fatal(err)
	}
	cm, err := decode.Codepage("cp037")
	if err != nil {
		t.Fatal(err)
	}
	decoder, err := record.NewDecoder(recs[0], cm)
	if err != nil {
		t.Fatal(err)
	}
	decoder.IncludeFillers = fillers
	return decoder.QueryView()
}

// nestedRecords returns records for a layout: well-formed EBCDIC digits and
// text mixed with damaged bytes, and copies differing in up to three bytes,
// so comparisons meet equal members, close members and placeholders. pairs
// lists each copy with its original: equal bytes almost everywhere is where
// a wrong shortcut shows.
func nestedRecords(rng *rand.Rand, view *record.QueryView, maxLength int) (records [][]byte, pairs [][2]int) {
	alphabet := []byte{0x40, 0xC1, 0xC2, 0x81, 0x4B, 0x00, 0x15, 0xFF, 0x0C, 0x1D, 0x12, 0xD3, 0xC5}
	for len(records) < 600 {
		var rec []byte
		source := -1
		switch {
		case len(records) > 0 && rng.IntN(3) == 0:
			source = rng.IntN(len(records))
			rec = append([]byte{}, records[source]...)
			for range rng.IntN(4) {
				// The front holds the counters; favor it.
				i := rng.IntN(len(rec))
				if rng.IntN(2) == 0 {
					i = rng.IntN(min(len(rec), 4))
				}
				rec[i] = []byte{0xF0, 0xF1, 0xF2, 0xF3, 0xC1, 0x40, 0x00}[rng.IntN(7)]
			}
		default:
			rec = make([]byte, maxLength+rng.IntN(3))
			for i := range rec {
				switch rng.IntN(4) {
				case 0:
					rec[i] = alphabet[rng.IntN(len(alphabet))]
				default:
					rec[i] = 0xF0 + byte(rng.IntN(3))
				}
			}
			if rng.IntN(3) == 0 {
				// Variable records end after the counted occurrences.
				rec = rec[:len(rec)-rng.IntN(len(rec)/3)]
			}
		}
		if view.Check(rec) == nil {
			if source >= 0 {
				pairs = append(pairs, [2]int{source, len(records)})
			}
			records = append(records, rec)
		}
	}
	return records, pairs
}

// materialized converts a lazy value and everything below it to native
// values.
func materialized(v any) any {
	switch v := gojq.Materialize(v).(type) {
	case map[string]any:
		m := make(map[string]any, len(v))
		for k, member := range v {
			m[k] = materialized(member)
		}
		return m
	case []any:
		a := make([]any, len(v))
		for i, element := range v {
			a[i] = materialized(element)
		}
		return a
	default:
		return v
	}
}

// checkNested holds two lazy values, and every lazy value reachable below
// them, to their reference values: they materialize to them, and they
// compare as Compare compares them, through CompareLazy where both are the
// same member.
func checkNested(t *testing.T, a, b, wantA, wantB any, path string) {
	t.Helper()
	if got := materialized(a); !reflect.DeepEqual(got, wantA) {
		t.Fatalf("%s = %#v, want %#v", path, got, wantA)
	}
	want := gojq.Compare(wantA, wantB)
	if got := gojq.Compare(a, b); got != want {
		t.Fatalf("%s: Compare = %d, want %d (%#v vs %#v)", path, got, want, wantA, wantB)
	}
	if got := gojq.Compare(b, a); got != -want {
		t.Fatalf("%s: reversed Compare = %d, want %d", path, got, -want)
	}
	if reflect.TypeOf(a) == reflect.TypeOf(b) {
		if comparer, ok := a.(gojq.LazyComparer); ok {
			if _, ok := comparer.CompareLazy(b); !ok {
				t.Fatalf("%s: CompareLazy declined two instances of one member", path)
			}
		}
	}
	switch a := a.(type) {
	case gojq.LazyObject:
		object := wantA.(map[string]any)
		keys := make([]string, 0, len(object))
		for k := range object {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			checkNested(t, a.Get(k), b.(gojq.LazyObject).Get(k), object[k], wantB.(map[string]any)[k], path+"."+k)
		}
		if a.Get("NO-SUCH-MEMBER") != nil {
			t.Fatalf("%s: an unknown member is not null", path)
		}
	case gojq.LazyArray:
		arrayA, arrayB := wantA.([]any), wantB.([]any)
		if a.Len() != len(arrayA) {
			t.Fatalf("%s: Len = %d, want %d", path, a.Len(), len(arrayA))
		}
		other := b.(gojq.LazyArray)
		for i := range arrayA {
			for j := range arrayB {
				checkNested(t, a.At(i), other.At(j), arrayA[i], arrayB[j], fmt.Sprintf("%s[%d|%d]", path, i, j))
			}
		}
		for start := 0; start <= a.Len(); start++ {
			for end := start; end <= a.Len(); end++ {
				if got := materialized(a.Slice(start, end)); !reflect.DeepEqual(got, arrayA[start:end]) {
					t.Fatalf("%s[%d:%d] = %#v, want %#v", path, start, end, got, arrayA[start:end])
				}
			}
		}
	}
}

// Every lazy value below a record must be indistinguishable from the fully
// decoded value it stands for, in what it holds and in how it orders against
// the same member of another record, another occurrence, or another member.
func TestNestedValuesMatchFullDecode(t *testing.T) {
	for name, source := range nestedCopybooks {
		for _, fillers := range []bool{false, true} {
			view := nestedView(t, source, fillers)
			rng := rand.New(rand.NewPCG(9, uint64(len(source))))
			records, pairs := nestedRecords(rng, view, view.Root().Field.Length)
			set := openSet(t, writeFrames(t, records), view)
			array := set.Array()
			for range 1500 {
				pairs = append(pairs, [2]int{rng.IntN(set.Len()), rng.IntN(set.Len())})
			}
			for _, pair := range pairs {
				i, j := pair[0], pair[1]
				a, b := array.At(i), array.At(j)
				wantA, wantB := view.Value(records[i]), view.Value(records[j])
				checkNested(t, a, b, wantA, wantB, fmt.Sprintf("%s/%v [%d|%d]", name, fillers, i, j))
				// Different members of one layout decline, and the generic
				// comparison orders them.
				members := view.Names()
				x, y := members[rng.IntN(len(members))], members[rng.IntN(len(members))]
				memberA, memberB := a.(*Record).Get(x), b.(*Record).Get(y)
				if x != y {
					if comparer, ok := memberA.(gojq.LazyComparer); ok {
						if _, ok := comparer.CompareLazy(memberB); ok {
							t.Fatalf("CompareLazy accepted %s against %s", x, y)
						}
					}
				}
				if got, want := gojq.Compare(memberA, memberB), gojq.Compare(wantA[x], wantB[y]); got != want {
					t.Fatalf("%s/%v: Compare(.%s, .%s) = %d, want %d", name, fillers, x, y, got, want)
				}
			}
		}
	}
}
