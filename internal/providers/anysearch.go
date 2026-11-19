package providers

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"strings"

	"github.com/ShawnMa123/search-relay-mcp/internal/search"
)

const anySearchBaseURL = "https://api.anysearch.com"

type anysearch struct {
	client  *http.Client
	key     string
	baseURL string
}

// NewAnySearch returns the AnySearch adapter.
func NewAnySearch(client *http.Client, key string) search.Provider {
	return newAnySearch(client, key, anySearchBaseURL)
}

func newAnySearch(client *http.Client, key, baseURL string) *anysearch {
	return &anysearch{client: client, key: key, baseURL: baseURL}
}

func (a *anysearch) Name() string { return "anysearch" }

type anySearchResponse struct {
	Code json.RawMessage `json:"code"`
	Data json.RawMessage `json:"data"`
}

type anySearchData struct {
	Results json.RawMessage `json:"results"`
}

type anySearchItem struct {
	Title   string `json:"title"`
	URL     string `json:"url"`
	Snippet string `json:"snippet"`
}

func (a *anysearch) Search(ctx context.Context, req search.Request) ([]search.Result, error) {
	payload, err := json.Marshal(struct {
		Query      string `json:"query"`
		MaxResults int    `json:"max_results"`
	}{Query: req.Query, MaxResults: req.Limit})
	if err != nil {
		return nil, &search.ProviderError{Kind: search.KindInvalidResponse}
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, a.baseURL+"/v1/search", bytes.NewReader(payload))
	if err != nil {
		return nil, &search.ProviderError{Kind: search.KindNetwork}
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+a.key)

	var body anySearchResponse
	if err := doJSON(a.client, httpReq, &body); err != nil {
		return nil, err
	}

	// A missing or null code must not be read as Go's zero value.
	var code *int
	if len(body.Code) == 0 || json.Unmarshal(body.Code, &code) != nil || code == nil {
		return nil, &search.ProviderError{Kind: search.KindInvalidResponse}
	}
	if *code != 0 {
		return nil, &search.ProviderError{Kind: search.KindUpstreamError}
	}

	if len(body.Data) == 0 || strings.TrimSpace(string(body.Data)) == "null" {
		return nil, &search.ProviderError{Kind: search.KindInvalidResponse}
	}
	var data anySearchData
	if err := json.Unmarshal(body.Data, &data); err != nil {
		return nil, &search.ProviderError{Kind: search.KindInvalidResponse}
	}
	if len(data.Results) == 0 || strings.TrimSpace(string(data.Results)) == "null" {
		return nil, &search.ProviderError{Kind: search.KindInvalidResponse}
	}
	var items []anySearchItem
	if err := json.Unmarshal(data.Results, &items); err != nil {
		return nil, &search.ProviderError{Kind: search.KindInvalidResponse}
	}
	out := make([]search.Result, 0, len(items))
	for _, item := range items {
		out = append(out, search.Result{
			Title:   item.Title,
			URL:     item.URL,
			Snippet: item.Snippet,
		})
	}
	return out, nil
}
