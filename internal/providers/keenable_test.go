package providers

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ShawnMa123/search-relay-mcp/internal/search"
)

func TestKeenableSearch(t *testing.T) {
	const key = "test-keenable-key"
	var gotMethod, gotPath, gotAPIKey, gotAuth, gotBody string

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		gotAPIKey = r.Header.Get("X-API-Key")
		gotAuth = r.Header.Get("Authorization")
		var buf strings.Builder
		_, _ = io.Copy(&buf, r.Body)
		gotBody = buf.String()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"query":"go","mode":"pro","results":[
			{"title":" Release notes ","url":"https://go.dev/doc/devel/release","snippet":"history","published_at":"2026-10-30T00:00:00Z"},
			{"title":"No date","url":"https://example.com/x","snippet":""}
		]}`))
	}))
	defer srv.Close()

	p := newKeenable(NewHTTPClient(), key, srv.URL)
	results, err := p.Search(context.Background(), search.Request{Query: "go", Limit: 5})
	if err != nil {
		t.Fatal(err)
	}
	if gotMethod != http.MethodPost || gotPath != "/v1/search" {
		t.Fatalf("request = %s %s", gotMethod, gotPath)
	}
	if gotAPIKey != key || gotAuth != "" {
		t.Fatalf("api key = %q, authorization = %q", gotAPIKey, gotAuth)
	}
	var body map[string]any
	if err := json.Unmarshal([]byte(gotBody), &body); err != nil {
		t.Fatal(err)
	}
	if body["query"] != "go" || body["max_results"] != float64(5) {
		t.Fatalf("body = %v", body)
	}
	if len(results) != 2 {
		t.Fatalf("results = %+v", results)
	}
	if results[0].Title != " Release notes " || results[0].URL != "https://go.dev/doc/devel/release" ||
		results[0].PublishedAt != "2026-10-30T00:00:00Z" {
		t.Fatalf("first = %+v", results[0])
	}
	if results[1].PublishedAt != "" {
		t.Fatalf("second = %+v", results[1])
	}
}

func TestKeenableEmptyResults(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"query":"go","results":[]}`))
	}))
	defer srv.Close()

	results, err := newKeenable(NewHTTPClient(), "k", srv.URL).Search(context.Background(), search.Request{Query: "go", Limit: 5})
	if err != nil {
		t.Fatalf("legal empty array failed: %v", err)
	}
	if results == nil || len(results) != 0 {
		t.Fatalf("results = %#v", results)
	}
}

func TestKeenableMissingResults(t *testing.T) {
	for name, body := range map[string]string{
		"missing": `{"query":"go"}`,
		"null":    `{"query":"go","results":null}`,
		"object":  `{"query":"go","results":{}}`,
	} {
		t.Run(name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_, _ = w.Write([]byte(body))
			}))
			defer srv.Close()
			_, err := newKeenable(NewHTTPClient(), "k", srv.URL).Search(context.Background(), search.Request{Query: "go", Limit: 5})
			pe := providerError(t, err)
			if pe.Kind != search.KindInvalidResponse {
				t.Fatalf("kind = %s", pe.Kind)
			}
		})
	}
}

func TestKeenableIgnoresBodyOnErrorStatus(t *testing.T) {
	const secret = "upstream-body-secret"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"results":[{"title":"should not parse","url":"https://example.com"}],"secret":"` + secret + `"}`))
	}))
	defer srv.Close()

	results, err := newKeenable(NewHTTPClient(), "k", srv.URL).Search(context.Background(), search.Request{Query: "go", Limit: 5})
	pe := providerError(t, err)
	if pe.Kind != search.KindUpstreamError || pe.Status != http.StatusInternalServerError {
		t.Fatalf("got %+v", pe)
	}
	if results != nil {
		t.Fatalf("results parsed from error body: %+v", results)
	}
	if strings.Contains(err.Error(), secret) {
		t.Fatalf("error leaked body: %s", err)
	}
}
