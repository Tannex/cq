package recordset

import (
	"context"
	"fmt"
	"math/rand/v2"
	"sort"
	"testing"

	"github.com/Tannex/cq/internal/gojq"
	"github.com/Tannex/cq/internal/query"
	"github.com/Tannex/cq/internal/record"
)

// structuralCopybooks are layouts with unusual structure: the ODO table
// inside a group inside OCCURS 1, an ODO table of FILLER-only groups, a
// REDEFINES over the ODO table, groups with a picture, empty groups, SYNC,
// duplicates of different shapes, a counter inside a table, three levels of
// OCCURS, and numbers whose bytes differ while their values are equal.
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
	"equal values, different bytes": `
01 REC.
   05 Z PIC S9V9.
   05 P PIC S9 COMP-3.
   05 U PIC 9.
   05 X PIC X(2).
   05 G OCCURS 2.
      10 GZ PIC S9.
      10 GX PIC X.
`,
}

var structuralExpressions = []string{
	`sort | map(tojson)`,
	`unique | length`,
	`unique | map(tojson)`,
	`[.[] | to_entries[] | .value] | unique | map(tojson)`,
	`[.[] | .[]] | sort | map(tojson)`,
	`[.[] | .. ] | unique | map(tojson)`,
	`[range(length) as $i | range(length) as $j | (.[$i] < .[$j])] | map(tostring) | join("")`,
	`[range(length) as $i | range(length) as $j | (.[$i] == .[$j])] | map(tostring) | join("")`,
	`min | tojson`, `max | tojson`,
	`group_by(.) | map(length)`,
}

func structuralRecords(rng *rand.Rand, maxLength int) [][]byte {
	alphabet := []byte{0xF0, 0xF1, 0xF2, 0x40, 0x00, 0xC1, 0xC0, 0xD0, 0x4E, 0x60, 0x0C, 0x0D, 0x1C, 0x0F, 0xFF}
	var records [][]byte
	for len(records) < 40 {
		var rec []byte
		if len(records) > 0 && rng.IntN(2) == 0 {
			rec = append([]byte{}, records[rng.IntN(len(records))]...)
			if len(rec) > 0 {
				rec[rng.IntN(len(rec))] = alphabet[rng.IntN(len(alphabet))]
			}
			if rng.IntN(4) == 0 && len(rec) > 0 {
				rec = rec[:len(rec)-1]
			}
			if rng.IntN(4) == 0 {
				rec = append(rec, alphabet[rng.IntN(len(alphabet))])
			}
		} else {
			rec = make([]byte, max(maxLength-rng.IntN(4), 0)+rng.IntN(2))
			for i := range rec {
				rec[i] = alphabet[rng.IntN(len(alphabet))]
			}
		}
		records = append(records, rec)
	}
	return records
}

// TestStructuralLayoutsMatchFullDecode holds lazy records of unusual layouts
// to their full decoding, member by member and through whole-array queries
// that compare records, groups and tables.
func TestStructuralLayoutsMatchFullDecode(t *testing.T) {
	names := make([]string, 0, len(structuralCopybooks))
	for name := range structuralCopybooks {
		names = append(names, name)
	}
	sort.Strings(names)
	Workers = func(int) int { return 1 }
	for _, name := range names {
		for _, fillers := range []bool{false, true} {
			view := nestedView(t, structuralCopybooks[name], fillers)
			rng := rand.New(rand.NewPCG(17, uint64(len(name))))
			used := 0
			for round := range 15 {
				records := structuralRecords(rng, view.Root().Field.Length)
				set := openSet(t, writeFrames(t, records), view)
				if set.Len() < 2 {
					continue
				}
				used++
				var accepted [][]byte
				for _, rec := range records {
					if view.Check(rec) == nil {
						accepted = append(accepted, rec)
					}
				}
				array := set.Array()
				for i := range set.Len() {
					for j := range set.Len() {
						checkNested(t, array.At(i), array.At(j), view.Value(accepted[i]), view.Value(accepted[j]),
							fmt.Sprintf("%s/%v/%d [%d|%d]", name, fillers, round, i, j))
					}
				}
				for _, expr := range structuralExpressions {
					compiled, err := query.Compile(expr)
					if err != nil {
						t.Fatal(err)
					}
					want := reference(t, view, records, expr, 10)
					if diff := sameResult(set.Query(context.Background(), compiled, 10), want); diff != "" {
						t.Fatalf("%s/%v/%d: %s: %s", name, fillers, round, expr, diff)
					}
				}
			}
			if used == 0 {
				t.Errorf("%s/%v: no set had two decodable records", name, fillers)
			}
		}
	}
}

// Nodes of two sets that share a view compare by their own bytes.
func TestNodesOfTwoSetsSharingAView(t *testing.T) {
	view := nestedView(t, structuralCopybooks["three levels"], false)
	rng := rand.New(rand.NewPCG(19, 23))
	for range 20 {
		recordsA := structuralRecords(rng, view.Root().Field.Length)
		recordsB := structuralRecords(rng, view.Root().Field.Length)
		setA := openSet(t, writeFrames(t, recordsA), view)
		setB := openSet(t, writeFrames(t, recordsB), view)
		var acceptedA, acceptedB [][]byte
		for _, rec := range recordsA {
			if view.Check(rec) == nil {
				acceptedA = append(acceptedA, rec)
			}
		}
		for _, rec := range recordsB {
			if view.Check(rec) == nil {
				acceptedB = append(acceptedB, rec)
			}
		}
		for i := range setA.Len() {
			for j := range setB.Len() {
				a, b := setA.Array().At(i).(*Record), setB.Array().At(j).(*Record)
				wantA, wantB := view.Value(acceptedA[i]), view.Value(acceptedB[j])
				if got, want := gojq.Compare(a, b), gojq.Compare(wantA, wantB); got != want {
					t.Fatalf("records: %d, want %d", got, want)
				}
				checkNested(t, a.Get("L1"), b.Get("L1"), wantA["L1"], wantB["L1"], "L1")
			}
		}
	}
}

var _ = record.Decoder{}
