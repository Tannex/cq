// Package dsncopy resolves COBOL COPY members through configured z/OS data set libraries.
package dsncopy

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"

	"github.com/Tannex/cq/internal/zosmf"
)

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
	fetchSlot   chan struct{}

	mu    sync.Mutex
	cache map[string]copyResult
}

type copyResult struct {
	text string
	err  error
}

// New creates a resolver. Remote reads are serialized, including concurrent
// COPY prefetches, to limit pressure on z/OSMF's TSO address spaces.
func New(searchPaths []string, fetcher Fetcher, diagnostic Diagnostic) *Resolver {
	return &Resolver{
		searchPaths: append([]string(nil), searchPaths...),
		fetcher:     fetcher,
		diagnostic:  diagnostic,
		fetchSlot:   make(chan struct{}, 1),
		cache:       make(map[string]copyResult),
	}
}

// FetchPrimary retrieves the copybook at dsn. When z/OSMF reports it missing
// (404) and the name carries a member — DSN(MEMBER) or a bare member name —
// that member is re-resolved through the same search chain nested COPY
// members use, so a copybook moves libraries without breaking saved mappings.
func (r *Resolver) FetchPrimary(ctx context.Context, dsn string) (string, error) {
	text, err := r.fetchText(ctx, dsn)
	if err == nil {
		return string(text), nil
	}
	if !zosmf.IsNotFound(err) {
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
	if _, member, found := zosmf.SplitMemberTarget(dsn); found {
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

// ResolveContext is safe for concurrent use. Caller cancellation interrupts
// queued and active reads and is not cached, so a later resolution can retry.
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

// probeSearchPaths reads libraries in precedence order and stops at the first
// match. Let each FetchText finish (including response-body cleanup) before
// starting another read; canceling speculative HTTP requests does not wait for
// the corresponding server-side TSO work to finish.
func (r *Resolver) probeSearchPaths(ctx context.Context, member string) copyResult {
	failures := make([]string, 0, len(r.searchPaths))
	for _, library := range r.searchPaths {
		dsn := fmt.Sprintf("%s(%s)", library, member)
		src, err := r.fetchText(ctx, dsn)
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return copyResult{err: err}
		}
		if err == nil {
			r.logf("COPY %s: using %s", member, dsn)
			return copyResult{text: string(src)}
		}
		failures = append(failures, err.Error())
	}

	r.logf("COPY %s: not found in any of %d libraries", member, len(r.searchPaths))
	return copyResult{err: fmt.Errorf("COPY %s was not resolved through dsnSearchPath:\n  %s", member, strings.Join(failures, "\n  "))}
}

// fetchText shares one cancellable slot between primary and nested copybooks.
// Keep the slot until FetchText returns, even when the caller is canceled, so
// subsequent reads cannot overtake the fetcher's cleanup.
func (r *Resolver) fetchText(ctx context.Context, dsn string) ([]byte, error) {
	if ctx == nil {
		return nil, &zosmf.RequestError{Field: "context", Message: "must not be nil"}
	}
	select {
	case r.fetchSlot <- struct{}{}:
		defer func() { <-r.fetchSlot }()
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	// A slot and cancellation can become ready together.
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	src, err := r.fetcher.FetchText(ctx, dsn)
	if contextErr := ctx.Err(); contextErr != nil {
		return nil, contextErr
	}
	return src, err
}

func (r *Resolver) logf(format string, args ...any) {
	if r.diagnostic != nil {
		r.diagnostic(format, args...)
	}
}
