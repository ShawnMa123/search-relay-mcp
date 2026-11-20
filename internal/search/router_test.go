package search

import (
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type mockProvider struct {
	name  string
	calls atomic.Int32
	fn    func(context.Context, Request) ([]Result, error)
}

func (m *mockProvider) Name() string { return m.name }

func (m *mockProvider) Search(ctx context.Context, req Request) ([]Result, error) {
	m.calls.Add(1)
	return m.fn(ctx, req)
}

type callLog struct {
	mu    sync.Mutex
	names []string
}

func (l *callLog) add(name string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.names = append(l.names, name)
}

func (l *callLog) snapshot() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]string(nil), l.names...)
}

type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func newFakeClock(t time.Time) *fakeClock { return &fakeClock{t: t} }

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

func okResults(n int) []Result {
	out := make([]Result, 0, n)
	urls := []string{
		"https://example.com/a",
		"https://example.com/b",
		"https://example.com/c",
	}
	for i := range n {
		out = append(out, Result{Title: "hit", URL: urls[i]})
	}
	return out
}

func testRouter(providers []Provider, opts RouterOptions) *Router {
	return NewRouter(providers, opts)
}

func defaultOpts(clock *fakeClock) RouterOptions {
	return RouterOptions{
		AttemptTimeout: 4 * time.Second,
		TotalTimeout:   12 * time.Second,
		MaxConcurrent:  4,
		Now:            clock.Now,
	}
}

func TestRouterStopsAtFirstUsable(t *testing.T) {
	clock := newFakeClock(time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC))
	var log callLog
	first := &mockProvider{name: "keenable", fn: func(context.Context, Request) ([]Result, error) {
		log.add("keenable")
		return okResults(1), nil
	}}
	second := &mockProvider{name: "tinyfish", fn: func(context.Context, Request) ([]Result, error) {
		log.add("tinyfish")
		return okResults(2), nil
	}}
	r := testRouter([]Provider{first, second}, defaultOpts(clock))

	resp, err := r.Search(context.Background(), Request{Query: "go", Limit: 5})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Provider != "keenable" || len(resp.Results) != 1 {
		t.Fatalf("response = %+v", resp)
	}
	if got := log.snapshot(); len(got) != 1 || got[0] != "keenable" {
		t.Fatalf("calls = %v", got)
	}
	if second.calls.Load() != 0 {
		t.Fatal("second provider called after usable result")
	}
}

func TestRouterFallsBack(t *testing.T) {
	clock := newFakeClock(time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC))

	failures := []struct {
		name string
		err  error
	}{
		{name: "timeout", err: &ProviderError{Kind: KindTimeout}},
		{name: "network", err: &ProviderError{Kind: KindNetwork}},
		{name: "upstream status", err: &ProviderError{Kind: KindUpstreamError, Status: 500}},
		{name: "invalid entries", err: nil},
	}

	for _, fail := range failures {
		t.Run(fail.name, func(t *testing.T) {
			var log callLog
			mk := func(name string, fn func() ([]Result, error)) *mockProvider {
				return &mockProvider{name: name, fn: func(context.Context, Request) ([]Result, error) {
					log.add(name)
					return fn()
				}}
			}
			bad := func() ([]Result, error) {
				if fail.err != nil {
					return nil, fail.err
				}
				return []Result{{Title: "bad", URL: "javascript:alert(1)"}}, nil
			}
			a := mk("keenable", bad)
			b := mk("tinyfish", bad)
			c := mk("anysearch", func() ([]Result, error) { return okResults(1), nil })

			r := testRouter([]Provider{a, b, c}, defaultOpts(clock))
			resp, err := r.Search(context.Background(), Request{Query: "go", Limit: 5})
			if err != nil {
				t.Fatal(err)
			}
			if resp.Provider != "anysearch" || len(resp.Results) != 1 {
				t.Fatalf("response = %+v", resp)
			}
			got := log.snapshot()
			want := []string{"keenable", "tinyfish", "anysearch"}
			if len(got) != len(want) {
				t.Fatalf("calls = %v", got)
			}
			for i := range want {
				if got[i] != want[i] {
					t.Fatalf("calls = %v", got)
				}
			}
			for _, p := range []*mockProvider{a, b, c} {
				if p.calls.Load() != 1 {
					t.Fatalf("%s called %d times", p.name, p.calls.Load())
				}
			}
		})
	}
}

func TestRouterDistinguishesEmptyFromFailure(t *testing.T) {
	clock := newFakeClock(time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC))

	t.Run("empty success is a normal response", func(t *testing.T) {
		empty := &mockProvider{name: "keenable", fn: func(context.Context, Request) ([]Result, error) {
			return []Result{}, nil
		}}
		failing := &mockProvider{name: "tinyfish", fn: func(context.Context, Request) ([]Result, error) {
			return nil, &ProviderError{Kind: KindUpstreamError, Status: 500}
		}}
		r := testRouter([]Provider{empty, failing}, defaultOpts(clock))
		resp, err := r.Search(context.Background(), Request{Query: "go", Limit: 5})
		if err != nil {
			t.Fatal(err)
		}
		if resp.Provider != "keenable" || resp.Results == nil || len(resp.Results) != 0 {
			t.Fatalf("response = %+v", resp)
		}
	})

	t.Run("empty result does not stop the chain", func(t *testing.T) {
		empty := &mockProvider{name: "keenable", fn: func(context.Context, Request) ([]Result, error) {
			return []Result{}, nil
		}}
		other := &mockProvider{name: "tinyfish", fn: func(context.Context, Request) ([]Result, error) {
			return okResults(1), nil
		}}
		r := testRouter([]Provider{empty, other}, defaultOpts(clock))
		resp, err := r.Search(context.Background(), Request{Query: "go", Limit: 5})
		if err != nil {
			t.Fatal(err)
		}
		if resp.Provider != "tinyfish" || len(resp.Results) != 1 {
			t.Fatalf("response = %+v", resp)
		}
	})

	t.Run("all invalid entries still counts as empty success", func(t *testing.T) {
		p := &mockProvider{name: "keenable", fn: func(context.Context, Request) ([]Result, error) {
			return []Result{{Title: "bad", URL: "not-a-url"}}, nil
		}}
		r := testRouter([]Provider{p}, defaultOpts(clock))
		resp, err := r.Search(context.Background(), Request{Query: "go", Limit: 5})
		if err != nil {
			t.Fatal(err)
		}
		if resp.Provider != "keenable" || len(resp.Results) != 0 {
			t.Fatalf("response = %+v", resp)
		}
	})

	t.Run("all failures return unavailable", func(t *testing.T) {
		p := &mockProvider{name: "keenable", fn: func(context.Context, Request) ([]Result, error) {
			return nil, &ProviderError{Kind: KindUpstreamError, Status: 500}
		}}
		r := testRouter([]Provider{p}, defaultOpts(clock))
		_, err := r.Search(context.Background(), Request{Query: "go", Limit: 5})
		var unavailable *UnavailableError
		if !errors.As(err, &unavailable) {
			t.Fatalf("error = %v", err)
		}
		if len(unavailable.Attempts) != 1 || unavailable.Attempts[0].Provider != "keenable" ||
			unavailable.Attempts[0].Kind != KindUpstreamError {
			t.Fatalf("attempts = %+v", unavailable.Attempts)
		}
	})

	t.Run("invalid input never reaches a provider", func(t *testing.T) {
		p := &mockProvider{name: "keenable", fn: func(context.Context, Request) ([]Result, error) {
			return okResults(1), nil
		}}
		r := testRouter([]Provider{p}, defaultOpts(clock))
		if _, err := r.Search(context.Background(), Request{Query: "", Limit: 5}); err == nil {
			t.Fatal("expected validation error")
		}
		if _, err := r.Search(context.Background(), Request{Query: "go", Limit: 11}); err == nil {
			t.Fatal("expected validation error")
		}
		if p.calls.Load() != 0 {
			t.Fatalf("provider called %d times", p.calls.Load())
		}
	})
}

func TestRouterHonorsCancellation(t *testing.T) {
	clock := newFakeClock(time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC))

	t.Run("total budget stops the chain", func(t *testing.T) {
		blocking := &mockProvider{name: "keenable", fn: func(ctx context.Context, _ Request) ([]Result, error) {
			<-ctx.Done()
			return nil, ctx.Err()
		}}
		second := &mockProvider{name: "tinyfish", fn: func(context.Context, Request) ([]Result, error) {
			return okResults(1), nil
		}}
		opts := defaultOpts(clock)
		opts.TotalTimeout = 50 * time.Millisecond
		r := testRouter([]Provider{blocking, second}, opts)

		start := time.Now()
		_, err := r.Search(context.Background(), Request{Query: "go", Limit: 5})
		if err == nil {
			t.Fatal("expected error")
		}
		var unavailable *UnavailableError
		if !errors.As(err, &unavailable) {
			t.Fatalf("error = %v", err)
		}
		if second.calls.Load() != 0 {
			t.Fatalf("second provider called %d times after budget expiry", second.calls.Load())
		}
		if elapsed := time.Since(start); elapsed > time.Second {
			t.Fatalf("budget not enforced, took %v", elapsed)
		}
	})

	t.Run("parent cancellation is not an empty success", func(t *testing.T) {
		blocking := &mockProvider{name: "keenable", fn: func(ctx context.Context, _ Request) ([]Result, error) {
			<-ctx.Done()
			return nil, ctx.Err()
		}}
		second := &mockProvider{name: "tinyfish", fn: func(context.Context, Request) ([]Result, error) {
			return okResults(1), nil
		}}
		r := testRouter([]Provider{blocking, second}, defaultOpts(clock))

		ctx, cancel := context.WithCancel(context.Background())
		go func() {
			time.Sleep(20 * time.Millisecond)
			cancel()
		}()
		resp, err := r.Search(ctx, Request{Query: "go", Limit: 5})
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("error = %v, response = %+v", err, resp)
		}
		if second.calls.Load() != 0 {
			t.Fatalf("second provider called %d times after cancellation", second.calls.Load())
		}
	})
}

func TestRouterExpiredBudgetIsNotEmptySuccess(t *testing.T) {
	clock := newFakeClock(time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC))

	t.Run("budget expiry after an empty result is an error", func(t *testing.T) {
		empty := &mockProvider{name: "keenable", fn: func(context.Context, Request) ([]Result, error) {
			return []Result{}, nil
		}}
		blocking := &mockProvider{name: "tinyfish", fn: func(ctx context.Context, _ Request) ([]Result, error) {
			<-ctx.Done()
			return nil, ctx.Err()
		}}
		third := &mockProvider{name: "anysearch", fn: func(context.Context, Request) ([]Result, error) {
			return okResults(1), nil
		}}
		opts := defaultOpts(clock)
		opts.TotalTimeout = 50 * time.Millisecond
		r := testRouter([]Provider{empty, blocking, third}, opts)

		resp, err := r.Search(context.Background(), Request{Query: "go", Limit: 5})
		var unavailable *UnavailableError
		if !errors.As(err, &unavailable) {
			t.Fatalf("error = %v, response = %+v", err, resp)
		}
		if third.calls.Load() != 0 {
			t.Fatalf("third provider called %d times", third.calls.Load())
		}
	})

	t.Run("caller cancellation after an empty result is an error", func(t *testing.T) {
		empty := &mockProvider{name: "keenable", fn: func(context.Context, Request) ([]Result, error) {
			return []Result{}, nil
		}}
		started := make(chan struct{})
		blocking := &mockProvider{name: "tinyfish", fn: func(ctx context.Context, _ Request) ([]Result, error) {
			close(started)
			<-ctx.Done()
			return nil, ctx.Err()
		}}
		third := &mockProvider{name: "anysearch", fn: func(context.Context, Request) ([]Result, error) {
			return okResults(1), nil
		}}
		r := testRouter([]Provider{empty, blocking, third}, defaultOpts(clock))

		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan error, 1)
		go func() {
			_, err := r.Search(ctx, Request{Query: "go", Limit: 5})
			done <- err
		}()
		<-started
		cancel()
		select {
		case err := <-done:
			if !errors.Is(err, context.Canceled) {
				t.Fatalf("error = %v", err)
			}
		case <-time.After(2 * time.Second):
			t.Fatal("search did not return after cancellation")
		}
		if third.calls.Load() != 0 {
			t.Fatalf("third provider called %d times", third.calls.Load())
		}
	})
}

func TestRouterCooldown(t *testing.T) {
	clock := newFakeClock(time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC))

	t.Run("rate limit uses retry-after then expires", func(t *testing.T) {
		var firstCalls atomic.Int32
		limited := &mockProvider{name: "keenable", fn: func(context.Context, Request) ([]Result, error) {
			if firstCalls.Add(1) == 1 {
				return nil, &ProviderError{
					Kind:    KindRateLimited,
					Status:  429,
					RetryAt: clock.Now().Add(120 * time.Second),
				}
			}
			return okResults(1), nil
		}}
		backup := &mockProvider{name: "tinyfish", fn: func(context.Context, Request) ([]Result, error) {
			return okResults(1), nil
		}}
		r := testRouter([]Provider{limited, backup}, defaultOpts(clock))

		resp, err := r.Search(context.Background(), Request{Query: "go", Limit: 5})
		if err != nil || resp.Provider != "tinyfish" {
			t.Fatalf("first search: %+v %v", resp, err)
		}

		clock.Advance(60 * time.Second)
		resp, err = r.Search(context.Background(), Request{Query: "go", Limit: 5})
		if err != nil || resp.Provider != "tinyfish" {
			t.Fatalf("during cooldown: %+v %v", resp, err)
		}
		if firstCalls.Load() != 1 {
			t.Fatalf("limited provider called %d times during cooldown", firstCalls.Load())
		}

		clock.Advance(61 * time.Second)
		resp, err = r.Search(context.Background(), Request{Query: "go", Limit: 5})
		if err != nil || resp.Provider != "keenable" {
			t.Fatalf("after cooldown: %+v %v", resp, err)
		}
		if firstCalls.Load() != 2 {
			t.Fatalf("limited provider calls = %d", firstCalls.Load())
		}
	})

	t.Run("all cooling is unavailable not empty success", func(t *testing.T) {
		p := &mockProvider{name: "keenable", fn: func(context.Context, Request) ([]Result, error) {
			return nil, &ProviderError{Kind: KindRateLimited, Status: 429}
		}}
		r := testRouter([]Provider{p}, defaultOpts(clock))
		if _, err := r.Search(context.Background(), Request{Query: "go", Limit: 5}); err == nil {
			t.Fatal("expected first failure")
		}
		_, err := r.Search(context.Background(), Request{Query: "go", Limit: 5})
		var unavailable *UnavailableError
		if !errors.As(err, &unavailable) {
			t.Fatalf("error = %v", err)
		}
		if len(unavailable.Attempts) != 1 || unavailable.Attempts[0].Kind != KindCooldown {
			t.Fatalf("attempts = %+v", unavailable.Attempts)
		}
		if p.calls.Load() != 1 {
			t.Fatalf("provider called %d times", p.calls.Load())
		}
	})

	t.Run("empty results do not open a cooldown", func(t *testing.T) {
		p := &mockProvider{name: "keenable", fn: func(context.Context, Request) ([]Result, error) {
			return []Result{}, nil
		}}
		r := testRouter([]Provider{p}, defaultOpts(clock))
		for range 3 {
			if _, err := r.Search(context.Background(), Request{Query: "go", Limit: 5}); err != nil {
				t.Fatal(err)
			}
		}
		if p.calls.Load() != 3 {
			t.Fatalf("provider calls = %d", p.calls.Load())
		}
	})
}

func TestRouterCooldownBounds(t *testing.T) {
	clock := newFakeClock(time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC))
	r := testRouter(nil, defaultOpts(clock))

	cases := []struct {
		name string
		pe   *ProviderError
		want time.Duration
	}{
		{
			name: "rate limit default",
			pe:   &ProviderError{Kind: KindRateLimited},
			want: 60 * time.Second,
		},
		{
			name: "retry-after in the past clamps to 1s",
			pe:   &ProviderError{Kind: KindRateLimited, RetryAt: clock.Now().Add(-10 * time.Second)},
			want: time.Second,
		},
		{
			name: "retry-after beyond 1h clamps to 1h",
			pe:   &ProviderError{Kind: KindRateLimited, RetryAt: clock.Now().Add(2 * time.Hour)},
			want: time.Hour,
		},
		{
			name: "quota default is 1h",
			pe:   &ProviderError{Kind: KindQuotaExhausted},
			want: time.Hour,
		},
		{
			name: "quota honors retry-after",
			pe:   &ProviderError{Kind: KindQuotaExhausted, RetryAt: clock.Now().Add(30 * time.Minute)},
			want: 30 * time.Minute,
		},
		{
			name: "reset-at beyond 1h is kept",
			pe:   &ProviderError{Kind: KindQuotaExhausted, ResetAt: clock.Now().Add(12 * time.Hour)},
			want: 12 * time.Hour,
		},
		{
			name: "past reset-at clamps to 1s",
			pe:   &ProviderError{Kind: KindQuotaExhausted, ResetAt: clock.Now().Add(-time.Hour)},
			want: time.Second,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			until, ok := r.cooldownUntil(tc.pe)
			if !ok {
				t.Fatal("no cooldown computed")
			}
			if got := until.Sub(clock.Now()); got != tc.want {
				t.Fatalf("cooldown = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestRouterCooldownNeverShortens(t *testing.T) {
	clock := newFakeClock(time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC))
	r := testRouter(nil, defaultOpts(clock))

	r.applyCooldown("keenable", &ProviderError{Kind: KindRateLimited, RetryAt: clock.Now().Add(time.Hour)})
	r.applyCooldown("keenable", &ProviderError{Kind: KindRateLimited, RetryAt: clock.Now().Add(10 * time.Minute)})

	r.mu.Lock()
	until := r.cooldown["keenable"]
	r.mu.Unlock()
	if want := clock.Now().Add(time.Hour); !until.Equal(want) {
		t.Fatalf("cooldown shortened to %v, want %v", until, want)
	}
}

func TestRouterBusy(t *testing.T) {
	clock := newFakeClock(time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC))
	entered := make(chan struct{}, 4)
	release := make(chan struct{})
	p := &mockProvider{name: "keenable", fn: func(context.Context, Request) ([]Result, error) {
		entered <- struct{}{}
		<-release
		return okResults(1), nil
	}}
	opts := defaultOpts(clock)
	opts.MaxConcurrent = 4
	r := testRouter([]Provider{p}, opts)

	var wg sync.WaitGroup
	for i := range 4 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := r.Search(context.Background(), Request{Query: "go", Limit: 5}); err != nil {
				t.Errorf("search %d: %v", i, err)
			}
		}()
	}
	for range 4 {
		select {
		case <-entered:
		case <-time.After(2 * time.Second):
			t.Fatal("providers did not start")
		}
	}

	if _, err := r.Search(context.Background(), Request{Query: "go", Limit: 5}); !errors.Is(err, ErrBusy) {
		t.Fatalf("fifth call error = %v", err)
	}
	if p.calls.Load() != 4 {
		t.Fatalf("provider calls = %d", p.calls.Load())
	}

	close(release)
	wg.Wait()

	// Slots must be released after completion.
	if _, err := r.Search(context.Background(), Request{Query: "go", Limit: 5}); err != nil {
		t.Fatalf("post-release search: %v", err)
	}
}

func TestRouterErrorsAreSafe(t *testing.T) {
	clock := newFakeClock(time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC))
	const query = "sentinel-query-DO-NOT-ECHO"
	const upstreamBody = "secret-upstream-body"
	p := &mockProvider{name: "keenable", fn: func(context.Context, Request) ([]Result, error) {
		return nil, errors.New(upstreamBody)
	}}
	r := testRouter([]Provider{p}, defaultOpts(clock))
	_, err := r.Search(context.Background(), Request{Query: query, Limit: 5})
	if err == nil {
		t.Fatal("expected error")
	}
	msg := err.Error()
	if strings.Contains(msg, query) || strings.Contains(msg, upstreamBody) {
		t.Fatalf("error leaked data: %s", msg)
	}
}
