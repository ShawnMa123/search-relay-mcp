package providers

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"strings"

	"github.com/ShawnMa123/search-relay-mcp/internal/search"
)

const keenableBaseURL = "https://api.keenable.ai"

type keenable struct {
	client  *http.Client
	key     string
	baseURL string
}

// NewKeenable returns the Keenable adapter. The endpoint is fixed in
// production; tests use newKeenable with a local server.
func NewKeenable(client *http.Client, key string) search.Provider {
	return newKeenable(client, key, keenableBaseURL)
}

func newKeenable(client *http.Client, key, baseURL string) *keenable {
	return &keenable{client: client, key: key, baseURL: baseURL}
}

func (k *keenable) Name() string { return "keenable" }

type keenableResponse struct {
	Results json.RawMessage `json:"results"`
}

type keenableItem struct {
	Title       string `json:"title"`
	URL         string `json:"url"`
	Snippet     string `json:"snippet"`
	PublishedAt string `json:"published_at"`
}

func (k *keenable) Search(ctx context.Context, req search.Request) ([]search.Result, error) {
	payload, err := json.Marshal(struct {
		Query      string `json:"query"`
		MaxResults int    `json:"max_results"`
	}{Query: req.Query, MaxResults: req.Limit})
	if err != nil {
		return nil, &search.ProviderError{Kind: search.KindInvalidResponse}
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, k.baseURL+"/v1/search", bytes.NewReader(payload))
	if err != nil {
		return nil, &search.ProviderError{Kind: search.KindNetwork}
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("X-API-Key", k.key)

	var body keenableResponse
	if err := doJSON(k.client, httpReq, &body); err != nil {
		return nil, err
	}
	if len(body.Results) == 0 || strings.TrimSpace(string(body.Results)) == "null" {
		return nil, &search.ProviderError{Kind: search.KindInvalidResponse}
	}
	var items []keenableItem
	if err := json.Unmarshal(body.Results, &items); err != nil {
		return nil, &search.ProviderError{Kind: search.KindInvalidResponse}
	}
	out := make([]search.Result, 0, len(items))
	for _, item := range items {
		out = append(out, search.Result{
			Title:       item.Title,
			URL:         item.URL,
			Snippet:     item.Snippet,
			PublishedAt: item.PublishedAt,
		})
	}
	return out, nil
}
