package providers

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/ShawnMa123/search-relay-mcp/internal/search"
)

func TestTinyFishSearch(t *testing.T) {
	const key = "test-tinyfish-key"
	var gotMethod, gotPath, gotAPIKey, gotQuery string
	var gotHasMaxResults bool

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		gotAPIKey = r.Header.Get("X-API-Key")
		gotQuery = r.URL.Query().Get("query")
		gotHasMaxResults = r.URL.Query().Has("max_results")
		_, _ = w.Write([]byte(`{"query":"中文 查询","results":[
			{"title":"结果","url":"https://example.cn/a","snippet":"摘要","date":"2026-11-19"},
			{"title":"no date","url":"https://example.cn/b","snippet":""}
		],"total_results":2,"page":0}`))
	}))
	defer srv.Close()

	p := newTinyFish(NewHTTPClient(), key, srv.URL, time.Now)
	results, err := p.Search(context.Background(), search.Request{Query: "中文 查询", Limit: 5})
	if err != nil {
		t.Fatal(err)
	}
	if gotMethod != http.MethodGet || gotPath != "/" {
		t.Fatalf("request = %s %s", gotMethod, gotPath)
	}
	if gotAPIKey != key {
		t.Fatalf("X-API-Key = %q", gotAPIKey)
	}
	if gotQuery != "中文 查询" {
		t.Fatalf("query = %q", gotQuery)
	}
	if gotHasMaxResults {
		t.Fatal("max_results must not be sent to TinySearch")
	}
	if len(results) != 2 || results[0].Title != "结果" || results[0].PublishedAt != "2026-11-19" {
		t.Fatalf("results = %+v", results)
	}
	if results[1].PublishedAt != "" {
		t.Fatalf("second = %+v", results[1])
	}
}

func TestTinyFishEmptyResults(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"query":"go","results":[],"total_results":0,"page":0}`))
	}))
	defer srv.Close()

	results, err := newTinyFish(NewHTTPClient(), "k", srv.URL, time.Now).Search(context.Background(), search.Request{Query: "go", Limit: 5})
	if err != nil {
		t.Fatalf("legal empty array failed: %v", err)
	}
	if results == nil || len(results) != 0 {
		t.Fatalf("results = %#v", results)
	}
}

func TestTinyFishDailyLimit(t *testing.T) {
	fixed := time.Date(2026, 11, 20, 23, 30, 0, 0, time.UTC)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(`{"error":{"code":"RATE_LIMIT_EXCEEDED","message":"daily limit","details":{"reason":"free_allowance_exhausted","window_seconds":86400}}}`))
	}))
	defer srv.Close()

	_, err := newTinyFish(NewHTTPClient(), "k", srv.URL, func() time.Time { return fixed }).
		Search(context.Background(), search.Request{Query: "go", Limit: 5})
	pe := providerError(t, err)
	if pe.Kind != search.KindQuotaExhausted {
		t.Fatalf("kind = %s", pe.Kind)
	}
	want := time.Date(2026, 11, 21, 0, 0, 0, 0, time.UTC)
	if !pe.ResetAt.Equal(want) {
		t.Fatalf("ResetAt = %v, want %v", pe.ResetAt, want)
	}
}

func TestTinyFishPlainRateLimit(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", "30")
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(`{"error":{"code":"RATE_LIMIT_EXCEEDED","message":"per-minute limit","details":{"limit":30,"unit":"requests","window_seconds":60}}}`))
	}))
	defer srv.Close()

	_, err := newTinyFish(NewHTTPClient(), "k", srv.URL, time.Now).
		Search(context.Background(), search.Request{Query: "go", Limit: 5})
	pe := providerError(t, err)
	if pe.Kind != search.KindRateLimited {
		t.Fatalf("kind = %s", pe.Kind)
	}
	if !pe.ResetAt.IsZero() {
		t.Fatalf("plain rate limit must not set ResetAt: %v", pe.ResetAt)
	}
	if pe.RetryAt.IsZero() {
		t.Fatal("Retry-After not parsed")
	}
}

func TestTinyFishPaymentRequiredUsesNormalQuota(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusPaymentRequired)
		_, _ = w.Write([]byte(`{"error":{"code":"INSUFFICIENT_CREDITS","message":"wallet empty"}}`))
	}))
	defer srv.Close()

	_, err := newTinyFish(NewHTTPClient(), "k", srv.URL, time.Now).
		Search(context.Background(), search.Request{Query: "go", Limit: 5})
	pe := providerError(t, err)
	if pe.Kind != search.KindQuotaExhausted {
		t.Fatalf("kind = %s", pe.Kind)
	}
	if !pe.ResetAt.IsZero() {
		t.Fatalf("402 must not set ResetAt: %v", pe.ResetAt)
	}
}

func TestTinyFishDoesNotLeakErrorBody(t *testing.T) {
	const secret = "tinyfish-upstream-secret"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"error":{"code":"INTERNAL","message":"` + secret + `"}}`))
	}))
	defer srv.Close()

	_, err := newTinyFish(NewHTTPClient(), "k", srv.URL, time.Now).
		Search(context.Background(), search.Request{Query: "go", Limit: 5})
	var pe *search.ProviderError
	if !errors.As(err, &pe) {
		t.Fatalf("unexpected error type: %v", err)
	}
	if strings.Contains(pe.Error(), secret) {
		t.Fatalf("error leaked body: %s", pe.Error())
	}
}
