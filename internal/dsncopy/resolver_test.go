package dsncopy

import (
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/Tannex/cq/internal/zosmf"
)

type cancelThenSucceedFetcher struct {
	calls   atomic.Int32
	started chan struct{}
}

func (f *cancelThenSucceedFetcher) FetchText(ctx context.Context, _ string) ([]byte, error) {
	if f.calls.Add(1) == 1 {
		close(f.started)
		<-ctx.Done()
		return nil, ctx.Err()
	}
	return []byte("05 ADDRESS PIC X(10).\n"), nil
}

func TestResolveContextCancelsProbesWithoutCachingCancellation(t *testing.T) {
	fetcher := &cancelThenSucceedFetcher{started: make(chan struct{})}
	resolver := New([]string{"HQL.CPY.SRC"}, fetcher, nil)
	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() {
		_, err := resolver.ResolveContext(ctx, "ADDRESS")
		result <- err
	}()
	<-fetcher.started
	cancel()
	if err := <-result; !errors.Is(err, context.Canceled) {
		t.Fatalf("ResolveContext() error = %v, want context cancellation", err)
	}

	got, err := resolver.ResolveContext(context.Background(), "ADDRESS")
	if err != nil {
		t.Fatalf("ResolveContext() retry error = %v", err)
	}
	if got != "05 ADDRESS PIC X(10).\n" {
		t.Fatalf("ResolveContext() retry = %q", got)
	}
}

// mapFetcher answers from fixed tables; the resolver probes libraries
// concurrently, so the request log is mutex-guarded.
type mapFetcher struct {
	texts    map[string]string
	statuses map[string]int

	mu       sync.Mutex
	requests []string
}

func (f *mapFetcher) FetchText(_ context.Context, dsn string) ([]byte, error) {
	f.mu.Lock()
	f.requests = append(f.requests, dsn)
	f.mu.Unlock()
	if text, ok := f.texts[dsn]; ok {
		return []byte(text), nil
	}
	status := f.statuses[dsn]
	if status == 0 {
		status = 404
	}
	return nil, &zosmf.HTTPError{StatusCode: status, Resource: dsn}
}

func TestFetchPrimaryFallsBackThroughSearchChainOn404(t *testing.T) {
	for _, dsn := range []string{"OLD.COPYLIB(CUST)", "CUST"} {
		fetcher := &mapFetcher{texts: map[string]string{"NEW.COPYLIB(CUST)": "05 A PIC X."}}
		resolver := New([]string{"MISSING.LIB", "NEW.COPYLIB"}, fetcher, nil)
		text, err := resolver.FetchPrimary(context.Background(), dsn)
		if err != nil {
			t.Fatalf("FetchPrimary(%q) error = %v", dsn, err)
		}
		if text != "05 A PIC X." {
			t.Fatalf("FetchPrimary(%q) = %q", dsn, text)
		}
		if fetcher.requests[0] != dsn {
			t.Fatalf("first request = %q, want the original DSN", fetcher.requests[0])
		}
	}
}

func TestFetchPrimaryKeepsNonNotFoundErrors(t *testing.T) {
	fetcher := &mapFetcher{
		statuses: map[string]int{"OLD.COPYLIB(CUST)": 500},
		texts:    map[string]string{"NEW.COPYLIB(CUST)": "05 A PIC X."},
	}
	resolver := New([]string{"NEW.COPYLIB"}, fetcher, nil)
	if _, err := resolver.FetchPrimary(context.Background(), "OLD.COPYLIB(CUST)"); err == nil {
		t.Fatal("server errors must not trigger the search chain")
	}
	if len(fetcher.requests) != 1 {
		t.Fatalf("requests = %v, want only the original fetch", fetcher.requests)
	}
}

func TestFetchPrimarySkipsFallbackWithoutAMemberName(t *testing.T) {
	fetcher := &mapFetcher{}
	resolver := New([]string{"NEW.COPYLIB"}, fetcher, nil)
	if _, err := resolver.FetchPrimary(context.Background(), "OLD.COPYLIB.CUST"); err == nil {
		t.Fatal("qualified name without a member must keep the 404")
	}
	if len(fetcher.requests) != 1 {
		t.Fatalf("requests = %v, want only the original fetch", fetcher.requests)
	}
}

func TestFetchPrimaryReportsBothFailures(t *testing.T) {
	fetcher := &mapFetcher{}
	resolver := New([]string{"NEW.COPYLIB"}, fetcher, nil)
	_, err := resolver.FetchPrimary(context.Background(), "OLD.COPYLIB(CUST)")
	if err == nil || !strings.Contains(err.Error(), "dsnSearchPath") {
		t.Fatalf("error should mention the failed chain, got %v", err)
	}
	var httpErr *zosmf.HTTPError
	if !errors.As(err, &httpErr) || httpErr.StatusCode != 404 {
		t.Fatalf("original 404 not preserved in %v", err)
	}
}
