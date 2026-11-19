package record

import (
	"bytes"
	"math/rand/v2"
	"reflect"
	"sort"
	"testing"

	"github.com/Tannex/cq/internal/copybook"
	"github.com/Tannex/cq/internal/decode"
	"github.com/Tannex/cq/internal/gojq"
	"github.com/Tannex/cq/internal/layout"
)

// queryViewDecoderCP builds a decoder like queryViewDecoder, for any
// codepage.
func queryViewDecoderCP(t testing.TB, source, codepage string, fillers bool) *Decoder {
	t.Helper()
	items, err := copybook.Parse(source, copybook.FormatFree)
	if err != nil {
		t.Fatal(err)
	}
	recs, err := layout.Build(items)
	if err != nil {
		t.Fatal(err)
	}
	cm, err := decode.Codepage(codepage)
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

var exhaustiveCodepages = []string{"cp037", "cp277", "cp1047", "cp1140", "cp1142", "latin1"}

// exhaustiveScalars has every USAGE, signed and unsigned, with scale zero,
// positive and negative, in the widths that can be enumerated completely
// (one and two bytes) and in wider ones that are sampled.
const exhaustiveScalars = `
01 REC.
   05 Z1 PIC 9.
   05 Z1S PIC S9.
   05 Z1V PIC V9.
   05 Z1SV PIC SV9.
   05 Z1SP PIC SP9.
   05 Z1PP PIC S9PP.
   05 Z1VP PIC SVP9.
   05 Z2 PIC 99.
   05 Z2S PIC S99.
   05 Z2SV PIC S9V9.
   05 Z2SVV PIC SV99.
   05 Z2PP PIC S99PP.
   05 Z2VPPP PIC SVPPP99.
   05 L1 PIC S9 SIGN LEADING SEPARATE.
   05 T1 PIC S9 SIGN TRAILING SEPARATE.
   05 L1V PIC SV9 SIGN LEADING SEPARATE.
   05 T1V PIC SV9 SIGN TRAILING SEPARATE.
   05 P1 PIC 9 COMP-3.
   05 P1S PIC S9 COMP-3.
   05 P1V PIC SV9 COMP-3.
   05 P1P PIC S9PP COMP-3.
   05 P2 PIC S99 COMP-3.
   05 P3 PIC S999 COMP-3.
   05 P3V PIC S9V99 COMP-3.
   05 P3VV PIC SV999 COMP-3.
   05 B2 PIC 9(4) COMP.
   05 B2S PIC S9(4) COMP.
   05 B2V PIC S99V99 COMP.
   05 B2VU PIC 9V999 COMP.
   05 B2P PIC S9(3)PP COMP-5.
   05 X1 PIC X.
   05 X2 PIC XX.
   05 E2 PIC Z9.
   05 E1 PIC Z.
   05 Z5 PIC S9(5).
   05 Z5V PIC S9(3)V99.
   05 Z19 PIC S9(19).
   05 Z20V PIC S9(16)V9(4).
   05 Z25V PIC 9(2)V9(23).
   05 Z64 PIC S9(64).
   05 Z65 PIC S9(65).
   05 Z64V PIC S9(40)V9(24).
   05 L64 PIC S9(64) SIGN LEADING SEPARATE.
   05 T65 PIC S9(65) SIGN TRAILING SEPARATE.
   05 L5V PIC S9(3)V99 SIGN LEADING SEPARATE.
   05 T5V PIC S9(3)V99 SIGN TRAILING SEPARATE.
   05 P10 PIC S9(18) COMP-3.
   05 P10V PIC S9(15)V9(3) COMP-3.
   05 P17 PIC S9(16)V9 COMP-3.
   05 P31V PIC S9(8)V9(23) COMP-3.
   05 P33 PIC S9(65) COMP-3.
   05 P32 PIC S9(63) COMP-3.
   05 B4 PIC S9(9) COMP.
   05 B4U PIC 9(9) COMP.
   05 B4V PIC S9(5)V9(4) COMP.
   05 B8 PIC S9(18) COMP.
   05 B8U PIC 9(18) COMP.
   05 B8V PIC S9(16)V99 COMP.
   05 B8UV PIC 9(2)V9(16) COMP.
   05 X10 PIC X(10).
   05 E8 PIC -(4)9.99.
   05 F4 COMP-1.
   05 F8 COMP-2.
`

// scalarCandidates lists raw values for a field: every value for one and
// two bytes, and for wider fields every byte at every position over a set
// of backgrounds, plus random values.
func scalarCandidates(rng *rand.Rand, n int) [][]byte {
	var out [][]byte
	switch n {
	case 1:
		for b := range 256 {
			out = append(out, []byte{byte(b)})
		}
		return out
	case 2:
		for b := range 65536 {
			out = append(out, []byte{byte(b >> 8), byte(b)})
		}
		return out
	}
	backgrounds := [][]byte{
		bytes.Repeat([]byte{0xF0}, n), bytes.Repeat([]byte{0xF9}, n), bytes.Repeat([]byte{0x40}, n),
		bytes.Repeat([]byte{0x00}, n), bytes.Repeat([]byte{0xFF}, n), bytes.Repeat([]byte{0x99}, n),
		bytes.Repeat([]byte{0x20}, n), bytes.Repeat([]byte{0x30}, n), bytes.Repeat([]byte{0x80}, n),
		bytes.Repeat([]byte{0x7F}, n), bytes.Repeat([]byte{0x09}, n),
	}
	for _, background := range backgrounds {
		out = append(out, background)
		positions := []int{0, 1, n / 2, n - 2, n - 1}
		if n <= 20 {
			positions = positions[:0]
			for i := range n {
				positions = append(positions, i)
			}
		}
		for _, i := range positions {
			for b := range 256 {
				raw := append([]byte{}, background...)
				raw[i] = byte(b)
				out = append(out, raw)
			}
		}
	}
	// Values around 2^53 and powers of ten in packed, zoned and binary.
	for _, digits := range []string{"9007199254740992", "9007199254740993", "9007199254740991", "18014398509481984",
		"1000000000000000000", "9999999999999999999", "10000000000000000000", "18446744073709551615", "9223372036854775807", "9223372036854775808"} {
		out = append(out, packedRaw(digits, n, 0x0C), packedRaw(digits, n, 0x0D), zonedRaw(digits, n, 0xF0), zonedRaw(digits, n, 0xD0))
	}
	for range 4000 {
		raw := make([]byte, n)
		switch rng.IntN(4) {
		case 0:
			for i := range raw {
				raw[i] = byte(rng.IntN(256))
			}
		case 1:
			for i := range raw {
				raw[i] = 0xF0 | byte(rng.IntN(10))
			}
			raw[n-1] = []byte{0xC0, 0xD0, 0xF0, 0xA0, 0xB0, 0xE0}[rng.IntN(6)] | byte(rng.IntN(10))
		case 2:
			for i := range raw {
				raw[i] = byte(rng.IntN(10))<<4 | byte(rng.IntN(10))
			}
			raw[n-1] = raw[n-1]&0xF0 | []byte{0x0C, 0x0D, 0x0F, 0x0B, 0x0A, 0x0E}[rng.IntN(6)]
		default:
			copy(raw, backgrounds[rng.IntN(len(backgrounds))])
			raw[rng.IntN(n)] = byte(rng.IntN(256))
			raw[rng.IntN(n)] = byte(rng.IntN(256))
		}
		out = append(out, raw)
	}
	return out
}

func packedRaw(digits string, n int, sign byte) []byte {
	all := make([]byte, 2*n-1)
	copy(all[max(len(all)-len(digits), 0):], digits[max(len(digits)-len(all), 0):])
	raw := make([]byte, n)
	for i := range all {
		d := all[i]
		if d >= '0' {
			d -= '0'
		}
		if i%2 == 0 {
			raw[i/2] |= d << 4
		} else {
			raw[i/2] |= d
		}
	}
	raw[n-1] |= sign
	return raw
}

func zonedRaw(digits string, n int, lastZone byte) []byte {
	raw := bytes.Repeat([]byte{0xF0}, n)
	for i := 0; i < len(digits) && i < n; i++ {
		raw[n-1-i] = 0xF0 | (digits[len(digits)-1-i] - '0')
	}
	raw[n-1] = lastZone | raw[n-1]&0x0F
	return raw
}

// TestScalarsExhaustive decodes every candidate value of every scalar field
// through the query path and DecodeDisplay's scalar path, and orders them
// with CompareScalar against gojq.Compare of the reference values: along the
// reference order (where ties and near ties sit next to each other), against
// anchors, and at random.
func TestScalarsExhaustive(t *testing.T) {
	for _, codepage := range exhaustiveCodepages {
		d := queryViewDecoderCP(t, exhaustiveScalars, codepage, false)
		view := d.QueryView()
		rng := rand.New(rand.NewPCG(11, uint64(len(codepage))))
		for _, s := range scalarShapes(view.Root()) {
			f := s.Field
			// The codepage only matters to text (whose order
			// TestCompareDisplayAllPairs covers in every codepage) and to
			// separate signs.
			switch {
			case f.SignSeparate:
			case f.Kind == layout.KindText || f.Kind == layout.KindEdited:
				if codepage != "cp037" && codepage != "latin1" {
					continue
				}
			case codepage != "cp037":
				continue
			}
			t.Run(codepage+"/"+f.Name, func(t *testing.T) {
				candidates := scalarCandidates(rng, f.Length)
				refs := make([]any, len(candidates))
				for i, raw := range candidates {
					want, _ := d.displayScalar(f, raw)
					refs[i] = want
					if got := view.Element(raw, s, 0); !reflect.DeepEqual(got, any(want)) {
						t.Fatalf("Element(%x) = %#v, DecodeDisplay %#v", raw, got, want)
					}
				}
				check := func(i, j int) {
					got, ok := view.CompareScalar(s, candidates[i], candidates[j])
					if !ok {
						return
					}
					if want := gojq.Compare(refs[i], refs[j]); got != want {
						t.Fatalf("CompareScalar(%x, %x) = %d, Compare(%#v, %#v) = %d", candidates[i], candidates[j], got, refs[i], refs[j], want)
					}
				}
				if len(candidates) <= 256 {
					for i := range candidates {
						for j := range candidates {
							check(i, j)
						}
					}
					return
				}
				order := make([]int, len(candidates))
				for i := range order {
					order[i] = i
				}
				sort.SliceStable(order, func(x, y int) bool { return gojq.Compare(refs[order[x]], refs[order[y]]) < 0 })
				for k := 1; k < len(order); k++ {
					for back := 1; back <= 3 && back <= k; back++ {
						check(order[k], order[k-back])
						check(order[k-back], order[k])
					}
					check(order[k], order[k])
				}
				anchors := make([]int, 12)
				for k := range anchors {
					anchors[k] = rng.IntN(len(candidates))
				}
				for i := range candidates {
					for _, a := range anchors {
						check(i, a)
						check(a, i)
					}
				}
			})
		}
	}
}

// TestCompareDisplayAllPairs orders every pair of one- and two-byte text
// values, trailing spaces included, in every codepage.
func TestCompareDisplayAllPairs(t *testing.T) {
	for _, codepage := range exhaustiveCodepages {
		cm, err := decode.Codepage(codepage)
		if err != nil {
			t.Fatal(err)
		}
		strs := make([]string, 256)
		for b := range 256 {
			strs[b] = decode.DisplayString([]byte{byte(b)}, cm)
		}
		for a := range 256 {
			for b := range 256 {
				got := decode.CompareDisplay([]byte{byte(a)}, []byte{byte(b)}, cm)
				if want := gojq.Compare(strs[a], strs[b]); got != want {
					t.Fatalf("%s: CompareDisplay(%02x, %02x) = %d, want %d", codepage, a, b, got, want)
				}
			}
		}
		// Two bytes against one: covers prefixes and trailing trimming.
		for a := range 65536 {
			ra := []byte{byte(a >> 8), byte(a)}
			sa := decode.DisplayString(ra, cm)
			for _, b := range []int{0x00, 0x20, 0x40, 0x41, 0xC1, 0xF0, 0xFF, a >> 8, a & 0xFF} {
				rb := []byte{byte(b)}
				got := decode.CompareDisplay(ra, rb, cm)
				if want := gojq.Compare(sa, strs[b]); got != want {
					t.Fatalf("%s: CompareDisplay(%x, %x) = %d, want %d", codepage, ra, rb, got, want)
				}
			}
		}
	}
}

// structuralCopybooks are layouts whose structure, rather than scalars,
// is unusual: the ODO table inside a group inside OCCURS 1, an ODO table of
// FILLER-only groups, a REDEFINES over the ODO table, groups with a
// picture, empty groups, SYNC slack bytes, duplicates of different shapes,
// a counter inside a table, and three levels of OCCURS.
var structuralCopybooks = map[string]string{
	"odo in occurs 1": `
01 REC.
   05 N PIC 9.
   05 G OCCURS 1.
      10 A PIC X.
      10 T OCCURS 0 TO 3 DEPENDING ON N.
         15 V PIC S9 COMP-3.
         15 W PIC X OCCURS 2.
`,
	"odo of fillers": `
01 REC.
   05 N PIC 9.
   05 A PIC X.
   05 G.
      10 T OCCURS 0 TO 3 DEPENDING ON N.
         15 FILLER PIC X.
`,
	"redefines over odo": `
01 REC.
   05 N PIC 9.
   05 T OCCURS 1 TO 3 DEPENDING ON N PIC X.
   05 R REDEFINES T PIC X(2).
`,
	"picture groups, empty groups, sync": `
01 REC.
   05 G PIC X(4).
      10 A PIC X.
   05 E.
      10 FILLER PIC X(2).
   05 ET OCCURS 2.
      10 FILLER PIC X.
   05 C PIC X.
   05 B PIC S9(4) COMP SYNC.
   05 D PIC X.
   05 B8 PIC S9(9) COMP SYNC.
`,
	"duplicates of different shapes": `
01 REC.
   05 G.
      10 A PIC X.
      10 A OCCURS 2 PIC 9.
   05 H OCCURS 2.
      10 A PIC X.
   05 H.
      10 A PIC 9.
      10 B PIC X.
   05 G PIC X(2).
`,
	"counter in a table": `
01 REC.
   05 H OCCURS 2.
      10 N PIC 9.
   05 T OCCURS 0 TO 2 DEPENDING ON N PIC X(2).
`,
	"three levels": `
01 REC.
   05 L1 OCCURS 2.
      10 K PIC X.
      10 L2 OCCURS 2.
         15 L3 OCCURS 2.
            20 V PIC 9.
            20 W PIC X.
         15 J PIC S9 SIGN LEADING SEPARATE.
`,
	"separate sign counter": `
01 REC.
   05 N PIC S9 SIGN TRAILING SEPARATE.
   05 T OCCURS 0 TO 3 DEPENDING ON N.
      10 V PIC X.
`,
}

func TestStructuralLayoutsMatchDecodeDisplay(t *testing.T) {
	names := make([]string, 0, len(structuralCopybooks))
	for name := range structuralCopybooks {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		for _, fillers := range []bool{false, true} {
			d := queryViewDecoderCP(t, structuralCopybooks[name], "cp037", fillers)
			view := d.QueryView()
			rng := rand.New(rand.NewPCG(13, uint64(len(name))))
			accepted := 0
			var previous [][]byte
			alphabet := []byte{0xF0, 0xF1, 0xF2, 0xF3, 0x40, 0x00, 0xC1, 0x4E, 0x60, 0x1C, 0x0D, 0xFF}
			for range 6000 {
				rec := make([]byte, rng.IntN(d.Rec.MaxLength+4))
				for j := range rec {
					rec[j] = alphabet[rng.IntN(len(alphabet))]
				}
				if checkQueryView(t, d, view, rec) {
					accepted++
					for _, other := range previous {
						checkScalarOrder(t, view, other, 0, rec, 0, view.Root())
						checkScalarOrder(t, view, rec, 0, other, 0, view.Root())
					}
					previous = append(previous, rec)
					if len(previous) > 4 {
						previous = previous[1:]
					}
				}
			}
			if accepted == 0 {
				t.Errorf("%s: no record accepted", name)
			}
		}
	}
}

// FuzzScalarsEveryKind holds decoding and CompareScalar of every field of
// exhaustiveScalars, in every codepage, to DecodeDisplay and gojq.Compare
// for whatever two values the fuzzer finds.
func FuzzScalarsEveryKind(f *testing.F) {
	var views []*QueryView
	for _, codepage := range exhaustiveCodepages {
		views = append(views, queryViewDecoderCP(f, exhaustiveScalars, codepage, false).QueryView())
	}
	f.Add([]byte{0xF1, 0xF2, 0xC3}, []byte{0xF1, 0xF2, 0xD3})
	f.Add([]byte{0x12, 0x3C}, []byte{0x12, 0x3D})
	f.Add(bytes.Repeat([]byte{0x40}, 70), bytes.Repeat([]byte{0xF0}, 70))
	f.Add(bytes.Repeat([]byte{0x99}, 40), bytes.Repeat([]byte{0x09}, 40))
	f.Fuzz(func(t *testing.T, a, b []byte) {
		for _, view := range views {
			for _, s := range scalarShapes(view.Root()) {
				n := s.Field.Length
				if len(a) < n || len(b) < n {
					continue
				}
				// Right-aligned too, so short inputs reach the sign end.
				checkCompareScalar(t, view, s, a[:n], b[:n])
				checkCompareScalar(t, view, s, a[len(a)-n:], b[len(b)-n:])
			}
		}
	})
}
