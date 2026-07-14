// COPY member resolution through the configured DSN search path. All data
// set access goes through the Zowe sidecar (sidecar.go).
package main

import (
	"fmt"
	"strings"
	"sync"
)

// maxConcurrentZoweFetches bounds the data set reads a resolver has in
// flight at once; each is one HTTP request on the sidecar's z/OSMF session.
const maxConcurrentZoweFetches = 8

type dsnCopyResolver struct {
	searchPaths []string
	transport   zoweTransport
	slots       chan struct{}

	mu    sync.Mutex
	cache map[string]copyResult
}

type copyResult struct {
	text string
	err  error
}

func newDSNCopyResolver(searchPaths []string, transport zoweTransport) *dsnCopyResolver {
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
// configured precedence still decides which copy wins.
func (r *dsnCopyResolver) probeSearchPaths(member string) copyResult {
	results := make([]copyResult, len(r.searchPaths))
	var wg sync.WaitGroup
	for i, library := range r.searchPaths {
		wg.Add(1)
		go func() {
			defer wg.Done()
			r.slots <- struct{}{}
			defer func() { <-r.slots }()
			src, err := r.transport.fetchCopybook(fmt.Sprintf("%s(%s)", library, member))
			results[i] = copyResult{text: string(src), err: err}
		}()
	}
	wg.Wait()

	var failures []string
	for i, res := range results {
		if res.err == nil {
			debugLog.Printf("COPY %s: using %s(%s)", member, r.searchPaths[i], member)
			return copyResult{text: res.text}
		}
		failures = append(failures, res.err.Error())
	}
	debugLog.Printf("COPY %s: not found in any of %d libraries", member, len(r.searchPaths))
	return copyResult{err: fmt.Errorf("COPY %s was not resolved through dsnSearchPath:\n  %s", member, strings.Join(failures, "\n  "))}
}
