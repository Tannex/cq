package compaz

import (
	"context"
	"encoding/binary"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"testing"

	"github.com/Tannex/cq/internal/query"
)

func reviewBulkFile(t *testing.T, count int) (*overlay, string) {
	t.Helper()
	ov, err := buildOverlay(context.Background(), CopybookSource{Local: "benchmark.cpy", Format: "free"}, "latin1", nil,
		func(context.Context, string) ([]byte, error) { return []byte(benchmarkQueryCopybook), nil }, nil)
	if err != nil {
		t.Fatal(err)
	}
	var data []byte
	for i := range count {
		record := benchmarkQueryRecord(i)
		data = binary.BigEndian.AppendUint32(data, uint32(len(record)))
		data = append(data, record...)
	}
	path := filepath.Join(t.TempDir(), "query.records")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	return ov, path
}

// The mapping outlives the cache's interest while a query still holds it,
// and goes away with the last holder; a dropped cache hands out nothing.
func TestBulkFileLifetime(t *testing.T) {
	ov, path := reviewBulkFile(t, 3000)
	bulk := newBulkFile(path)
	held, err := bulk.open(context.Background(), ov)
	if err != nil {
		t.Fatal(err)
	}
	again, err := bulk.open(context.Background(), ov)
	if err != nil || again != held {
		t.Fatalf("second open: %v, same set %v", err, again == held)
	}
	again.release()
	bulk.drop()
	_ = os.Remove(path) // what dropBulkRecords does next
	if _, err := bulk.open(context.Background(), ov); !errors.Is(err, errBulkDropped) {
		t.Fatalf("open after drop: %v", err)
	}
	compiled, _ := query.Compile(`map(.ID) | add`)
	result := held.Query(context.Background(), compiled, 10)
	if result.Err != nil || len(result.Lines) != 1 || result.Lines[0] != strconv.Itoa(3000*2999/2) {
		t.Fatalf("query on a set the cache dropped: %+v", result)
	}
	if refs := held.refs.Load(); refs != 1 {
		t.Fatalf("refs = %d before the last release, want 1", refs)
	}
	held.release()
	if refs := held.refs.Load(); refs != 0 {
		t.Fatalf("refs = %d after the last release", refs)
	}
}

// Queries racing a drop (refresh, quit) and a copybook change: every query
// either fails cleanly with errBulkDropped or completes with the right answer.
func TestBulkFileConcurrentOpenDrop(t *testing.T) {
	ov, path := reviewBulkFile(t, 20000)
	other, _ := reviewBulkFile(t, 1) // same copybook, a different decoder
	compiled, _ := query.Compile(`.[] | select(.ID % 5000 == 0) | .ID`)
	for round := range 20 {
		bulk := newBulkFile(path)
		var wg sync.WaitGroup
		for worker := range 6 {
			wg.Add(1)
			go func() {
				defer wg.Done()
				use := ov
				if worker%3 == 2 {
					use = other
				}
				set, err := bulk.open(context.Background(), use)
				if errors.Is(err, errBulkDropped) {
					return
				}
				if err != nil {
					t.Error(err)
					return
				}
				defer set.release()
				result := set.Query(context.Background(), compiled, 10)
				if result.Err != nil || len(result.Lines) != 4 {
					t.Errorf("round %d: %+v", round, result)
				}
			}()
		}
		if round%2 == 0 {
			bulk.drop()
		}
		wg.Wait()
		bulk.drop()
	}
}
