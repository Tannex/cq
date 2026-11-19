package record

import (
	"bytes"
	"math/rand/v2"
	"reflect"
	"sort"
	"testing"

	"github.com/Tannex/cq/internal/copybook"
	"github.com/Tannex/cq/internal/decode"
	"github.com/Tannex/cq/internal/layout"
)

// displayAsQueryValue is the reference conversion the query view replaces:
// the ordered DecodeDisplay value as plain containers, last duplicate wins.
func displayAsQueryValue(value Value) any {
	switch value := value.(type) {
	case Object:
		object := make(map[string]any, len(value))
		for _, member := range value {
			object[member.Name] = displayAsQueryValue(member.Value)
		}
		return object
	case Array:
		array := make([]any, len(value))
		for i, item := range value {
			array[i] = displayAsQueryValue(item)
		}
		return array
	default:
		return value
	}
}

var queryViewCopybooks = map[string]string{
	"every kind with trailing ODO": fuzzCopybook,
	"fixed with groups and occurs": `
01 REC.
   05 ID PIC 9(4).
   05 FILLER PIC X(2).
   05 CUSTOMER.
      10 CITY PIC X(6).
      10 FILLER PIC X.
      10 TIER PIC S9(3) COMP-3.
   05 ITEMS OCCURS 3 TIMES.
      10 CODE PIC X(2).
      10 QTY PIC 9(2).
      10 PARTS OCCURS 2 TIMES.
         15 PART PIC X.
   05 TRAILER PIC X(3).
`,
	"duplicate names and redefines": `
01 REC.
   05 A PIC X(4).
   05 B REDEFINES A PIC 9(4).
   05 A PIC 9(2).
   05 G.
      10 X PIC X.
      10 X PIC 9.
   05 FILLER PIC X(3).
   05 SGN PIC S9(3) SIGN TRAILING SEPARATE.
   05 F4 COMP-1.
`,
	"ODO of groups": `
01 REC.
   05 CNT PIC 9.
   05 NAME PIC X(3).
   05 ROWS OCCURS 0 TO 3 TIMES DEPENDING ON CNT.
      10 K PIC X(2).
      10 V PIC S9(3) COMP-3.
`,
}

func queryViewDecoder(t testing.TB, source string, fillers bool) *Decoder {
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
	d, err := NewDecoder(recs[0], cm)
	if err != nil {
		t.Fatal(err)
	}
	d.IncludeFillers = fillers
	return d
}

// checkQueryView holds the view to DecodeDisplay for one record: the same
// records are rejected, and accepted records decode to the same value whether
// taken whole or member by member.
func checkQueryView(t *testing.T, d *Decoder, view *QueryView, rec []byte) {
	t.Helper()
	display, displayErr := d.DecodeDisplay(rec)
	checkErr := view.Check(rec)
	if (displayErr != nil) != (checkErr != nil) {
		t.Fatalf("record %x: DecodeDisplay error %v, Check error %v", rec, displayErr, checkErr)
	}
	if displayErr != nil {
		if displayErr.Error() != checkErr.Error() {
			t.Fatalf("record %x: DecodeDisplay error %q, Check error %q", rec, displayErr, checkErr)
		}
		return
	}
	want := displayAsQueryValue(display.Value).(map[string]any)
	if got := view.Value(rec); !reflect.DeepEqual(got, want) {
		t.Fatalf("record %x:\n Value = %#v\n want    %#v", rec, got, want)
	}
	names := make([]string, 0, len(want))
	for name := range want {
		names = append(names, name)
	}
	sort.Strings(names)
	if !reflect.DeepEqual(names, append([]string{}, view.Names()...)) {
		t.Fatalf("Names = %v, want %v", view.Names(), names)
	}
	for i, name := range view.Names() {
		field := view.Member(name)
		if field != view.Members()[i] {
			t.Fatalf("Members()[%d] is not Member(%q)", i, name)
		}
		if got := view.Field(rec, field); !reflect.DeepEqual(got, want[name]) {
			t.Fatalf("record %x: Field(%s) = %#v, want %#v", rec, name, got, want[name])
		}
		if start, end, fixed := view.Span(field); fixed && (start < 0 || end > len(rec) || start > end) {
			t.Fatalf("Span(%s) = %d:%d outside a %d byte record", name, start, end, len(rec))
		}
	}
	if view.Member("NO-SUCH-MEMBER") != nil {
		t.Fatal("Member resolved an unknown name")
	}
}

// queryViewBytes are the byte values worth mixing into random records: valid
// and invalid digits and signs, spaces, LOW-VALUE and HIGH-VALUE.
var queryViewBytes = []byte{0x00, 0x40, 0x4E, 0x60, 0xF0, 0xF1, 0xF2, 0xF3, 0xF9, 0xC1, 0xD5, 0x0C, 0x1D, 0x2F, 0x12, 0x99, 0xFA, 0xFF, 0x7F, 0x15}

func TestQueryViewMatchesDecodeDisplay(t *testing.T) {
	for name, source := range queryViewCopybooks {
		for _, fillers := range []bool{false, true} {
			d := queryViewDecoder(t, source, fillers)
			view := d.QueryView()
			rng := rand.New(rand.NewPCG(1, uint64(len(source))))
			for i := 0; i < 20000; i++ {
				// Lengths straddle the layout so short, exact and long
				// records are all covered.
				rec := make([]byte, rng.IntN(d.Rec.MaxLength+6))
				for j := range rec {
					rec[j] = queryViewBytes[rng.IntN(len(queryViewBytes))]
				}
				if i%4 == 0 && len(rec) >= d.Rec.MaxLength {
					// Mostly well-formed digits so accepted records dominate.
					for j := range rec {
						if rng.IntN(8) != 0 {
							rec[j] = 0xF0 + byte(rng.IntN(4))
						}
					}
				}
				checkQueryView(t, d, view, rec)
			}
			_ = name
		}
	}
}

func TestValidJSONNumber(t *testing.T) {
	for token, want := range map[string]bool{
		"": true, "0": true, "-0": true, "12": true, "-12.50": true, "0.00": true, "1e5": true, "1E+06": true, "1.5e-3": true,
		"-": false, "01": false, ".5": false, "1.": false, "1e": false, "1e+": false, "NaN": false, "+Inf": false, "-Inf": false, "1 ": false, "0x1": false,
	} {
		if got := validJSONNumber(token); got != want {
			t.Errorf("validJSONNumber(%q) = %v, want %v", token, got, want)
		}
	}
}

// FuzzQueryViewMatchesDecodeDisplay extends the random comparison to
// whatever bytes the fuzzer finds.
func FuzzQueryViewMatchesDecodeDisplay(f *testing.F) {
	var decoders []*Decoder
	var views []*QueryView
	names := make([]string, 0, len(queryViewCopybooks))
	for name := range queryViewCopybooks {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		for _, fillers := range []bool{false, true} {
			d := queryViewDecoder(f, queryViewCopybooks[name], fillers)
			decoders = append(decoders, d)
			views = append(views, d.QueryView())
		}
	}
	f.Add([]byte{})
	f.Add(bytes.Repeat([]byte{0x40}, 64))
	f.Add(bytes.Repeat([]byte{0xF0}, 64))
	f.Add(bytes.Repeat([]byte{0xF3}, 64))
	f.Add(bytes.Repeat([]byte{0xFF}, 64))
	f.Add(bytes.Repeat([]byte{0x00}, 128))
	f.Fuzz(func(t *testing.T, data []byte) {
		for i, d := range decoders {
			checkQueryView(t, d, views[i], data)
		}
	})
}
