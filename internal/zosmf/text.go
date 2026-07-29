package zosmf

import (
	"bufio"
	"io"
	"mime"
	"strings"
	"unicode/utf8"

	"golang.org/x/text/encoding/charmap"
)

// z/OSMF converts EBCDIC text payloads (spool records, text-mode data sets)
// to a network codeset before sending. Which codeset is host-configuration
// dependent — ISO 8859-1 on many systems, UTF-8 on others — so a declared
// Content-Type charset is honored first, and bodies without one are sniffed:
// anything that validates as UTF-8 (pure ASCII included) passes through,
// anything else is read as ISO 8859-1. The ambiguity only matters for bytes
// beyond ASCII, where guessing wrong either widens UTF-8 pairs into mojibake
// or collapses 8859-1 bytes into replacement runes; validation picks the
// interpretation under which the payload is coherent.

// hostTextCharset extracts the charset a response declared, lowercased;
// empty when absent or unparsable.
func hostTextCharset(contentType string) string {
	if contentType == "" {
		return ""
	}
	_, params, err := mime.ParseMediaType(contentType)
	if err != nil {
		return ""
	}
	return strings.ToLower(params["charset"])
}

// hostTextString decodes one buffered text body per the policy above. The
// undeclared-charset sniff inspects the same bounded prefix as
// hostTextReader, so a spool file decodes identically whether it arrives
// through the paged reads or the bulk stream — a whole-body check here would
// let a non-UTF-8 byte past the sniff limit flip only the buffered path to
// ISO 8859-1.
func hostTextString(body []byte, contentType string) string {
	switch hostTextCharset(contentType) {
	case "utf-8", "utf8":
		return string(body)
	case "iso-8859-1", "iso8859-1", "latin-1", "latin1":
		return latin1String(body)
	}
	prefix := body[:min(len(body), hostTextSniffLimit)]
	if utf8.Valid(trimSplitRune(prefix)) {
		return string(body)
	}
	return latin1String(body)
}

// latin1String widens ISO 8859-1 bytes to runes; pure ASCII passes through
// allocation-free.
func latin1String(body []byte) string {
	for _, c := range body {
		if c >= 0x80 {
			var builder strings.Builder
			builder.Grow(len(body) + len(body)/8)
			for _, c := range body {
				builder.WriteRune(rune(c))
			}
			return builder.String()
		}
	}
	return string(body)
}

// hostTextSniffLimit bounds how much of an undeclared stream is inspected
// before committing to a charset.
const hostTextSniffLimit = 64 << 10

// hostTextReader applies hostTextString's policy to a streamed body. With no
// declared charset the first chunk is sniffed; a stream whose sniffed prefix
// is ASCII but that carries non-UTF-8 bytes later renders those bytes as
// replacement runes rather than risking mis-decoding genuine UTF-8.
func hostTextReader(body io.ReadCloser, contentType string) io.ReadCloser {
	latin1 := func(r io.Reader) io.ReadCloser {
		return &textBody{Reader: charmap.ISO8859_1.NewDecoder().Reader(r), Closer: body}
	}
	switch hostTextCharset(contentType) {
	case "utf-8", "utf8":
		return body
	case "iso-8859-1", "iso8859-1", "latin-1", "latin1":
		return latin1(body)
	}
	reader := bufio.NewReaderSize(body, hostTextSniffLimit)
	prefix, _ := reader.Peek(hostTextSniffLimit) // shorter at EOF; the error resurfaces on Read
	if utf8.Valid(trimSplitRune(prefix)) {
		return &textBody{Reader: reader, Closer: body}
	}
	return latin1(reader)
}

// trimSplitRune drops a trailing UTF-8 sequence the sniff limit cut in
// half, so a rune straddling the peek boundary does not fail validation.
// Only a genuinely incomplete-but-well-formed tail is trimmed — arbitrary
// invalid bytes near the end must keep counting against UTF-8, or short
// 8859-1 bodies would sniff as UTF-8 once their evidence was trimmed away.
func trimSplitRune(prefix []byte) []byte {
	if len(prefix) < hostTextSniffLimit {
		return prefix // the peek reached EOF: nothing can be split
	}
	for i := 1; i <= utf8.UTFMax-1 && i <= len(prefix); i++ {
		b := prefix[len(prefix)-i]
		if b&0xC0 == 0x80 {
			continue // continuation byte: keep walking to its lead
		}
		if b&0xC0 == 0xC0 && !utf8.FullRune(prefix[len(prefix)-i:]) {
			return prefix[:len(prefix)-i]
		}
		break // a complete rune or plain invalid tail: judge it as-is
	}
	return prefix
}

// textBody pairs a decoding reader with the network body's Close.
type textBody struct {
	io.Reader
	io.Closer
}
