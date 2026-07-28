package zosmf

import (
	"bytes"
	"io"
	"strings"
	"testing"
)

func TestHostTextStringPolicy(t *testing.T) {
	for _, tc := range []struct {
		name        string
		contentType string
		body        []byte
		want        string
	}{
		{"ascii undeclared", "", []byte("PLAIN"), "PLAIN"},
		{"utf8 undeclared", "text/plain", []byte("ÆØÅ"), "ÆØÅ"},
		{"latin1 undeclared", "", []byte{0xC6, 0xD8, 0xC5}, "ÆØÅ"},
		{"latin1 declared", "text/plain; charset=iso-8859-1", []byte{0xC6}, "Æ"},
		// A declared 8859-1 body whose bytes happen to validate as UTF-8
		// must still be widened — the header outranks the heuristic. (0x85
		// is the NEL control in 8859-1, not the windows-1252 ellipsis.)
		{"latin1 declared, utf8-shaped", "text/plain; charset=ISO-8859-1", []byte{0xC3, 0x85}, "Ã" + string(rune(0x85))},
		{"utf8 declared", "text/plain; charset=UTF-8", []byte("Å"), "Å"},
		{"unparsable header", ";;;", []byte{0xD8}, "Ø"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := hostTextString(tc.body, tc.contentType); got != tc.want {
				t.Fatalf("hostTextString = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestHostTextReaderSniffBoundarySplitRune pins the sniff's trim: a UTF-8
// rune straddling the peek limit must not tip an undeclared stream into the
// ISO 8859-1 interpretation.
func TestHostTextReaderSniffBoundarySplitRune(t *testing.T) {
	body := bytes.Repeat([]byte{'A'}, hostTextSniffLimit-1)
	body = append(body, []byte("Ø END")...) // Ø's two bytes straddle the limit
	reader := hostTextReader(io.NopCloser(bytes.NewReader(body)), "")
	content, err := io.ReadAll(reader)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(string(content), "Ø END") {
		t.Fatalf("stream tail = %q, want the split rune preserved", string(content)[len(content)-16:])
	}
}
