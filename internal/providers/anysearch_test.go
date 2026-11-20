package providers

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ShawnMa123/search-relay-mcp/internal/search"
)

func TestAnySearchSearch(t *testing.T) {
	const key = "test-anysearch-key"
	var gotMethod, gotPath, gotAuth, gotContentType string
	var gotBody map[string]any

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		gotContentType = r.Header.Get("Content-Type")
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		_, _ = w.Write([]byte(`{"code":0,"message":"success","data":{"results":[
			{"title":"Go 1.26","url":"https://go.dev/doc/go1.26","snippet":"release"},
			{"title":"Docs","url":"https://go.dev/doc","snippet":""}
		],"metadata":{"total_results":2}}}`))
	}))
	defer srv.Close()

	p := newAnySearch(NewHTTPClient(), key, srv.URL)
	results, err := p.Search(context.Background(), search.Request{Query: "go 1.26", Limit: 7})
	if err != nil {
		t.Fatal(err)
	}
	if gotMethod != http.MethodPost || gotPath != "/v1/search" {
		t.Fatalf("request = %s %s", gotMethod, gotPath)
	}
	if gotAuth != "Bearer "+key || !strings.HasPrefix(gotContentType, "application/json") {
		t.Fatalf("auth = %q content-type = %q", gotAuth, gotContentType)
	}
	if gotBody["query"] != "go 1.26" || gotBody["max_results"] != float64(7) {
		t.Fatalf("body = %v", gotBody)
	}
	if _, hasTag := gotBody["tag"]; hasTag {
		t.Fatalf("unexpected tag in body: %v", gotBody)
	}
	if len(results) != 2 || results[0].URL != "https://go.dev/doc/go1.26" {
		t.Fatalf("results = %+v", results)
	}
}

func TestAnySearchRejectsBusinessFailure(t *testing.T) {
	cases := []struct {
		name    string
		body    string
		want    search.Kind
		success bool
	}{
		{name: "code zero empty array", body: `{"code":0,"message":"success","data":{"results":[]}}`, success: true},
		{name: "missing code", body: `{"message":"success","data":{"results":[]}}`, want: search.KindInvalidResponse},
		{name: "null code", body: `{"code":null,"data":{"results":[]}}`, want: search.KindInvalidResponse},
		{name: "string code", body: `{"code":"0","data":{"results":[]}}`, want: search.KindInvalidResponse},
		{name: "nonzero code", body: `{"code":-1,"message":"Invalid API key.","error_code":"invalid_api_key"}`, want: search.KindUpstreamError},
		{name: "missing data", body: `{"code":0}`, want: search.KindInvalidResponse},
		{name: "missing results", body: `{"code":0,"data":{}}`, want: search.KindInvalidResponse},
		{name: "null results", body: `{"code":0,"data":{"results":null}}`, want: search.KindInvalidResponse},
		{name: "object results", body: `{"code":0,"data":{"results":{}}}`, want: search.KindInvalidResponse},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_, _ = w.Write([]byte(tc.body))
			}))
			defer srv.Close()
			results, err := newAnySearch(NewHTTPClient(), "k", srv.URL).
				Search(context.Background(), search.Request{Query: "go", Limit: 5})
			if tc.success {
				if err != nil {
					t.Fatalf("expected success: %v", err)
				}
				if results == nil || len(results) != 0 {
					t.Fatalf("results = %#v", results)
				}
				return
			}
			pe := providerError(t, err)
			if pe.Kind != tc.want {
				t.Fatalf("kind = %s, want %s", pe.Kind, tc.want)
			}
		})
	}
}

func TestAnySearchDoesNotLeakErrorBody(t *testing.T) {
	const secret = "anysearch-upstream-secret"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"code":-1,"message":"` + secret + `","echo":"` + secret + `"}`))
	}))
	defer srv.Close()

	_, err := newAnySearch(NewHTTPClient(), "k", srv.URL).
		Search(context.Background(), search.Request{Query: "go", Limit: 5})
	pe := providerError(t, err)
	if pe.Status != http.StatusBadRequest {
		t.Fatalf("status = %d", pe.Status)
	}
	if strings.Contains(err.Error(), secret) {
		t.Fatalf("error leaked body: %s", err)
	}
}
