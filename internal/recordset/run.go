package recordset

import (
	"context"
	"errors"
	"fmt"
	"runtime"
	"sync"
	"sync/atomic"

	"github.com/Tannex/cq/internal/query"
)

// Result is the outcome of one query over a Set.
type Result struct {
	Lines    []string // outputs as compact JSON, at most the line cap
	Matches  int      // all outputs, including those past the cap
	Err      error    // the runtime error that ended the query, if any
	Canceled bool     // the context ended the query
}

// chunkRecords is how many records one unit of parallel work covers: large
// enough that scheduling is noise, small enough that an early error or a
// reached line cap stops the other workers promptly.
const chunkRecords = 8192

// Workers is the number of goroutines a row-wise query uses. Tests lower it.
var Workers = runtime.GOMAXPROCS

// Query evaluates q with the set as its input array and keeps at most
// lineCap output lines. A row-wise expression is evaluated on all cores;
// the result is the same as evaluating it over the whole array at once.
func (s *Set) Query(ctx context.Context, q *query.Query, lineCap int) Result {
	workers := Workers(0)
	if q.RowWise() && workers > 1 && s.count > chunkRecords {
		return s.queryChunks(ctx, q, lineCap, workers)
	}
	var result Result
	finish(ctx, &result, run(ctx, q, s.Array(), lineCap, nil, &result.Lines, &result.Matches))
	return result
}

// run evaluates q over one array, appending up to lineCap lines. countOnly,
// when set, stops rendering lines once it reports true.
func run(ctx context.Context, q *query.Query, input *Array, lineCap int, countOnly *atomic.Bool, lines *[]string, matches *int) (err error) {
	// A fault in the engine must end this query, not the program: workers
	// run outside any caller's recovery.
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("query engine fault: %v", r)
		}
	}()
	return q.RunContext(ctx, input, func(out any) error {
		*matches++
		if len(*lines) < lineCap && (countOnly == nil || !countOnly.Load()) {
			*lines = append(*lines, query.Marshal(out))
		}
		return nil
	})
}

// finish classifies the error that ended a run. halt is not an error, and a
// cancelled context outranks whatever the interrupted evaluation reported.
func finish(ctx context.Context, result *Result, err error) {
	if err == nil {
		return
	}
	var halt *query.Halt
	switch {
	case errors.As(err, &halt):
	case ctx.Err() != nil:
		result.Canceled = true
	default:
		result.Err = err
	}
}

type chunkResult struct {
	lines   []string
	matches int
	err     error
	done    bool
}

// queryChunks evaluates a row-wise query chunk by chunk on several workers
// and assembles the chunks in order. A chunk that ends in an error (halt
// included) ends the query there, exactly as it would sequentially: earlier
// chunks are complete, later ones are discarded.
func (s *Set) queryChunks(ctx context.Context, q *query.Query, lineCap, workers int) Result {
	chunks := (s.count + chunkRecords - 1) / chunkRecords
	workers = min(workers, chunks)
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	var (
		mu       sync.Mutex
		ready    = sync.NewCond(&mu)
		results  = make([]chunkResult, chunks)
		next     atomic.Int64 // next chunk to hand out
		capped   atomic.Bool  // the merged result holds lineCap lines
		stopping atomic.Bool  // a chunk ended the query; start no more
		wg       sync.WaitGroup
	)
	for range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for !stopping.Load() {
				chunk := int(next.Add(1)) - 1
				if chunk >= chunks {
					return
				}
				var r chunkResult
				input := s.Range(chunk*chunkRecords, min(s.count, (chunk+1)*chunkRecords))
				r.err = run(runCtx, q, input, lineCap, &capped, &r.lines, &r.matches)
				r.done = true
				mu.Lock()
				results[chunk] = r
				mu.Unlock()
				ready.Broadcast()
			}
		}()
	}

	var result Result
	var failure error
	mu.Lock()
	for chunk := 0; chunk < chunks; chunk++ {
		for !results[chunk].done {
			ready.Wait()
		}
		r := results[chunk]
		results[chunk] = chunkResult{done: true} // release the lines early
		result.Matches += r.matches
		if room := lineCap - len(result.Lines); room > 0 {
			result.Lines = append(result.Lines, r.lines[:min(room, len(r.lines))]...)
		}
		if len(result.Lines) >= lineCap {
			capped.Store(true)
		}
		if r.err != nil {
			failure = r.err
			break
		}
	}
	mu.Unlock()
	// Workers may still be evaluating chunks past a failed one; the merged
	// result no longer depends on them.
	stopping.Store(true)
	if failure != nil {
		cancel()
	}
	wg.Wait()
	// The parent context decides whether a failure was a cancellation: the
	// derived one is cancelled by this function itself.
	finish(ctx, &result, failure)
	return result
}
