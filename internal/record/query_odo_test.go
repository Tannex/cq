package record

import (
	"math/rand/v2"
	"testing"
)

// The DEPENDING ON table may be the trailing storage of a group rather than
// a top-level member (NewDecoder accepts it). Span then reports the group as
// fixed storage of its full declared size: it reaches past the end of a
// record holding fewer table entries, and its decoded value depends on the
// counter outside it, so equal bytes do not mean equal values.
const nestedODOCopybook = `
01 REC.
   05 ZCNT PIC 9.
   05 MID PIC X.
   05 G.
      10 T PIC X OCCURS 0 TO 3 TIMES DEPENDING ON ZCNT.
`

func TestQueryViewNestedODO(t *testing.T) {
	for _, fillers := range []bool{false, true} {
		d := queryViewDecoder(t, nestedODOCopybook, fillers)
		view := d.QueryView()
		rng := rand.New(rand.NewPCG(3, 5))
		for range 2000 {
			rec := make([]byte, rng.IntN(d.Rec.MaxLength+3))
			for j := range rec {
				rec[j] = 0xC1 + byte(rng.IntN(3))
			}
			if len(rec) > 0 {
				rec[0] = 0xF0 + byte(rng.IntN(4)) // a valid counter
			}
			checkQueryView(t, d, view, rec)
		}
	}
}

// Span must not call a member fixed when its value depends on bytes outside
// the span: two records whose G bytes agree but whose counters differ decode
// to different G values.
func TestQueryViewSpanCoversValue(t *testing.T) {
	d := queryViewDecoder(t, nestedODOCopybook, false)
	view := d.QueryView()
	// cp037: F2/F1 = "2"/"1", C1 = "A", C2 = "B", E7 E8 = "XY", 40 = " "
	left := []byte{0xF2, 0xC1, 0xE7, 0xE8, 0x40}
	right := []byte{0xF1, 0xC2, 0xE7, 0xE8, 0x40}
	g := view.Member("G")
	if start, end, fixed := view.Span(g); fixed {
		if string(left[start:end]) == string(right[start:end]) {
			l, r := view.Field(left, g), view.Field(right, g)
			t.Errorf("Span(G) = %d:%d is fixed and the bytes agree, but the values differ: %v vs %v", start, end, l, r)
		}
	}
}
