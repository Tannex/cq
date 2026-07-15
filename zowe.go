// COPY member resolution through the configured DSN search path. Data set
// access goes through the transport boundary in zowe_transport.go.
package main

import (
	"context"
	"fmt"
	"strings"
	"sync"
)

// maxConcurrentZoweFetches bounds the z/OSMF data set reads a resolver has in
// flight at once.
const maxConcurrentZoweFetches = 8

type copybookFetcher interface {
	fetchCopybook(context.Context, string) ([]byte, error)
}

type dsnCopyResolver struct {
	searchPaths []string
	transport   copybookFetcher
	slots       chan struct{}

	mu    sync.Mutex
	cache map[string]copyResult
}

type copyResult struct {
	text string
	err  error
}

func newDSNCopyResolver(searchPaths []string, transport copybookFetcher) *dsnCopyResolver {
	return &dsnCopyResolver{
		searchPaths: searchPaths,
		transport:   transport,
		slots:       make(chan struct{}, maxConcurrentZoweFetches),
		cache:       make(map[string]copyResult),
	}
}

// Resolve is safe for concurrent use so COPY expansion can prefetch the
// members of a level in parallel.
func (r *dsnCopyResolver) Resolve(member string) (string, error) {
	member = strings.ToUpper(strings.TrimSpace(member))
	r.mu.Lock()
	res, ok := r.cache[member]
	r.mu.Unlock()
	if ok {
		debugLog.Printf("COPY %s: cached", member)
		return res.text, res.err
	}
	if len(r.searchPaths) == 0 {
		return "", fmt.Errorf("COPY %s requires dsnSearchPath in the user config file", member)
	}
	res = r.probeSearchPaths(member)
	r.mu.Lock()
	r.cache[member] = res
	r.mu.Unlock()
	return res.text, res.err
}

// probeSearchPaths queries every library concurrently and keeps the result
// from the earliest library in search order that has the member, so the
// configured precedence still decides which copy wins. It returns as soon as
// the winner is decided — a success whose earlier candidates have all failed
// — and cancels the probes still in flight, so a slow later library never
// delays or blocks an earlier match.
func (r *dsnCopyResolver) probeSearchPaths(member string) copyResult {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	type probe struct {
		i   int
		res copyResult
	}
	resCh := make(chan probe, len(r.searchPaths))
	for i, library := range r.searchPaths {
		go func() {
			r.slots <- struct{}{}
			defer func() { <-r.slots }()
			src, err := r.transport.fetchCopybook(ctx, fmt.Sprintf("%s(%s)", library, member))
			resCh <- probe{i: i, res: copyResult{text: string(src), err: err}}
		}()
	}

	results := make([]*copyResult, len(r.searchPaths))
	next := 0 // earliest library whose outcome is still unknown
	for range r.searchPaths {
		p := <-resCh
		results[p.i] = &p.res
		for next < len(results) && results[next] != nil {
			if results[next].err == nil {
				debugLog.Printf("COPY %s: using %s(%s)", member, r.searchPaths[next], member)
				return copyResult{text: results[next].text}
			}
			next++
		}
	}

	var failures []string
	for _, res := range results {
		failures = append(failures, res.err.Error())
	}
	debugLog.Printf("COPY %s: not found in any of %d libraries", member, len(r.searchPaths))
	return copyResult{err: fmt.Errorf("COPY %s was not resolved through dsnSearchPath:\n  %s", member, strings.Join(failures, "\n  "))}
}
