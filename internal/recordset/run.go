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

// Workers is the number of goroutines a parallel query uses. Tests lower it.
var Workers = runtime.GOMAXPROCS

// onStagedChunk is called as a worker starts a chunk of a staged query's
// per-element stage. Tests count the calls to prove the stage runs in
// parallel; falling back to one thread would otherwise go unnoticed.
var onStagedChunk = func() {}

// Query evaluates q with the set as its input array and keeps at most
// lineCap output lines. A row-wise expression, and the per-element stage of
// a staged one, is evaluated on all cores; the result is the same as
// evaluating the expression over the whole array at once.
func (s *Set) Query(ctx context.Context, q *query.Query, lineCap int) Result {
	if workers := Workers(0); workers > 1 && s.count > chunkRecords {
		if q.RowWise() {
			return s.queryChunks(ctx, q, lineCap, workers)
		}
		if staged := q.Staged(); staged != nil {
			return s.queryStaged(ctx, staged, lineCap, workers)
		}
	}
	var result Result
	finish(ctx, &result, run(ctx, q.RunContext, s.Array(), lineCap, nil, &result.Lines, &result.Matches))
	return result
}

type evaluator func(ctx context.Context, input any, emit func(any) error) error

// run evaluates over one input, appending up to lineCap lines. countOnly,
// when set, stops rendering lines once it reports true.
func run(ctx context.Context, eval evaluator, input any, lineCap int, countOnly *atomic.Bool, lines *[]string, matches *int) error {
	return guard(func() error {
		return eval(ctx, input, func(out any) error {
			*matches++
			if len(*lines) < lineCap && (countOnly == nil || !countOnly.Load()) {
				*lines = append(*lines, query.Marshal(out))
			}
			return nil
		})
	})
}

// guard runs an evaluation and reports a fault in the engine as its error.
// The fault must end this query, not the program: workers run outside any
// caller's recovery.
func guard(eval func() error) (err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("query engine fault: %v", r)
		}
	}()
	return eval()
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
	values  []any
	matches int
	err     error
	done    bool
}

// queryChunks evaluates a row-wise query chunk by chunk on several workers
// and assembles the chunks' lines in order.
func (s *Set) queryChunks(ctx context.Context, q *query.Query, lineCap, workers int) Result {
	var result Result
	var capped atomic.Bool // the merged result holds lineCap lines
	err := s.inChunks(ctx, workers, func(ctx context.Context, input *Array) (r chunkResult) {
		r.err = run(ctx, q.RunContext, input, lineCap, &capped, &r.lines, &r.matches)
		return r
	}, func(r chunkResult) {
		result.Matches += r.matches
		if room := lineCap - len(result.Lines); room > 0 {
			result.Lines = append(result.Lines, r.lines[:min(room, len(r.lines))]...)
		}
		if len(result.Lines) >= lineCap {
			capped.Store(true)
		}
	})
	finish(ctx, &result, err)
	return result
}

// queryStaged evaluates the per-element stage of a staged query chunk by
// chunk on several workers, then the rest of the query once, on the whole
// set, given the stage's outputs in order. A failing chunk ends the query
// before the rest runs, with the error of the earliest failing element: the
// one the sequential evaluation reports.
func (s *Set) queryStaged(ctx context.Context, staged *query.Staged, lineCap, workers int) Result {
	var (
		result Result
		parts  [][]any
		total  int
	)
	err := s.inChunks(ctx, workers, func(ctx context.Context, input *Array) (r chunkResult) {
		onStagedChunk()
		r.err = guard(func() error {
			return staged.Each(ctx, input, func(out any) error {
				r.values = append(r.values, out)
				return nil
			})
		})
		return r
	}, func(r chunkResult) {
		parts = append(parts, r.values)
		total += len(r.values)
	})
	if err == nil {
		each := make([]any, 0, total)
		for _, part := range parts {
			each = append(each, part...)
		}
		parts = nil
		err = run(ctx, func(ctx context.Context, input any, emit func(any) error) error {
			return staged.Then(ctx, input, each, emit)
		}, s.Array(), lineCap, nil, &result.Lines, &result.Matches)
	}
	finish(ctx, &result, err)
	return result
}

// inChunks evaluates work for every chunk of the set on several workers and
// hands the results to merge in chunk order. A chunk that ends in an error
// (halt included) ends the evaluation there, exactly as it would
// sequentially: it and the chunks before it are merged, later ones are
// discarded, and its error is returned.
func (s *Set) inChunks(ctx context.Context, workers int, work func(context.Context, *Array) chunkResult, merge func(chunkResult)) error {
	chunks := (s.count + chunkRecords - 1) / chunkRecords
	workers = min(workers, chunks)
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	var (
		mu       sync.Mutex
		ready    = sync.NewCond(&mu)
		results  = make([]chunkResult, chunks)
		next     atomic.Int64 // next chunk to hand out
		stopping atomic.Bool  // a chunk ended the evaluation; start no more
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
				r := work(runCtx, s.Range(chunk*chunkRecords, min(s.count, (chunk+1)*chunkRecords)))
				r.done = true
				mu.Lock()
				results[chunk] = r
				mu.Unlock()
				ready.Broadcast()
			}
		}()
	}

	var failure error
	mu.Lock()
	for chunk := 0; chunk < chunks; chunk++ {
		for !results[chunk].done {
			ready.Wait()
		}
		r := results[chunk]
		results[chunk] = chunkResult{done: true} // release the outputs early
		merge(r)
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
	// The caller classifies the failure against the parent context: the
	// derived one is cancelled by this function itself.
	return failure
}
