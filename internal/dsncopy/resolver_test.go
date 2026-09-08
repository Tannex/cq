package dsncopy

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/Tannex/cq/internal/copybook"
	"github.com/Tannex/cq/internal/zosmf"
)

type fetchFunc func(context.Context, string) ([]byte, error)

func (f fetchFunc) FetchText(ctx context.Context, dsn string) ([]byte, error) {
	return f(ctx, dsn)
}

func TestResolveStopsFetchingAfterFirstMatch(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		fetcher := &mapFetcher{texts: map[string]string{
			"FIRST.CPY(ADDR)": "05 ADDRESS PIC X(10).",
			"LATER.CPY(ADDR)": "05 ADDRESS PIC X(99).",
		}}
		resolver := New([]string{"MISSING.CPY", "FIRST.CPY", "LATER.CPY"}, fetcher, nil)
		for range 2 {
			got, err := resolver.Resolve(" addr ")
			if err != nil || got != "05 ADDRESS PIC X(10)." {
				t.Fatalf("Resolve() = %q, %v", got, err)
			}
		}
		synctest.Wait()
		want := []string{"MISSING.CPY(ADDR)", "FIRST.CPY(ADDR)"}
		if !slices.Equal(fetcher.requests, want) {
			t.Fatalf("requests = %v, want %v with no speculative reads or cache refetches", fetcher.requests, want)
		}
	})
}

func TestRecursiveCopiesUseOneActiveFetch(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var active, peak atomic.Int32
		var mu sync.Mutex
		var requests []string
		texts := map[string]string{
			"LIB.CPY(ROOT)":   "01 REC. COPY FIRST. COPY SECOND.",
			"LIB.CPY(FIRST)":  "05 NAME PIC X(3). COPY NESTED.",
			"LIB.CPY(SECOND)": "05 FLAG PIC X.",
			"LIB.CPY(NESTED)": "05 EXTRA PIC X.",
		}
		fetcher := fetchFunc(func(ctx context.Context, dsn string) ([]byte, error) {
			n := active.Add(1)
			defer active.Add(-1)
			for old := peak.Load(); n > old; old = peak.Load() {
				if peak.CompareAndSwap(old, n) {
					break
				}
			}
			mu.Lock()
			requests = append(requests, dsn)
			mu.Unlock()
			// Fake time lets every concurrent prefetch start before any finishes.
			time.Sleep(time.Millisecond)
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			if text, ok := texts[dsn]; ok {
				return []byte(text), nil
			}
			return nil, &zosmf.HTTPError{StatusCode: 404, Resource: dsn}
		})
		resolver := New([]string{"MISSING.CPY", "LIB.CPY", "UNUSED.CPY"}, fetcher, nil)
		// Exercise primary fallback as well as concurrent sibling prefetches
		// and a nested COPY through the same resolver.
		src, err := resolver.FetchPrimary(context.Background(), "OLD.CPY(ROOT)")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := copybook.ParseWithCopies(src, copybook.FormatFree, resolver.Resolve); err != nil {
			t.Fatal(err)
		}
		synctest.Wait()
		if got := peak.Load(); got != 1 {
			t.Errorf("peak active fetches = %d, want 1 across recursive prefetches", got)
		}
		if got := active.Load(); got != 0 {
			t.Errorf("active fetches after parsing = %d, want 0", got)
		}
		slices.Sort(requests)
		want := []string{
			"LIB.CPY(FIRST)", "LIB.CPY(NESTED)", "LIB.CPY(ROOT)", "LIB.CPY(SECOND)",
			"MISSING.CPY(FIRST)", "MISSING.CPY(NESTED)", "MISSING.CPY(ROOT)", "MISSING.CPY(SECOND)",
			"OLD.CPY(ROOT)",
		}
		if !slices.Equal(requests, want) {
			t.Errorf("requests = %v, want %v", requests, want)
		}
	})
}

func TestCanceledFetchWaitsForCleanup(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		cleanup := make(chan struct{})
		var calls atomic.Int32
		fetcher := fetchFunc(func(ctx context.Context, _ string) ([]byte, error) {
			if calls.Add(1) == 1 {
				<-ctx.Done()
				<-cleanup // simulate deferred response-body cleanup
				return nil, ctx.Err()
			}
			return []byte("05 FIELD PIC X."), nil
		})
		resolver := New([]string{"LIB.CPY"}, fetcher, nil)
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		result := make(chan error, 1)
		go func() {
			_, err := resolver.ResolveContext(ctx, "FIRST")
			result <- err
		}()
		synctest.Wait()
		cancel()
		synctest.Wait()
		if len(result) != 0 {
			t.Error("ResolveContext returned before fetch cleanup finished")
		}
		primaryResult := make(chan error, 1)
		go func() {
			_, err := resolver.FetchPrimary(context.Background(), "HQ.CPY(ROOT)")
			primaryResult <- err
		}()
		synctest.Wait()
		if got := calls.Load(); got != 1 {
			t.Errorf("fetches before cleanup = %d, want 1", got)
		}
		close(cleanup)
		if err := <-result; !errors.Is(err, context.Canceled) {
			t.Errorf("ResolveContext() error = %v, want cancellation", err)
		}
		if err := <-primaryResult; err != nil {
			t.Errorf("FetchPrimary() error = %v", err)
		}
	})
}

func TestQueuedFetchCancellationDoesNotFetchOrCache(t *testing.T) {
	for _, primary := range []bool{false, true} {
		t.Run(fmt.Sprintf("primary=%t", primary), func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				release := make(chan struct{})
				var calls atomic.Int32
				fetcher := fetchFunc(func(context.Context, string) ([]byte, error) {
					if calls.Add(1) == 1 {
						<-release
					}
					return []byte("05 FIELD PIC X."), nil
				})
				resolver := New([]string{"LIB.CPY"}, fetcher, nil)
				first := make(chan error, 1)
				go func() {
					_, err := resolver.FetchPrimary(context.Background(), "HQ.CPY(ROOT)")
					first <- err
				}()
				synctest.Wait()
				fetch := func(ctx context.Context) (string, error) {
					if primary {
						return resolver.FetchPrimary(ctx, "LIB.CPY(NEXT)")
					}
					return resolver.ResolveContext(ctx, "NEXT")
				}
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				result := make(chan error, 1)
				go func() {
					_, err := fetch(ctx)
					result <- err
				}()
				synctest.Wait()
				cancel()
				if err := <-result; !errors.Is(err, context.Canceled) {
					t.Errorf("queued fetch error = %v, want cancellation", err)
				}
				if got := calls.Load(); got != 1 {
					t.Errorf("fetches with canceled waiter = %d, want 1", got)
				}
				close(release)
				if err := <-first; err != nil {
					t.Fatal(err)
				}
				if got, err := fetch(context.Background()); err != nil || got != "05 FIELD PIC X." {
					t.Fatalf("retry = %q, %v", got, err)
				}
				if got := calls.Load(); got != 2 {
					t.Errorf("fetches after retry = %d, want 2", got)
				}
			})
		})
	}
}

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
