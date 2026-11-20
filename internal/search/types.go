package search

import (
	"context"
	"errors"
	"fmt"
	"time"
)

// Request is a validated search request. Limit is always concrete.
type Request struct {
	Query string `json:"query"`
	Limit int    `json:"limit"`
}

// Result is a single normalized search hit.
type Result struct {
	Title       string `json:"title"`
	URL         string `json:"url"`
	Snippet     string `json:"snippet"`
	PublishedAt string `json:"published_at,omitempty"`
}

// Response is the payload returned to the MCP client.
type Response struct {
	Query    string   `json:"query"`
	Provider string   `json:"provider"`
	Results  []Result `json:"results"`
}

// Provider is a single upstream search backend.
type Provider interface {
	Name() string
	Search(ctx context.Context, req Request) ([]Result, error)
}

// Kind is a stable failure classification. Values are part of the public
// tool-error contract and never contain upstream payloads.
type Kind string

const (
	KindTimeout         Kind = "timeout"
	KindNetwork         Kind = "network"
	KindRateLimited     Kind = "rate_limited"
	KindQuotaExhausted  Kind = "quota_exhausted"
	KindAuthFailed      Kind = "auth_failed"
	KindUpstreamError   Kind = "upstream_error"
	KindInvalidResponse Kind = "invalid_response"
	// KindCooldown marks a provider skipped because an earlier call put it
	// in cooldown. It is only used in aggregated tool errors.
	KindCooldown Kind = "cooldown"
)

// ProviderError carries a safe classification for one upstream failure.
// It intentionally does not wrap the underlying error: error strings may be
// surfaced to clients and must not leak request data.
type ProviderError struct {
	Kind    Kind
	Status  int
	RetryAt time.Time
	// ResetAt is a documented quota reset (for example TinyFish's next UTC
	// midnight). Unlike RetryAt it is not bounded by the router's 1h cap.
	ResetAt time.Time
}

func (e *ProviderError) Error() string {
	if e.Status != 0 {
		return fmt.Sprintf("upstream %s (status %d)", e.Kind, e.Status)
	}
	return "upstream " + string(e.Kind)
}

var (
	ErrEmptyQuery       = errors.New("query is required")
	ErrQueryTooLong     = errors.New("query exceeds 500 characters")
	ErrQueryControlChar = errors.New("query contains control characters")
	ErrLimitRange       = errors.New("limit must be between 1 and 10")
)
