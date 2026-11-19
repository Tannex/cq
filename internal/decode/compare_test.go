package decode

import (
	"math/rand/v2"
	"strings"
	"testing"
)

func checkCompareDisplay(t *testing.T, cm *Charmap, a, b []byte) {
	t.Helper()
	want := strings.Compare(DisplayString(a, cm), DisplayString(b, cm))
	if got := CompareDisplay(a, b, cm); got != want {
		t.Fatalf("%s: CompareDisplay(%x, %x) = %d, want %d", cm.Name(), a, b, got, want)
	}
}

// CompareDisplay must order exactly as the decoded strings do, for every
// codepage: EBCDIC byte order is not decoded order, and trailing spaces,
// control characters and unmapped bytes all change the decoded text.
func TestCompareDisplayMatchesDisplayString(t *testing.T) {
	rng := rand.New(rand.NewPCG(7, 11))
	for _, cm := range codepages {
		for i := range 256 {
			for j := range 256 {
				checkCompareDisplay(t, cm, []byte{byte(i)}, []byte{byte(j)})
			}
		}
		for range 50000 {
			// A small alphabet makes shared prefixes and trailing spaces common.
			alphabet := []byte{0x00, 0x15, 0x20, 0x40, 0x4B, 0x5A, 0x81, 0xC1, 0xF0, 0xFF, byte(rng.IntN(256))}
			a, b := make([]byte, rng.IntN(8)), make([]byte, rng.IntN(8))
			for k := range a {
				a[k] = alphabet[rng.IntN(len(alphabet))]
			}
			for k := range b {
				b[k] = alphabet[rng.IntN(len(alphabet))]
			}
			checkCompareDisplay(t, cm, a, b)
			checkCompareDisplay(t, cm, a, a)
		}
	}
}

func FuzzCompareDisplay(f *testing.F) {
	f.Add([]byte("AB "), []byte("AB\x00"))
	f.Add([]byte{0x40, 0xC1}, []byte{0xC1, 0x40})
	f.Fuzz(func(t *testing.T, a, b []byte) {
		for _, cm := range codepages {
			checkCompareDisplay(t, cm, a, b)
		}
	})
}
