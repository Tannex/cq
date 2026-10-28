package compaz

import (
	"bufio"
	"context"
	"encoding/binary"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/Tannex/cq/internal/query"
)

// The fixture is 192 bytes per record (196 including its frame header): text,
// display decimals, a 20-digit integer, packed decimal, a group, and an OCCURS
// group. IDs vary; four regions and a 10% active subset make the query outputs
// deterministic. The cached-file benchmarks exclude download and UI rendering.
const benchmarkQueryCopybook = `01 TRANSACTION-RECORD.
 05 ID PIC 9(8).
 05 NAME PIC X(32).
 05 REGION PIC X(4).
 05 STATUS PIC X.
 05 AMOUNT PIC 9(7)V99.
 05 BIGID PIC 9(20).
 05 BALANCE PIC S9(9)V99 COMP-3.
 05 CUSTOMER.
  10 CITY PIC X(24).
  10 TIER PIC 9(2).
 05 ITEMS OCCURS 3 TIMES.
  10 CODE PIC X(8).
  10 QUANTITY PIC 9(3).
  10 COST PIC 9(5)V99.
 05 DESCRIPTION PIC X(32).
`

func benchmarkQueryOverlay(b *testing.B) *overlay {
	b.Helper()
	ov, err := buildOverlay(context.Background(), CopybookSource{
		Local: "benchmark.cpy", Format: "free",
	}, "latin1", nil, func(context.Context, string) ([]byte, error) {
		return []byte(benchmarkQueryCopybook), nil
	}, nil)
	if err != nil {
		b.Fatal(err)
	}
	return ov
}

func benchmarkQueryRecord(i int) []byte {
	status := 'I'
	if i%10 == 0 {
		status = 'A'
	}
	data := fmt.Appendf(nil, "%08d%-32sR%03d%c%09d%020d",
		i, fmt.Sprintf("Customer %06d", i), i%4, status, i%10000*125,
		uint64(12345678901234567890)+uint64(i))
	// The signed packed-decimal value is 12345678.90.
	data = append(data, 0x01, 0x23, 0x45, 0x67, 0x89, 0x0c)
	data = fmt.Appendf(data, "%-24s%02d", "Copenhagen", i%5)
	for j := range 3 {
		data = fmt.Appendf(data, "ITEM%04d%03d%07d", j, j+1, (j+1)*125)
	}
	return fmt.Appendf(data, "%-32s", "Synthetic query benchmark")
}

func benchmarkQueryFile(b *testing.B, count int) string {
	b.Helper()
	path := filepath.Join(b.TempDir(), "query.records")
	f, err := os.Create(path)
	if err != nil {
		b.Fatal(err)
	}
	w := bufio.NewWriterSize(f, 256<<10)
	for i := range count {
		data := benchmarkQueryRecord(i)
		if len(data) != 192 {
			b.Fatalf("fixture record has %d bytes, want 192", len(data))
		}
		var header [4]byte
		binary.BigEndian.PutUint32(header[:], uint32(len(data)))
		if _, err := w.Write(header[:]); err != nil {
			b.Fatal(err)
		}
		if _, err := w.Write(data); err != nil {
			b.Fatal(err)
		}
	}
	if err := w.Flush(); err != nil {
		b.Fatal(err)
	}
	if err := f.Close(); err != nil {
		b.Fatal(err)
	}
	return path
}

func BenchmarkDecodeQueryValue(b *testing.B) {
	ov := benchmarkQueryOverlay(b)
	data := benchmarkQueryRecord(0)
	var value any
	var msg queryEvalMsg
	var ok bool
	b.ReportAllocs()
	b.SetBytes(int64(len(data)))
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		value, ok = decodeQueryValue(ov, data, &msg)
		if !ok {
			b.Fatalf("decode failed: errors=%d lastError=%q", msg.Errors, msg.LastError)
		}
	}
	b.StopTimer()
	// Query the result to check both shape and preservation of large integers.
	compiled, err := query.Compile(`{ID, BIGID, BALANCE, items: (.ITEMS | length), city: .CUSTOMER.CITY}`)
	if err != nil {
		b.Fatal(err)
	}
	var got string
	if err := compiled.Run(value, func(v any) error {
		got = query.Marshal(v)
		return nil
	}); err != nil {
		b.Fatal(err)
	}
	const want = `{"BALANCE":12345678.90,"BIGID":12345678901234567890,"ID":0,"city":"Copenhagen","items":3}`
	if got != want {
		b.Fatalf("decoded fixture = %s, want %s", got, want)
	}
}

func BenchmarkQueryArrayFile(b *testing.B) {
	ov := benchmarkQueryOverlay(b)
	for _, count := range []int{10_000, 100_000} {
		b.Run(strconv.Itoa(count), func(b *testing.B) {
			path := benchmarkQueryFile(b, count)
			for _, tc := range []struct {
				name    string
				expr    string
				matches int
				check   func(*testing.B, []string)
			}{
				{
					name: "count", expr: `length`, matches: 1,
					check: func(b *testing.B, lines []string) {
						if lines[0] != strconv.Itoa(count) {
							b.Fatalf("count = %s, want %d", lines[0], count)
						}
					},
				},
				{
					name: "select_projection", expr: `.[] | select(.STATUS == "A") | {ID, NAME, AMOUNT, BIGID}`, matches: count / 10,
					check: func(b *testing.B, lines []string) {
						const want = `{"AMOUNT":0.00,"BIGID":12345678901234567890,"ID":0,"NAME":"Customer 000000"}`
						if lines[0] != want || !strings.Contains(lines[len(lines)-1], fmt.Sprintf(`"ID":%d,`, count-10)) {
							b.Fatalf("projection endpoints = %s / %s", lines[0], lines[len(lines)-1])
						}
					},
				},
				{
					name: "whole_records", expr: `.[]`, matches: count,
					check: func(b *testing.B, lines []string) {
						if !strings.Contains(lines[0], `"BIGID":12345678901234567890,`) ||
							!strings.Contains(lines[0], `"ITEMS":[{"CODE":"ITEM0000","COST":1.25,"QUANTITY":1},`) ||
							!strings.Contains(lines[len(lines)-1], fmt.Sprintf(`"ID":%d,`, count-1)) {
							b.Fatalf("whole-record endpoints = %s / %s", lines[0], lines[len(lines)-1])
						}
					},
				},
				{
					name: "unique", expr: `map(.REGION) | unique`, matches: 1,
					check: func(b *testing.B, lines []string) {
						const want = `["R000","R001","R002","R003"]`
						if lines[0] != want {
							b.Fatalf("unique regions = %s, want %s", lines[0], want)
						}
					},
				},
				{
					name: "unique_records", expr: `unique | length`, matches: 1,
					check: func(b *testing.B, lines []string) {
						if lines[0] != strconv.Itoa(count) {
							b.Fatalf("unique records = %s, want %d", lines[0], count)
						}
					},
				},
				{
					name: "group_count", expr: `group_by(.REGION) | map({region: .[0].REGION, count: length})`, matches: 1,
					check: func(b *testing.B, lines []string) {
						want := fmt.Sprintf(`[{"count":%d,"region":"R000"},{"count":%d,"region":"R001"},{"count":%d,"region":"R002"},{"count":%d,"region":"R003"}]`, count/4, count/4, count/4, count/4)
						if lines[0] != want {
							b.Fatalf("groups = %s, want %s", lines[0], want)
						}
					},
				},
			} {
				b.Run(tc.name, func(b *testing.B) {
					compiled, err := query.Compile(tc.expr)
					if err != nil {
						b.Fatal(err)
					}
					cmd := evalQueryArrayFile(context.Background(), compiled, ov, path, "benchmark", 1, queryResultCap)
					var msg queryEvalMsg
					b.ReportAllocs()
					b.SetBytes(int64(count * 192))
					b.ResetTimer()
					for i := 0; i < b.N; i++ {
						msg = cmd().(queryEvalMsg)
						if msg.Canceled || msg.Errors != 0 || msg.LastError != "" || msg.Evaluated != count || msg.Matches != tc.matches || len(msg.Lines) != tc.matches {
							b.Fatalf("query result: evaluated=%d matches=%d lines=%d errors=%d lastError=%q canceled=%v", msg.Evaluated, msg.Matches, len(msg.Lines), msg.Errors, msg.LastError, msg.Canceled)
						}
					}
					b.StopTimer()
					tc.check(b, msg.Lines)
				})
			}
		})
	}
}
