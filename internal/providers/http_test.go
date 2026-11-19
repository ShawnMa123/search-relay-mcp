package providers

import (
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ShawnMa123/search-relay-mcp/internal/search"
)

type roundTripperFunc func(*http.Request) (*http.Response, error)

func (f roundTripperFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func providerError(t *testing.T, err error) *search.ProviderError {
	t.Helper()
	var pe *search.ProviderError
	if !errors.As(err, &pe) {
		t.Fatalf("error %v is not *search.ProviderError", err)
	}
	return pe
}

func TestHTTPRejectsRedirect(t *testing.T) {
	var targetHits atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		targetHits.Add(1)
		_, _ = w.Write([]byte(`{"results":[]}`))
	}))
	defer target.Close()

	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL, http.StatusFound)
	}))
	defer source.Close()

	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, source.URL, nil)
	if err != nil {
		t.Fatal(err)
	}
	var out map[string]any
	err = doJSON(NewHTTPClient(), req, &out)
	pe := providerError(t, err)
	if pe.Kind != search.KindUpstreamError || pe.Status != http.StatusFound {
		t.Fatalf("got %+v", pe)
	}
	if n := targetHits.Load(); n != 0 {
		t.Fatalf("redirect target received %d requests", n)
	}
}

func TestHTTPDoesNotLeakTransportDetails(t *testing.T) {
	const fakeKey = "key-DO-NOT-ECHO"
	const fakeQuery = "query-DO-NOT-ECHO"
	client := &http.Client{Transport: roundTripperFunc(func(*http.Request) (*http.Response, error) {
		return nil, fmt.Errorf("dial failed for %s?query=%s", fakeKey, fakeQuery)
	})}
	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, "https://api.example.com/x", nil)
	if err != nil {
		t.Fatal(err)
	}
	var out map[string]any
	err = doJSON(client, req, &out)
	pe := providerError(t, err)
	if pe.Kind != search.KindNetwork {
		t.Fatalf("kind = %s", pe.Kind)
	}
	msg := err.Error()
	if strings.Contains(msg, fakeKey) || strings.Contains(msg, fakeQuery) {
		t.Fatalf("error leaked request data: %s", msg)
	}
}

func TestHTTPLimitsDecodedBody(t *testing.T) {
	t.Run("gzip bomb rejected after decompression", func(t *testing.T) {
		var compressed bytes.Buffer
		zw := gzip.NewWriter(&compressed)
		if _, err := io.Copy(zw, bytes.NewReader(bytes.Repeat([]byte("a"), 2<<20))); err != nil {
			t.Fatal(err)
		}
		if err := zw.Close(); err != nil {
			t.Fatal(err)
		}
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Encoding", "gzip")
			_, _ = w.Write(compressed.Bytes())
		}))
		defer srv.Close()

		req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, srv.URL, nil)
		if err != nil {
			t.Fatal(err)
		}
		var out map[string]any
		pe := providerError(t, doJSON(NewHTTPClient(), req, &out))
		if pe.Kind != search.KindInvalidResponse {
			t.Fatalf("kind = %s", pe.Kind)
		}
	})

	t.Run("empty body rejected", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
		defer srv.Close()
		req, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, srv.URL, nil)
		var out map[string]any
		pe := providerError(t, doJSON(NewHTTPClient(), req, &out))
		if pe.Kind != search.KindInvalidResponse {
			t.Fatalf("kind = %s", pe.Kind)
		}
	})

	t.Run("trailing data rejected", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_, _ = w.Write([]byte(`{"results":[]} trailing`))
		}))
		defer srv.Close()
		req, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, srv.URL, nil)
		var out map[string]any
		pe := providerError(t, doJSON(NewHTTPClient(), req, &out))
		if pe.Kind != search.KindInvalidResponse {
			t.Fatalf("kind = %s", pe.Kind)
		}
	})
}

func TestHTTPClassifiesStatus(t *testing.T) {
	cases := []struct {
		status int
		want   search.Kind
	}{
		{http.StatusUnauthorized, search.KindAuthFailed},
		{http.StatusForbidden, search.KindAuthFailed},
		{http.StatusPaymentRequired, search.KindQuotaExhausted},
		{http.StatusTooManyRequests, search.KindRateLimited},
		{http.StatusInternalServerError, search.KindUpstreamError},
		{http.StatusServiceUnavailable, search.KindUpstreamError},
	}
	for _, tc := range cases {
		t.Run(http.StatusText(tc.status), func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tc.status)
			}))
			defer srv.Close()
			req, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, srv.URL, nil)
			var out map[string]any
			pe := providerError(t, doJSON(NewHTTPClient(), req, &out))
			if pe.Kind != tc.want || pe.Status != tc.status {
				t.Fatalf("got kind=%s status=%d", pe.Kind, pe.Status)
			}
		})
	}
}

func TestRetryAfterParsing(t *testing.T) {
	now := time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)

	if got, ok := parseRetryAfter("120", now); !ok || !got.Equal(now.Add(120*time.Second)) {
		t.Fatalf("seconds: %v %v", got, ok)
	}
	when := now.Add(90 * time.Second).Truncate(time.Second)
	if got, ok := parseRetryAfter(when.Format(http.TimeFormat), now); !ok || !got.Equal(when) {
		t.Fatalf("http-date: %v %v", got, ok)
	}
	if _, ok := parseRetryAfter("soon", now); ok {
		t.Fatal("invalid Retry-After accepted")
	}
	if _, ok := parseRetryAfter("", now); ok {
		t.Fatal("empty Retry-After accepted")
	}
}

func TestStatusErrorCarriesRetryAt(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", "60")
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer srv.Close()

	before := time.Now()
	req, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, srv.URL, nil)
	var out map[string]any
	pe := providerError(t, doJSON(NewHTTPClient(), req, &out))
	if pe.RetryAt.Before(before.Add(59*time.Second)) || pe.RetryAt.After(time.Now().Add(61*time.Second)) {
		t.Fatalf("RetryAt = %v", pe.RetryAt)
	}
}
