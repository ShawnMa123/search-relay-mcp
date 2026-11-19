package search

import (
	"net/url"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

const (
	maxQueryRunes   = 500
	maxTitleRunes   = 300
	maxSnippetRunes = 800
	maxURLBytes     = 2048
)

// ValidateRequest trims the query and validates it against the public
// contract. It never echoes the query in returned errors.
func ValidateRequest(req Request) (Request, error) {
	query := strings.TrimSpace(req.Query)
	if query == "" {
		return Request{}, ErrEmptyQuery
	}
	if utf8.RuneCountInString(query) > maxQueryRunes {
		return Request{}, ErrQueryTooLong
	}
	for _, r := range query {
		if unicode.IsControl(r) {
			return Request{}, ErrQueryControlChar
		}
	}
	if req.Limit < 1 || req.Limit > 10 {
		return Request{}, ErrLimitRange
	}
	return Request{Query: query, Limit: req.Limit}, nil
}

// NormalizeResults cleans upstream hits: trims, validates URLs, removes
// fragment duplicates keeping the first hit, normalizes dates and caps the
// result count. The returned slice is never nil.
func NormalizeResults(results []Result, limit int) []Result {
	out := make([]Result, 0, len(results))
	if limit <= 0 {
		return out
	}
	seen := make(map[string]struct{}, len(results))
	for _, r := range results {
		title := strings.TrimSpace(r.Title)
		if title == "" {
			continue
		}
		rawURL := strings.TrimSpace(r.URL)
		if len(rawURL) > maxURLBytes {
			continue
		}
		u, err := url.Parse(rawURL)
		if err != nil || u.User != nil || u.Host == "" {
			continue
		}
		if u.Scheme != "http" && u.Scheme != "https" {
			continue
		}
		u.Fragment = ""
		key := u.String()
		if _, dup := seen[key]; dup {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, Result{
			Title:       truncateRunes(title, maxTitleRunes),
			URL:         rawURL,
			Snippet:     truncateRunes(strings.TrimSpace(r.Snippet), maxSnippetRunes),
			PublishedAt: normalizePublishedAt(r.PublishedAt),
		})
		if len(out) == limit {
			break
		}
	}
	return out
}

func truncateRunes(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	i := 0
	for pos := range s {
		if i == n {
			return s[:pos]
		}
		i++
	}
	return s
}

func normalizePublishedAt(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t.UTC().Format(time.RFC3339)
	}
	if t, err := time.Parse("2006-01-02", s); err == nil {
		return t.Format("2006-01-02")
	}
	return ""
}
