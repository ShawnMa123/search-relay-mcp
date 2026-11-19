package search

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"time"
)

const (
	defaultAttemptTimeout = 4 * time.Second
	defaultTotalTimeout   = 12 * time.Second
	defaultMaxConcurrent  = 4

	rateLimitCooldown = 60 * time.Second
	quotaCooldown     = time.Hour
	minCooldown       = time.Second
	maxCooldown       = time.Hour
)

// RouterOptions bounds one tool call. Zero values select the defaults.
type RouterOptions struct {
	AttemptTimeout time.Duration
	TotalTimeout   time.Duration
	MaxConcurrent  int
	Now            func() time.Time
	Logger         *slog.Logger
}

// AttemptFailure is one provider's safe classification in an aggregated
// error. It never contains upstream payloads.
type AttemptFailure struct {
	Provider string
	Kind     Kind
	Status   int
}

// UnavailableError means no provider produced a usable reply: every attempt
// failed or every provider was cooling down.
type UnavailableError struct {
	Attempts []AttemptFailure
}

func (e *UnavailableError) Error() string { return "upstream_unavailable" }

// ErrBusy is returned immediately when every concurrency slot is taken.
var ErrBusy = errors.New("busy")

// Router tries providers in the configured order and returns the first
// usable result set.
type Router struct {
	providers []Provider
	opts      RouterOptions
	now       func() time.Time
	logger    *slog.Logger

	slots chan struct{}

	mu       sync.Mutex
	cooldown map[string]time.Time
}

// NewRouter builds a router. Providers are tried in slice order.
func NewRouter(providers []Provider, opts RouterOptions) *Router {
	if opts.AttemptTimeout <= 0 {
		opts.AttemptTimeout = defaultAttemptTimeout
	}
	if opts.TotalTimeout <= 0 {
		opts.TotalTimeout = defaultTotalTimeout
	}
	if opts.MaxConcurrent <= 0 {
		opts.MaxConcurrent = defaultMaxConcurrent
	}
	now := opts.Now
	if now == nil {
		now = time.Now
	}
	return &Router{
		providers: providers,
		opts:      opts,
		now:       now,
		logger:    opts.Logger,
		slots:     make(chan struct{}, opts.MaxConcurrent),
		cooldown:  make(map[string]time.Time),
	}
}

// Search validates the request, acquires one concurrency slot without
// queueing, and walks providers sequentially within the total budget.
func (r *Router) Search(ctx context.Context, req Request) (Response, error) {
	validated, err := ValidateRequest(req)
	if err != nil {
		return Response{}, err
	}
	select {
	case r.slots <- struct{}{}:
	default:
		return Response{}, ErrBusy
	}
	defer func() { <-r.slots }()

	totalCtx, cancel := context.WithTimeout(ctx, r.opts.TotalTimeout)
	defer cancel()

	attempts := make([]AttemptFailure, 0, len(r.providers))
	firstEmpty := ""
	for _, p := range r.providers {
		if totalCtx.Err() != nil {
			if ctx.Err() != nil {
				return Response{}, ctx.Err()
			}
			break
		}
		if r.cooling(p.Name()) {
			attempts = append(attempts, AttemptFailure{Provider: p.Name(), Kind: KindCooldown})
			continue
		}

		start := r.now()
		results, searchErr := r.attempt(totalCtx, p, validated)
		duration := r.now().Sub(start)
		if searchErr != nil {
			if ctx.Err() != nil {
				return Response{}, ctx.Err()
			}
			pe := asProviderError(searchErr)
			r.logAttempt(duration, p.Name(), pe.Kind, pe.Status, 0)
			attempts = append(attempts, AttemptFailure{Provider: p.Name(), Kind: pe.Kind, Status: pe.Status})
			r.applyCooldown(p.Name(), pe)
			continue
		}

		normalized := NormalizeResults(results, validated.Limit)
		r.logAttempt(duration, p.Name(), "", 0, len(normalized))
		if len(normalized) > 0 {
			return Response{Query: validated.Query, Provider: p.Name(), Results: normalized}, nil
		}
		if firstEmpty == "" {
			firstEmpty = p.Name()
		}
	}

	if firstEmpty != "" {
		return Response{Query: validated.Query, Provider: firstEmpty, Results: []Result{}}, nil
	}
	return Response{}, &UnavailableError{Attempts: attempts}
}

func (r *Router) attempt(ctx context.Context, p Provider, req Request) ([]Result, error) {
	attemptCtx, cancel := context.WithTimeout(ctx, r.opts.AttemptTimeout)
	defer cancel()
	return p.Search(attemptCtx, req)
}

func (r *Router) cooling(name string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	until := r.cooldown[name]
	if until.IsZero() {
		return false
	}
	if !r.now().Before(until) {
		delete(r.cooldown, name)
		return false
	}
	return true
}

// applyCooldown keeps the latest end time: concurrent failures may extend a
// cooldown but never shorten one that was already recorded.
func (r *Router) applyCooldown(name string, pe *ProviderError) {
	until, ok := r.cooldownUntil(pe)
	if !ok {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if until.After(r.cooldown[name]) {
		r.cooldown[name] = until
	}
}

func (r *Router) cooldownUntil(pe *ProviderError) (time.Time, bool) {
	now := r.now()
	switch pe.Kind {
	case KindRateLimited:
		if pe.RetryAt.IsZero() {
			return now.Add(rateLimitCooldown), true
		}
		return now.Add(clampCooldown(pe.RetryAt.Sub(now))), true
	case KindQuotaExhausted:
		if !pe.ResetAt.IsZero() {
			d := pe.ResetAt.Sub(now)
			if d < minCooldown {
				d = minCooldown
			}
			return now.Add(d), true
		}
		if pe.RetryAt.IsZero() {
			return now.Add(quotaCooldown), true
		}
		return now.Add(clampCooldown(pe.RetryAt.Sub(now))), true
	default:
		return time.Time{}, false
	}
}

func clampCooldown(d time.Duration) time.Duration {
	if d < minCooldown {
		return minCooldown
	}
	if d > maxCooldown {
		return maxCooldown
	}
	return d
}

func asProviderError(err error) *ProviderError {
	var pe *ProviderError
	if errors.As(err, &pe) {
		return pe
	}
	return &ProviderError{Kind: KindUpstreamError}
}

// logAttempt records only allow-listed fields: provider, classification,
// status, duration and result count. Query and payloads are never logged.
func (r *Router) logAttempt(duration time.Duration, provider string, kind Kind, status, results int) {
	if r.logger == nil {
		return
	}
	attrs := []any{
		"provider", provider,
		"duration_ms", duration.Milliseconds(),
		"results", results,
	}
	if kind != "" {
		attrs = append(attrs, "kind", string(kind))
	}
	if status != 0 {
		attrs = append(attrs, "status", status)
	}
	if kind == "" {
		r.logger.Debug("provider search succeeded", attrs...)
		return
	}
	r.logger.Warn("provider search failed", attrs...)
}
