package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/ShawnMa123/search-relay-mcp/internal/config"
)

func TestHealthcheck(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/healthz" {
			t.Errorf("request = %s %s", r.Method, r.URL.Path)
		}
		if r.Header.Get("Authorization") != "" {
			t.Errorf("healthcheck sent credentials")
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok\n"))
	}))
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := runHealthcheck(ctx, srv.URL+"/healthz"); err != nil {
		t.Fatalf("healthy endpoint failed: %v", err)
	}

	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer bad.Close()
	if err := runHealthcheck(ctx, bad.URL+"/healthz"); err == nil {
		t.Fatal("non-200 status accepted")
	}

	closed := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	closedURL := closed.URL
	closed.Close()
	if err := runHealthcheck(ctx, closedURL+"/healthz"); err == nil {
		t.Fatal("connection failure accepted")
	}
}

func TestBuildProviderChain(t *testing.T) {
	cfg := config.Config{
		Providers: []string{"anysearch", "keenable"},
		APIKeys: map[string]string{
			"anysearch": "a",
			"keenable":  "k",
		},
	}
	got := buildProviders(cfg, nil)
	if len(got) != 2 || got[0].Name() != "anysearch" || got[1].Name() != "keenable" {
		t.Fatalf("chain = %+v", got)
	}

	subset := buildProviders(config.Config{
		Providers: []string{"tinyfish"},
		APIKeys:   map[string]string{"tinyfish": "t"},
	}, nil)
	if len(subset) != 1 || subset[0].Name() != "tinyfish" {
		t.Fatalf("subset = %+v", subset)
	}
}
