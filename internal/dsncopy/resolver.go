// Package dsncopy resolves COBOL COPY members through configured z/OS data set libraries.
package dsncopy

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"

	"github.com/Tannex/cq/internal/zosmf"
)

// maxConcurrentFetches bounds the remote data set reads a resolver has in flight.
const maxConcurrentFetches = 8

// Fetcher retrieves a text data set or member.
type Fetcher interface {
	FetchText(context.Context, string) ([]byte, error)
}

// Diagnostic receives non-sensitive resolver diagnostics. A nil function
// disables diagnostics.
type Diagnostic func(format string, args ...any)

// Resolver expands COPY members through a configured, precedence-ordered DSN search path.
type Resolver struct {
	searchPaths []string
	fetcher     Fetcher
	diagnostic  Diagnostic
	slots       chan struct{}

	mu    sync.Mutex
	cache map[string]copyResult
}

type copyResult struct {
	text string
	err  error
}

// New creates a resolver. At most eight library fetches run concurrently.
func New(searchPaths []string, fetcher Fetcher, diagnostic Diagnostic) *Resolver {
	return &Resolver{
		searchPaths: append([]string(nil), searchPaths...),
		fetcher:     fetcher,
		diagnostic:  diagnostic,
		slots:       make(chan struct{}, maxConcurrentFetches),
		cache:       make(map[string]copyResult),
	}
}

// FetchPrimary retrieves the copybook at dsn. When z/OSMF reports it missing
// (404) and the name carries a member — DSN(MEMBER) or a bare member name —
// that member is re-resolved through the same search chain nested COPY
// members use, so a copybook moves libraries without breaking saved mappings.
func (r *Resolver) FetchPrimary(ctx context.Context, dsn string) (string, error) {
	text, err := r.fetcher.FetchText(ctx, dsn)
	if err == nil {
		return string(text), nil
	}
	var httpErr *zosmf.HTTPError
	if !errors.As(err, &httpErr) || httpErr.StatusCode != http.StatusNotFound {
		return "", err
	}
	member, ok := fallbackMember(dsn)
	if !ok || len(r.searchPaths) == 0 {
		return "", err
	}
	r.logf("copybook %s: not found; trying dsnSearchPath for member %s", dsn, member)
	text2, chainErr := r.ResolveContext(ctx, member)
	if chainErr != nil {
		return "", fmt.Errorf("%w; %v", err, chainErr)
	}
	return text2, nil
}

// fallbackMember extracts the member name a not-found copybook DSN can be
// re-searched by: the MEMBER of a DSN(MEMBER) reference, or a bare
// member-sized name with no qualifiers.
func fallbackMember(dsn string) (string, bool) {
	dsn = strings.ToUpper(strings.TrimSpace(dsn))
	if open := strings.IndexByte(dsn, '('); open >= 0 && strings.HasSuffix(dsn, ")") {
		member := dsn[open+1 : len(dsn)-1]
		return member, validMemberName(member)
	}
	if !strings.Contains(dsn, ".") && validMemberName(dsn) {
		return dsn, true
	}
	return "", false
}

func validMemberName(member string) bool {
	return member != "" && len(member) <= 8 && !strings.ContainsAny(member, ".()*% ")
}

// Resolve implements the context-free callback used by copybook.ParseWithCopies.
func (r *Resolver) Resolve(member string) (string, error) {
	return r.ResolveContext(context.Background(), member)
}

// ResolveContext is safe for concurrent use and cancels outstanding library
// probes when the caller is canceled. Caller cancellation is not cached, so a
// later resolution can retry normally.
func (r *Resolver) ResolveContext(ctx context.Context, member string) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	member = strings.ToUpper(strings.TrimSpace(member))
	r.mu.Lock()
	res, ok := r.cache[member]
	r.mu.Unlock()
	if ok {
		r.logf("COPY %s: cached", member)
		return res.text, res.err
	}
	if len(r.searchPaths) == 0 {
		return "", fmt.Errorf("COPY %s requires dsnSearchPath in the user config file", member)
	}
	res = r.probeSearchPaths(ctx, member)
	if !errors.Is(res.err, context.Canceled) && !errors.Is(res.err, context.DeadlineExceeded) {
		r.mu.Lock()
		r.cache[member] = res
		r.mu.Unlock()
	}
	return res.text, res.err
}

// probeSearchPaths queries every library concurrently and keeps the result
// from the earliest library in search order that has the member. It returns as
// soon as that winner is decided and cancels probes still in flight.
func (r *Resolver) probeSearchPaths(parent context.Context, member string) copyResult {
	ctx, cancel := context.WithCancel(parent)
	defer cancel()
	type probe struct {
		i   int
		res copyResult
	}
	resCh := make(chan probe, len(r.searchPaths))
	for i, library := range r.searchPaths {
		go func() {
			select {
			case r.slots <- struct{}{}:
				defer func() { <-r.slots }()
			case <-ctx.Done():
				resCh <- probe{i: i, res: copyResult{err: ctx.Err()}}
				return
			}
			src, err := r.fetcher.FetchText(ctx, fmt.Sprintf("%s(%s)", library, member))
			resCh <- probe{i: i, res: copyResult{text: string(src), err: err}}
		}()
	}

	results := make([]*copyResult, len(r.searchPaths))
	next := 0 // earliest library whose outcome is still unknown
	for range r.searchPaths {
		select {
		case <-parent.Done():
			return copyResult{err: parent.Err()}
		case p := <-resCh:
			results[p.i] = &p.res
			for next < len(results) && results[next] != nil {
				if results[next].err == nil {
					r.logf("COPY %s: using %s(%s)", member, r.searchPaths[next], member)
					return copyResult{text: results[next].text}
				}
				next++
			}
		}
	}

	failures := make([]string, 0, len(results))
	for _, res := range results {
		failures = append(failures, res.err.Error())
	}
	r.logf("COPY %s: not found in any of %d libraries", member, len(r.searchPaths))
	return copyResult{err: fmt.Errorf("COPY %s was not resolved through dsnSearchPath:\n  %s", member, strings.Join(failures, "\n  "))}
}

func (r *Resolver) logf(format string, args ...any) {
	if r.diagnostic != nil {
		r.diagnostic(format, args...)
	}
}
