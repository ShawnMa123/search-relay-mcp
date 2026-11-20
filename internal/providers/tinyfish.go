package providers

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/ShawnMa123/search-relay-mcp/internal/search"
)

const tinyFishBaseURL = "https://api.search.tinyfish.ai"

type tinyfish struct {
	client  *http.Client
	key     string
	baseURL string
	now     func() time.Time
}

// NewTinyFish returns the TinySearch adapter.
func NewTinyFish(client *http.Client, key string) search.Provider {
	return newTinyFish(client, key, tinyFishBaseURL, time.Now)
}

func newTinyFish(client *http.Client, key, baseURL string, now func() time.Time) *tinyfish {
	return &tinyfish{client: client, key: key, baseURL: baseURL, now: now}
}

func (t *tinyfish) Name() string { return "tinyfish" }

type tinyFishResponse struct {
	Results json.RawMessage `json:"results"`
}

type tinyFishItem struct {
	Title   string `json:"title"`
	URL     string `json:"url"`
	Snippet string `json:"snippet"`
	Date    string `json:"date"`
}

type tinyFishErrorBody struct {
	Error struct {
		Code    string `json:"code"`
		Details struct {
			Reason string `json:"reason"`
		} `json:"details"`
	} `json:"error"`
}

func (t *tinyfish) Search(ctx context.Context, req search.Request) ([]search.Result, error) {
	u, err := url.Parse(t.baseURL)
	if err != nil {
		return nil, &search.ProviderError{Kind: search.KindNetwork}
	}
	q := u.Query()
	q.Set("query", req.Query)
	u.RawQuery = q.Encode()

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, &search.ProviderError{Kind: search.KindNetwork}
	}
	httpReq.Header.Set("X-API-Key", t.key)

	res, err := fetch(t.client, httpReq)
	if err != nil {
		return nil, err
	}
	if res.Status < 200 || res.Status > 299 {
		pe := statusError(res.Status, res.Header)
		if res.Status == http.StatusTooManyRequests && tinyFishDailyExhausted(res.Body) {
			pe.Kind = search.KindQuotaExhausted
			pe.RetryAt = time.Time{}
			pe.ResetAt = nextUTCMidnight(t.now())
		}
		return nil, pe
	}
	var body tinyFishResponse
	if err := decodeStrict(res.Body, &body); err != nil {
		return nil, &search.ProviderError{Kind: search.KindInvalidResponse, Status: res.Status}
	}
	if len(body.Results) == 0 || strings.TrimSpace(string(body.Results)) == "null" {
		return nil, &search.ProviderError{Kind: search.KindInvalidResponse, Status: res.Status}
	}
	var items []tinyFishItem
	if err := json.Unmarshal(body.Results, &items); err != nil {
		return nil, &search.ProviderError{Kind: search.KindInvalidResponse, Status: res.Status}
	}
	out := make([]search.Result, 0, len(items))
	for _, item := range items {
		out = append(out, search.Result{
			Title:       item.Title,
			URL:         item.URL,
			Snippet:     item.Snippet,
			PublishedAt: item.Date,
		})
	}
	return out, nil
}

func tinyFishDailyExhausted(body []byte) bool {
	var b tinyFishErrorBody
	if err := json.Unmarshal(body, &b); err != nil {
		return false
	}
	return b.Error.Details.Reason == "free_allowance_exhausted"
}

func nextUTCMidnight(now time.Time) time.Time {
	u := now.UTC()
	return time.Date(u.Year(), u.Month(), u.Day()+1, 0, 0, 0, 0, time.UTC)
}
