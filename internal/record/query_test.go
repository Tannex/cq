package record

import (
	"bytes"
	"fmt"
	"math/rand/v2"
	"reflect"
	"sort"
	"testing"

	"github.com/Tannex/cq/internal/copybook"
	"github.com/Tannex/cq/internal/decode"
	"github.com/Tannex/cq/internal/gojq"
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
	"numbers of every width": `
01 REC.
   05 P20 PIC S9(18)V99 COMP-3.
   05 P17 PIC 9(16)V9 COMP-3.
   05 P31 PIC S9(31) COMP-3.
   05 Z20 PIC 9(20).
   05 Z70 PIC 9(70).
   05 Z70V PIC 9(66)V9(4).
   05 ZS PIC S9(5)V9(3).
   05 ZI PIC S9(2).
   05 PI PIC S9(3) COMP-3.
   05 ZV25 PIC V9(25).
   05 B8 PIC 9(18) COMP.
   05 B8S PIC S9(18) COMP.
   05 B4V PIC S9(7)V99 COMP.
   05 B2 PIC 9(4) COMP.
   05 TS PIC S9(4)V9 SIGN TRAILING SEPARATE.
   05 LS PIC S9(2) SIGN LEADING SEPARATE.
   05 ED PIC -9(3).99.
   05 F8 COMP-2.
   05 GRP OCCURS 2 TIMES.
      10 Q PIC S9(3)V9 COMP-3.
      10 T PIC X(2).
`,
	"ODO inside groups": `
01 REC.
   05 HDR.
      10 A PIC X.
      10 CNT PIC 9.
   05 MID PIC S9(2) COMP-3.
   05 OUTER.
      10 K PIC X.
      10 INNER.
         15 ROWS OCCURS 0 TO 3 TIMES DEPENDING ON CNT.
            20 RK PIC X(2).
            20 RV PIC S9(3) COMP-3.
            20 RT PIC X OCCURS 2 TIMES.
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
// taken whole or member by member, at every depth and every occurrence. It
// reports whether the record was accepted.
func checkQueryView(t *testing.T, d *Decoder, view *QueryView, rec []byte) bool {
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
		return false
	}
	want := displayAsQueryValue(display.Value).(map[string]any)
	if got := view.Value(rec); !reflect.DeepEqual(got, want) {
		t.Fatalf("record %x:\n Value = %#v\n want    %#v", rec, got, want)
	}
	if !reflect.DeepEqual(view.Names(), view.Root().Names()) {
		t.Fatal("the view's members are not its root's")
	}
	for i, name := range view.Names() {
		member := view.Member(name)
		if member != view.Members()[i] {
			t.Fatalf("Members()[%d] is not Member(%q)", i, name)
		}
		if got := view.Field(rec, member); !reflect.DeepEqual(got, want[name]) {
			t.Fatalf("record %x: Field(%s) = %#v, want %#v", rec, name, got, want[name])
		}
		if start, end, fixed := view.Span(member); fixed && (start < 0 || end > len(rec) || start > end) {
			t.Fatalf("Span(%s) = %d:%d outside a %d byte record", name, start, end, len(rec))
		}
	}
	checkQueryOne(t, view, rec, view.Root(), 0, want, "")
	return true
}

// checkQueryOne holds one occurrence of a member, and everything below it,
// to its reference value. It also checks the byte ranges the shapes claim
// decide their values, by changing every other byte of the record.
func checkQueryOne(t *testing.T, view *QueryView, rec []byte, s *Shape, base int, want any, path string) {
	t.Helper()
	if got := view.Element(rec, s, base); !reflect.DeepEqual(got, want) {
		t.Fatalf("record %x: Element(%s) = %#v, want %#v", rec, path, got, want)
	}
	if s.ElementFixed() {
		start, end := s.ElementSpan(base)
		if start < 0 || end > len(rec) || start > end {
			t.Fatalf("ElementSpan(%s) = %d:%d outside a %d byte record", path, start, end, len(rec))
		}
		if got := view.Element(scrambledOutside(rec, start, end), s, base); !reflect.DeepEqual(got, want) {
			t.Fatalf("record %x: %s depends on bytes outside %d:%d: %#v, want %#v", rec, path, start, end, got, want)
		}
	}
	if !s.Group() {
		return
	}
	object := want.(map[string]any)
	names := make([]string, 0, len(object))
	for name := range object {
		names = append(names, name)
	}
	sort.Strings(names)
	if !reflect.DeepEqual(names, append([]string{}, s.Names()...)) {
		t.Fatalf("%s: Names = %v, want %v", path, s.Names(), names)
	}
	if s.Member("NO-SUCH-MEMBER") != nil {
		t.Fatalf("%s: Member resolved an unknown name", path)
	}
	for i, name := range s.Names() {
		member := s.Member(name)
		if member != s.Members()[i] {
			t.Fatalf("%s: Members()[%d] is not Member(%q)", path, i, name)
		}
		memberBase, memberPath := member.Base(base), path+"."+name
		if member.Fixed() {
			start, end := member.Span(memberBase)
			if start < 0 || end > len(rec) || start > end {
				t.Fatalf("Span(%s) = %d:%d outside a %d byte record", memberPath, start, end, len(rec))
			}
			if got := view.d.queryMember(member, scrambledOutside(rec, start, end), memberBase); !reflect.DeepEqual(got, object[name]) {
				t.Fatalf("record %x: %s depends on bytes outside %d:%d", rec, memberPath, start, end)
			}
		}
		if !member.Table() {
			checkQueryOne(t, view, rec, member, memberBase, object[name], memberPath)
			continue
		}
		array := object[name].([]any)
		if count := view.Count(rec, member); count != len(array) {
			t.Fatalf("record %x: Count(%s) = %d, want %d", rec, memberPath, count, len(array))
		}
		for j, element := range array {
			checkQueryOne(t, view, rec, member, memberBase+j*member.Field.Length, element, fmt.Sprintf("%s[%d]", memberPath, j))
		}
	}
}

// scrambledOutside returns a copy of rec with every byte outside start:end
// changed.
func scrambledOutside(rec []byte, start, end int) []byte {
	scrambled := make([]byte, len(rec))
	for i, c := range rec {
		if i < start || i >= end {
			c = ^c ^ byte(i)
		}
		scrambled[i] = c
	}
	return scrambled
}

// checkScalarOrder holds CompareScalar to the engine's Compare of the decoded
// values for every scalar occurrence of two accepted records, pairing every
// occurrence of a table in one with every occurrence in the other. It
// returns how many comparisons took the shortcut.
func checkScalarOrder(t *testing.T, view *QueryView, a []byte, baseA int, b []byte, baseB int, s *Shape) (shortcuts int) {
	t.Helper()
	if !s.Group() {
		if checkCompareScalar(t, view, s, a[baseA:baseA+s.Field.Length], b[baseB:baseB+s.Field.Length]) {
			shortcuts++
		}
		return shortcuts
	}
	for _, member := range s.Members() {
		if !member.Table() {
			shortcuts += checkScalarOrder(t, view, a, member.Base(baseA), b, member.Base(baseB), member)
			continue
		}
		for i := range view.Count(a, member) {
			for j := range view.Count(b, member) {
				shortcuts += checkScalarOrder(t, view, a, member.Base(baseA)+i*member.Field.Length, b, member.Base(baseB)+j*member.Field.Length, member)
			}
		}
	}
	return shortcuts
}

// checkCompareScalar holds CompareScalar on two raw values of a scalar
// member to Compare of their decodings, and reports whether it took the
// shortcut. The decodings come from DecodeDisplay's scalar path, which
// shares no code with the shortcut, and must equal the view's.
func checkCompareScalar(t *testing.T, view *QueryView, s *Shape, rawA, rawB []byte) bool {
	t.Helper()
	valueA, _ := view.d.displayScalar(s.Field, rawA)
	valueB, _ := view.d.displayScalar(s.Field, rawB)
	if got := view.Element(rawA, s, 0); !reflect.DeepEqual(got, any(valueA)) {
		t.Fatalf("%s: Element(%x) = %#v, want %#v", s.Field.Name, rawA, got, valueA)
	}
	got, ok := view.CompareScalar(s, rawA, rawB)
	if want := gojq.Compare(valueA, valueB); ok && got != want {
		t.Fatalf("%s: CompareScalar(%x, %x) = %d, but Compare(%#v, %#v) = %d", s.Field.Name, rawA, rawB, got, valueA, valueB, want)
	}
	return ok
}

// scalarShapes lists every scalar shape below s.
func scalarShapes(s *Shape) []*Shape {
	if !s.Group() {
		return []*Shape{s}
	}
	var shapes []*Shape
	for _, member := range s.Members() {
		shapes = append(shapes, scalarShapes(member)...)
	}
	return shapes
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
			var previous []byte
			shortcuts := 0
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
				if checkQueryView(t, d, view, rec) {
					if previous != nil {
						shortcuts += checkScalarOrder(t, view, previous, 0, rec, 0, view.Root())
						shortcuts += checkScalarOrder(t, view, rec, 0, previous, 0, view.Root())
					}
					previous = rec
				}
			}
			if shortcuts == 0 {
				t.Errorf("%s: no scalar comparison took a shortcut", name)
			}
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

// queryViewDecoders builds a decoder and view for every test copybook, with
// and without fillers, in a stable order.
func queryViewDecoders(tb testing.TB) ([]*Decoder, []*QueryView) {
	var decoders []*Decoder
	var views []*QueryView
	names := make([]string, 0, len(queryViewCopybooks))
	for name := range queryViewCopybooks {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		for _, fillers := range []bool{false, true} {
			d := queryViewDecoder(tb, queryViewCopybooks[name], fillers)
			decoders = append(decoders, d)
			views = append(views, d.QueryView())
		}
	}
	return decoders, views
}

// FuzzQueryViewMatchesDecodeDisplay extends the random comparison to
// whatever bytes the fuzzer finds, and the scalar order to the two halves of
// those bytes as a pair of records.
func FuzzQueryViewMatchesDecodeDisplay(f *testing.F) {
	decoders, views := queryViewDecoders(f)
	f.Add([]byte{})
	f.Add(bytes.Repeat([]byte{0x40}, 64))
	f.Add(bytes.Repeat([]byte{0xF0}, 64))
	f.Add(bytes.Repeat([]byte{0xF3}, 64))
	f.Add(bytes.Repeat([]byte{0xFF}, 64))
	f.Add(bytes.Repeat([]byte{0x00}, 128))
	f.Add(bytes.Repeat([]byte{0xF1}, 1024))
	f.Fuzz(func(t *testing.T, data []byte) {
		a, b := data[:len(data)/2], data[len(data)/2:]
		for i, d := range decoders {
			checkQueryView(t, d, views[i], data)
			if checkQueryView(t, d, views[i], a) && checkQueryView(t, d, views[i], b) {
				checkScalarOrder(t, views[i], a, 0, b, 0, views[i].Root())
			}
		}
	})
}

// FuzzCompareScalar holds the scalar order of every field of every test
// copybook to the decoded order for whatever two values the fuzzer finds.
func FuzzCompareScalar(f *testing.F) {
	_, views := queryViewDecoders(f)
	f.Add([]byte{0xF1, 0xF2, 0xC3}, []byte{0xF1, 0xF2, 0xD3})
	f.Add([]byte{0x12, 0x3C}, []byte{0x12, 0x3D})
	f.Add([]byte{0x40, 0x40}, []byte{0xF0, 0xF0})
	f.Add(bytes.Repeat([]byte{0x99}, 40), bytes.Repeat([]byte{0x09}, 40))
	f.Fuzz(func(t *testing.T, a, b []byte) {
		for _, view := range views {
			for _, s := range scalarShapes(view.Root()) {
				if n := s.Field.Length; len(a) >= n && len(b) >= n {
					checkCompareScalar(t, view, s, a[:n], b[:n])
				}
			}
		}
	})
}

// randomScalar returns raw bytes for one value of a scalar field: well
// formed for its kind more often than not, so numbers are compared with
// numbers, but also damaged in the ways host data is.
func randomScalar(rng *rand.Rand, f *layout.Field) []byte {
	raw := make([]byte, f.Length)
	// Mostly small digits keep magnitudes close, so the deciding digit is
	// often far to the right.
	zero := rng.IntN(8) == 0 // the sign of zero is normalized away
	digit := func() byte {
		if zero {
			return 0
		}
		if rng.IntN(3) == 0 {
			return byte(rng.IntN(10))
		}
		return byte(rng.IntN(2))
	}
	switch mode := rng.IntN(6); {
	case mode == 0:
		for i := range raw {
			raw[i] = queryViewBytes[rng.IntN(len(queryViewBytes))]
		}
	case mode == 1:
		for i := range raw {
			raw[i] = byte(rng.IntN(256))
		}
	case f.Kind == layout.KindZoned:
		for i := range raw {
			raw[i] = 0xF0 | digit()
		}
		if f.SignSeparate {
			sign := []byte{0x4E, 0x60, 0x40, 0x00, 0xC1}[rng.IntN(5)]
			if f.SignLeading {
				raw[0] = sign
			} else {
				raw[len(raw)-1] = sign
			}
		} else {
			raw[len(raw)-1] = []byte{0xF0, 0xC0, 0xD0, 0xA0, 0xE0, 0x30}[rng.IntN(6)] | raw[len(raw)-1]&0x0F
		}
		if rng.IntN(10) == 0 {
			for i := range raw {
				raw[i] = 0x40
			}
		}
	case f.Kind == layout.KindPacked:
		for i := range raw {
			raw[i] = digit()<<4 | digit()
		}
		raw[len(raw)-1] = raw[len(raw)-1]&0xF0 | []byte{0x0C, 0x0D, 0x0F, 0x0B, 0x0A, 0x0E}[rng.IntN(6)]
	default:
		alphabet := []byte{0x00, 0x15, 0x40, 0x4B, 0x81, 0xC1, 0xF0, 0xFF}
		for i := range raw {
			raw[i] = alphabet[rng.IntN(len(alphabet))]
		}
	}
	return raw
}

// The scalar order must equal the decoded order for every field kind, for
// well-formed and damaged values, and for values differing in one byte.
func TestCompareScalarMatchesCompare(t *testing.T) {
	_, views := queryViewDecoders(t)
	rng := rand.New(rand.NewPCG(3, 5))
	for _, view := range views {
		for _, s := range scalarShapes(view.Root()) {
			shortcuts := 0
			for range 4000 {
				a := randomScalar(rng, s.Field)
				b := randomScalar(rng, s.Field)
				if rng.IntN(3) == 0 {
					b = append([]byte{}, a...)
					b[rng.IntN(len(b))] ^= byte(1 + rng.IntN(255))
				}
				for _, pair := range [][2][]byte{{a, b}, {b, a}, {a, a}} {
					if checkCompareScalar(t, view, s, pair[0], pair[1]) {
						shortcuts++
					}
				}
			}
			if s.kind != orderDecode && s.Field.Length <= 32 && shortcuts == 0 {
				t.Errorf("%s: no comparison took the shortcut", s.Field.Name)
			}
		}
	}
}

// Distinct decimals with a fraction can parse to the same float64, and the
// engine then calls them equal; the shortcut must too.
func TestCompareScalarRoundsLikeTheEngine(t *testing.T) {
	d := queryViewDecoder(t, queryViewCopybooks["numbers of every width"], false)
	view := d.QueryView()
	s := view.Member("P17") // PIC 9(16)V9 COMP-3

	a := []byte{0x09, 0x00, 0x71, 0x99, 0x25, 0x47, 0x40, 0x98, 0x2F} // 900719925474098.2
	b := []byte{0x09, 0x00, 0x71, 0x99, 0x25, 0x47, 0x40, 0x98, 0x3F} // 900719925474098.3
	if got, ok := view.CompareScalar(s, a, b); !ok || got != 0 {
		t.Fatalf("CompareScalar = %d, %v; want 0 from the shortcut", got, ok)
	}
	if want := gojq.Compare(view.Element(a, s, 0), view.Element(b, s, 0)); want != 0 {
		t.Fatalf("the engine orders the pair %d; the test no longer exercises rounding", want)
	}
}
