package mcpserver

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/ShawnMa123/search-relay-mcp/internal/providers"
	"github.com/ShawnMa123/search-relay-mcp/internal/search"
)

const testToken = "test-token-0123456789abcdefghijklmnop"

func guardRequest(t *testing.T, handler http.Handler, method, path string, auth []string, host, origin string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, nil)
	req.Host = host
	for _, v := range auth {
		req.Header.Add("Authorization", v)
	}
	if origin != "" {
		req.Header.Set("Origin", origin)
	}
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	return rec
}

func TestHTTPAccessGuard(t *testing.T) {
	handler := NewHTTPHandler(NewServer(&fakeSearcher{}), testToken)

	cases := []struct {
		name   string
		method string
		auth   []string
		host   string
		origin string
		want   int
	}{
		{name: "post no token", method: http.MethodPost, host: "127.0.0.1:18081", want: http.StatusUnauthorized},
		{name: "get wrong token", method: http.MethodGet, auth: []string{"Bearer wrong"}, host: "127.0.0.1:18081", want: http.StatusUnauthorized},
		{name: "delete no token", method: http.MethodDelete, host: "127.0.0.1:18081", want: http.StatusUnauthorized},
		{name: "duplicate authorization", method: http.MethodPost, auth: []string{"Bearer " + testToken, "Bearer " + testToken}, host: "127.0.0.1:18081", want: http.StatusUnauthorized},
		{name: "concatenated token", method: http.MethodPost, auth: []string{"Bearer " + testToken + "x"}, host: "127.0.0.1:18081", want: http.StatusUnauthorized},
		{name: "forged host", method: http.MethodPost, auth: []string{"Bearer " + testToken}, host: "evil.example:18081", want: http.StatusForbidden},
		{name: "foreign localhost port still localhost", method: http.MethodPost, auth: []string{"Bearer " + testToken}, host: "localhost:9999", want: http.StatusOK},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := guardRequest(t, handler, tc.method, "/mcp", tc.auth, tc.host, tc.origin)
			if tc.want == http.StatusOK {
				if rec.Code == http.StatusUnauthorized || rec.Code == http.StatusForbidden {
					t.Fatalf("valid request rejected: %d", rec.Code)
				}
				return
			}
			if rec.Code != tc.want {
				t.Fatalf("status = %d, want %d", rec.Code, tc.want)
			}
		})
	}

	t.Run("empty origin header rejected", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/mcp", nil)
		req.Host = "127.0.0.1:18081"
		req.Header.Set("Authorization", "Bearer "+testToken)
		req.Header["Origin"] = []string{""}
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if rec.Code != http.StatusForbidden {
			t.Fatalf("status = %d", rec.Code)
		}
	})

	t.Run("origin rejected even with valid token", func(t *testing.T) {
		rec := guardRequest(t, handler, http.MethodPost, "/mcp", []string{"Bearer " + testToken}, "127.0.0.1:18081", "https://example.com")
		if rec.Code != http.StatusForbidden {
			t.Fatalf("status = %d", rec.Code)
		}
	})

	t.Run("missing host rejected", func(t *testing.T) {
		rec := guardRequest(t, handler, http.MethodPost, "/mcp", []string{"Bearer " + testToken}, "", "")
		if rec.Code != http.StatusForbidden {
			t.Fatalf("status = %d", rec.Code)
		}
	})

	t.Run("ipv6 loopback host allowed", func(t *testing.T) {
		for _, host := range []string{"[::1]:18081", "[::1]"} {
			req := httptest.NewRequest(http.MethodGet, "/mcp", nil)
			req.Host = host
			req.Header.Set("Authorization", "Bearer "+testToken)
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)
			if rec.Code == http.StatusUnauthorized || rec.Code == http.StatusForbidden {
				t.Fatalf("%s rejected: %d", host, rec.Code)
			}
		}
	})
}

func TestHTTPHealth(t *testing.T) {
	searcher := &fakeSearcher{}
	handler := NewHTTPHandler(NewServer(searcher), testToken)

	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	req.Host = "127.0.0.1:18081"
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || rec.Body.String() != "ok\n" {
		t.Fatalf("health = %d %q", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "provider") || strings.Contains(rec.Body.String(), "key") {
		t.Fatalf("health leaks details: %q", rec.Body.String())
	}
	if searcher.callCount() != 0 {
		t.Fatal("health touched the searcher")
	}

	req = httptest.NewRequest(http.MethodPost, "/healthz", nil)
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("POST health = %d", rec.Code)
	}
}

func TestHTTPBodyLimit(t *testing.T) {
	searcher := &fakeSearcher{}
	server := httptest.NewServer(NewHTTPHandler(NewServer(searcher), testToken))
	defer server.Close()

	body := bytes.Repeat([]byte("a"), 17<<10)
	for _, chunked := range []bool{false, true} {
		name := "content-length"
		var reader io.Reader = bytes.NewReader(body)
		if chunked {
			name = "chunked"
			reader = io.NopCloser(bytes.NewReader(body))
		}
		t.Run(name, func(t *testing.T) {
			req, err := http.NewRequest(http.MethodPost, server.URL+"/mcp", reader)
			if err != nil {
				t.Fatal(err)
			}
			req.Header.Set("Authorization", "Bearer "+testToken)
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("Accept", "application/json, text/event-stream")
			if chunked {
				req.ContentLength = -1
			}
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			defer resp.Body.Close()
			if resp.StatusCode != http.StatusRequestEntityTooLarge {
				t.Fatalf("status = %d", resp.StatusCode)
			}
			if searcher.callCount() != 0 {
				t.Fatalf("searcher called %d times", searcher.callCount())
			}
		})
	}
}

type roundTripperFunc func(*http.Request) (*http.Response, error)

func (f roundTripperFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

type hostRewriteTransport struct {
	routes map[string]string
}

func (t *hostRewriteTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	target, ok := t.routes[r.URL.Host]
	if !ok {
		return nil, fmt.Errorf("unexpected upstream host")
	}
	u, err := url.Parse(target)
	if err != nil {
		return nil, err
	}
	clone := r.Clone(r.Context())
	clone.URL.Scheme = u.Scheme
	clone.URL.Host = u.Host
	clone.Host = u.Host
	return http.DefaultTransport.RoundTrip(clone)
}

func authedHTTPClient(token string) *http.Client {
	return &http.Client{Transport: roundTripperFunc(func(r *http.Request) (*http.Response, error) {
		clone := r.Clone(r.Context())
		clone.Header.Set("Authorization", "Bearer "+token)
		return http.DefaultTransport.RoundTrip(clone)
	})}
}

func TestStreamableMCPFallback(t *testing.T) {
	const (
		querySentinel = "query-DO-NOT-LOG"
		errorSentinel = "upstream-body-DO-NOT-LOG"
		keenableKey   = "keenable-key-DO-NOT-LOG"
		tinyfishKey   = "tinyfish-key-DO-NOT-LOG"
		clientToken   = "client-token-abcdefghijklmnopqrstuvwxyz"
		resultURL     = "https://example.com/tinyfish-result"
	)

	var keenableHits, tinyfishHits atomic.Int32
	keenableSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		keenableHits.Add(1)
		if r.Header.Get("X-API-Key") != keenableKey {
			t.Errorf("keenable key mismatch")
		}
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = io.WriteString(w, `{"error":"`+errorSentinel+`"}`)
	}))
	defer keenableSrv.Close()
	tinyfishSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		tinyfishHits.Add(1)
		if r.URL.Query().Get("query") != querySentinel {
			t.Errorf("tinyfish query = %q", r.URL.Query().Get("query"))
		}
		_, _ = io.WriteString(w, `{"results":[{"title":"TinyFish hit","url":"`+resultURL+`","snippet":"ok"}],"total_results":1,"page":0}`)
	}))
	defer tinyfishSrv.Close()

	upstream := &http.Client{Transport: &hostRewriteTransport{routes: map[string]string{
		"api.keenable.ai":        keenableSrv.URL,
		"api.search.tinyfish.ai": tinyfishSrv.URL,
	}}}

	var logBuf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logBuf, &slog.HandlerOptions{Level: slog.LevelDebug}))
	router := search.NewRouter([]search.Provider{
		providers.NewKeenable(upstream, keenableKey),
		providers.NewTinyFish(upstream, tinyfishKey),
	}, search.RouterOptions{
		AttemptTimeout: 2 * time.Second,
		TotalTimeout:   5 * time.Second,
		MaxConcurrent:  4,
		Logger:         logger,
	})

	srv := httptest.NewServer(NewHTTPHandler(NewServer(router), clientToken))
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	transport := &mcp.StreamableClientTransport{
		Endpoint:             srv.URL + "/mcp",
		HTTPClient:           authedHTTPClient(clientToken),
		DisableStandaloneSSE: true,
		MaxRetries:           -1,
	}
	client := mcp.NewClient(&mcp.Implementation{Name: "integration-client", Version: "0.0.1"}, nil)
	session, err := client.Connect(ctx, transport, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()

	tools, err := session.ListTools(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(tools.Tools) != 1 || tools.Tools[0].Name != "web_search" {
		t.Fatalf("tools = %+v", tools.Tools)
	}

	res, err := session.CallTool(ctx, &mcp.CallToolParams{
		Name:      "web_search",
		Arguments: map[string]any{"query": querySentinel, "limit": 3},
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.IsError {
		t.Fatalf("tool error: %s", textContent(t, res))
	}
	var structured map[string]any
	raw, _ := json.Marshal(res.StructuredContent)
	if err := json.Unmarshal(raw, &structured); err != nil {
		t.Fatal(err)
	}
	if structured["provider"] != "tinyfish" {
		t.Fatalf("structured = %v", structured)
	}
	results, _ := structured["results"].([]any)
	if len(results) != 1 || results[0].(map[string]any)["url"] != resultURL {
		t.Fatalf("results = %v", structured["results"])
	}
	if keenableHits.Load() != 1 || tinyfishHits.Load() != 1 {
		t.Fatalf("hits = keenable %d, tinyfish %d", keenableHits.Load(), tinyfishHits.Load())
	}

	logText := logBuf.String()
	for _, secret := range []string{querySentinel, errorSentinel, keenableKey, tinyfishKey, clientToken} {
		if strings.Contains(logText, secret) {
			t.Fatalf("logs leaked %q:\n%s", secret, logText)
		}
	}
}

type blockingProvider struct {
	name    string
	started chan struct{}
	once    sync.Once
	calls   atomic.Int32
}

func (p *blockingProvider) Name() string { return p.name }

func (p *blockingProvider) Search(ctx context.Context, _ search.Request) ([]search.Result, error) {
	p.calls.Add(1)
	p.once.Do(func() { close(p.started) })
	<-ctx.Done()
	return nil, ctx.Err()
}

type countingProvider struct {
	name  string
	calls atomic.Int32
}

func (p *countingProvider) Name() string { return p.name }

func (p *countingProvider) Search(context.Context, search.Request) ([]search.Result, error) {
	p.calls.Add(1)
	return []search.Result{{Title: "hit", URL: "https://example.com/hit"}}, nil
}

func TestStreamableMCPCancel(t *testing.T) {
	blocking := &blockingProvider{name: "keenable", started: make(chan struct{})}
	backup := &countingProvider{name: "tinyfish"}
	router := search.NewRouter([]search.Provider{blocking, backup}, search.RouterOptions{
		AttemptTimeout: 30 * time.Second,
		TotalTimeout:   30 * time.Second,
		MaxConcurrent:  4,
	})

	srv := httptest.NewServer(NewHTTPHandler(NewServer(router), testToken))
	defer srv.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	transport := &mcp.StreamableClientTransport{
		Endpoint:             srv.URL + "/mcp",
		HTTPClient:           authedHTTPClient(testToken),
		DisableStandaloneSSE: true,
		MaxRetries:           -1,
	}
	client := mcp.NewClient(&mcp.Implementation{Name: "cancel-client", Version: "0.0.1"}, nil)
	session, err := client.Connect(ctx, transport, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()

	callDone := make(chan struct{})
	go func() {
		defer close(callDone)
		_, _ = session.CallTool(ctx, &mcp.CallToolParams{
			Name:      "web_search",
			Arguments: map[string]any{"query": "cancel me"},
		})
	}()

	select {
	case <-blocking.started:
	case <-time.After(5 * time.Second):
		t.Fatal("provider did not start")
	}
	cancel()
	select {
	case <-callDone:
	case <-time.After(5 * time.Second):
		t.Fatal("cancelled call did not return")
	}
	time.Sleep(100 * time.Millisecond)
	if backup.calls.Load() != 0 {
		t.Fatalf("backup provider called %d times after cancellation", backup.calls.Load())
	}
	if blocking.calls.Load() != 1 {
		t.Fatalf("blocking provider called %d times", blocking.calls.Load())
	}
}
