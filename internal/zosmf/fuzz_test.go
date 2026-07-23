package zosmf

import (
	"bytes"
	"context"
	"testing"
)

// FuzzDecodeRecordPage feeds hostile bytes to the record-frame parser — the
// first thing that touches a data set download from the network. Malformed
// headers, lying lengths, and truncated payloads must come back as errors,
// never as panics or unbounded allocations.
func FuzzDecodeRecordPage(f *testing.F) {
	frame := func(payload string) []byte {
		var buf bytes.Buffer
		buf.Write([]byte{0, 0, 0, byte(len(payload))})
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
		records, err := decodeRecordPage(context.Background(), bytes.NewReader(data), 0, 100)
		if err != nil {
			return
		}
		for _, record := range records {
			if int64(len(record.Data)) > maxRecordSize {
				t.Fatalf("record %d exceeds the size limit: %d bytes", record.Number, len(record.Data))
			}
		}
	})
}
