package dsncopy

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
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
