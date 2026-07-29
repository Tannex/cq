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

// TestHostTextPathsAgreeBeyondSniffLimit pins the paged/stream parity: an
// undeclared body whose first non-UTF-8 byte sits past the sniff limit must
// decode identically through hostTextString (paged reads) and
// hostTextReader (the bulk stream). Both commit to UTF-8 on the prefix and
// pass the late byte through untouched; a whole-body check on either side
// would flip just that side to ISO 8859-1 and desynchronize the two views
// of the same spool file.
func TestHostTextPathsAgreeBeyondSniffLimit(t *testing.T) {
	body := bytes.Repeat([]byte{'A'}, hostTextSniffLimit)
	body = append(body, 0xD8, ' ', 'E', 'N', 'D') // 0xD8 alone is not valid UTF-8

	buffered := hostTextString(body, "")
	streamed, err := io.ReadAll(hostTextReader(io.NopCloser(bytes.NewReader(body)), ""))
	if err != nil {
		t.Fatal(err)
	}
	if buffered != string(streamed) {
		t.Fatalf("paths disagree: buffered tail %q, streamed tail %q",
			buffered[len(buffered)-8:], streamed[len(streamed)-8:])
	}
	if !strings.HasSuffix(buffered, "\xD8 END") {
		t.Fatalf("late byte not passed through raw: tail %q", buffered[len(buffered)-8:])
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
