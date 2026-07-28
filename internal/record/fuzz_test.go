package record

import (
	"bytes"
	"testing"

	"github.com/Tannex/cq/internal/copybook"
	"github.com/Tannex/cq/internal/decode"
	"github.com/Tannex/cq/internal/layout"
)

// fuzzCopybook exercises every decode kind plus the hazardous shapes: packed
// and zoned sign nibbles, binary, float, numeric-edited, a level-88 for the
// WHERE path, and an ODO whose counter comes from the (hostile) data itself.
const fuzzCopybook = `
01 REC.
   05 NAME PIC X(8).
   05 CNT PIC 9(2).
   05 AMOUNT PIC S9(5)V99 COMP-3.
   05 BAL PIC S9(4) COMP.
   05 RATE COMP-2.
   05 EDITED PIC ZZ,ZZ9.99-.
   05 SGN PIC S9(3) SIGN LEADING SEPARATE.
   05 FLAG PIC X.
      88 FLAG-ON VALUE "Y".
   05 ITEM PIC 9(3) OCCURS 1 TO 4 TIMES DEPENDING ON CNT.
`

// FuzzDecoderHostileRecords streams arbitrary bytes through the full decode
// pipeline data set contents take: framing via Next (including the
// ODO-driven variable record length), the level-88 WHERE evaluation, and
// Decode itself. Garbage must surface as errors, never as panics.
func FuzzDecoderHostileRecords(f *testing.F) {
	items, err := copybook.Parse(fuzzCopybook, copybook.FormatFree)
	if err != nil {
		f.Fatal(err)
	}
	recs, err := layout.Build(items)
	if err != nil {
		f.Fatal(err)
	}
	cm, err := decode.Codepage("cp037")
	if err != nil {
		f.Fatal(err)
	}
	d, err := NewDecoder(recs[0], cm)
	if err != nil {
		f.Fatal(err)
	}
	d.IncludeFillers = true
	if err := d.AddWhere("FLAG-ON"); err != nil {
		f.Fatal(err)
	}

	f.Add([]byte{})
	f.Add(bytes.Repeat([]byte{0x40}, 64))                     // EBCDIC spaces
	f.Add(bytes.Repeat([]byte{0xF0}, 64))                     // EBCDIC zeros
	f.Add(bytes.Repeat([]byte{0xFF}, 64))                     // invalid everywhere
	f.Add(bytes.Repeat([]byte{0x00}, 128))                    // NULs; CNT decodes invalid
	f.Add(append(bytes.Repeat([]byte{0xF9}, 10), 0x0C, 0x0D)) // hostile sign nibbles

	f.Fuzz(func(t *testing.T, data []byte) {
		reader := bytes.NewReader(data)
		for range 64 {
			raw, err := d.Next(reader)
			if err != nil {
				break
			}
			_, _ = d.Matches(raw)
			_, _ = d.Decode(raw)
			// DecodeDisplay is the parallel typed decode the copybook
			// overlay renders with; its invalid-byte formatters exist
			// precisely for hostile input, so they must be fuzzed too.
			_, _ = d.DecodeDisplay(raw)
		}
	})
}
