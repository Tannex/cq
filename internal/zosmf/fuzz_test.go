package zosmf

import (
	"bytes"
	"context"
	"encoding/binary"
	"testing"
)

// FuzzDecodeRecordPage feeds hostile bytes to the record-frame parser — the
// first thing that touches a data set download from the network. Malformed
// headers, lying lengths, and truncated payloads must come back as errors,
// never as panics or unbounded allocations.
func FuzzDecodeRecordPage(f *testing.F) {
	frame := func(payload string) []byte {
		var header [4]byte
		binary.BigEndian.PutUint32(header[:], uint32(len(payload)))
		var buf bytes.Buffer
		buf.Write(header[:])
		buf.WriteString(payload)
		return buf.Bytes()
	}
	f.Add(frame("HELLO"))
	f.Add(append(frame("ONE"), frame("TWO")...))
	f.Add([]byte{})
	f.Add([]byte{0, 0, 0})                    // truncated header
	f.Add([]byte{0xFF, 0xFF, 0xFF, 0xFF})     // length far past the limit
	f.Add([]byte{0, 0, 0, 9, 'S', 'H', 'O'})  // payload shorter than declared
	f.Add(append(frame(""), frame("")...))    // zero-length records
	f.Add(bytes.Repeat(frame("PADDED  "), 8)) // many small frames

	f.Fuzz(func(t *testing.T, data []byte) {
		const maxItems = 100
		records, err := decodeRecordPage(context.Background(), bytes.NewReader(data), 0, maxItems)
		if err != nil {
			return
		}
		// Oversized single records error before appending, so the invariants
		// a parser bug could actually break are the caller's item budget and
		// the cumulative page allocation bound.
		if len(records) > maxItems {
			t.Fatalf("returned %d records for a budget of %d", len(records), maxItems)
		}
		total := int64(0)
		for _, record := range records {
			total += 4 + int64(len(record.Data))
		}
		if total > maxRecordPageBytes {
			t.Fatalf("page holds %d bytes, over the %d limit", total, int64(maxRecordPageBytes))
		}
	})
}
