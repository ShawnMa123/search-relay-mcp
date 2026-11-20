package search

import (
	"encoding/json"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestValidateRequest(t *testing.T) {
	long500 := strings.Repeat("搜", 500)
	long501 := strings.Repeat("搜", 501)

	cases := []struct {
		name    string
		in      Request
		want    string
		wantErr bool
	}{
		{name: "accepts 500 multibyte runes", in: Request{Query: " " + long500 + " ", Limit: 5}, want: long500},
		{name: "rejects 501 runes", in: Request{Query: long501, Limit: 5}, wantErr: true},
		{name: "rejects empty", in: Request{Query: "", Limit: 5}, wantErr: true},
		{name: "rejects whitespace only", in: Request{Query: "  \t", Limit: 5}, wantErr: true},
		{name: "rejects NUL", in: Request{Query: "a\x00b", Limit: 5}, wantErr: true},
		{name: "rejects newline", in: Request{Query: "a\nb", Limit: 5}, wantErr: true},
		{name: "rejects limit 0", in: Request{Query: "go", Limit: 0}, wantErr: true},
		{name: "rejects limit 11", in: Request{Query: "go", Limit: 11}, wantErr: true},
		{name: "accepts limit 1", in: Request{Query: "go", Limit: 1}, want: "go"},
		{name: "accepts limit 10", in: Request{Query: "go", Limit: 10}, want: "go"},
		{name: "keeps operators and inner spacing", in: Request{Query: `  site:go.dev "release  notes" site:go.dev `, Limit: 3}, want: `site:go.dev "release  notes" site:go.dev`},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ValidateRequest(Request{Query: tc.in.Query, Limit: tc.in.Limit})
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected error, got %+v", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got.Query != tc.want {
				t.Fatalf("query = %q, want %q", got.Query, tc.want)
			}
			if got.Limit != tc.in.Limit {
				t.Fatalf("limit = %d, want %d", got.Limit, tc.in.Limit)
			}
		})
	}
}

func TestNormalizeResults(t *testing.T) {
	t.Run("drops fragment duplicates keeping first hit", func(t *testing.T) {
		in := []Result{
			{Title: "first", URL: "https://example.com/a#part"},
			{Title: "second", URL: "https://example.com/a"},
		}
		got := NormalizeResults(in, 5)
		if len(got) != 1 || got[0].Title != "first" || got[0].URL != "https://example.com/a#part" {
			t.Fatalf("got %+v", got)
		}
	})

	t.Run("keeps distinct query parameters", func(t *testing.T) {
		in := []Result{
			{Title: "one", URL: "https://example.com/a?q=1"},
			{Title: "two", URL: "https://example.com/a?q=2"},
		}
		got := NormalizeResults(in, 5)
		if len(got) != 2 {
			t.Fatalf("got %d results: %+v", len(got), got)
		}
	})

	t.Run("drops invalid URLs", func(t *testing.T) {
		in := []Result{
			{Title: "userinfo", URL: "https://alice@example.com/a"},
			{Title: "scheme", URL: "javascript:alert(1)"},
			{Title: "relative", URL: "/only/path"},
			{Title: "empty host", URL: "https:///nohost"},
			{Title: "ok", URL: "https://example.com/ok"},
		}
		got := NormalizeResults(in, 5)
		if len(got) != 1 || got[0].Title != "ok" {
			t.Fatalf("got %+v", got)
		}
	})

	t.Run("drops empty title and overlong URL", func(t *testing.T) {
		longURL := "https://example.com/" + strings.Repeat("a", 2048)
		in := []Result{
			{Title: "   ", URL: "https://example.com/blank"},
			{Title: "long", URL: longURL},
			{Title: " ok ", URL: "https://example.com/ok"},
		}
		got := NormalizeResults(in, 5)
		if len(got) != 1 || got[0].Title != "ok" {
			t.Fatalf("got %+v", got)
		}
	})

	t.Run("truncates unicode without breaking utf8", func(t *testing.T) {
		in := []Result{{
			Title:   strings.Repeat("标", 400),
			URL:     "https://example.com/x",
			Snippet: strings.Repeat("摘", 900),
		}}
		got := NormalizeResults(in, 5)
		if len(got) != 1 {
			t.Fatalf("got %+v", got)
		}
		if n := utf8.RuneCountInString(got[0].Title); n != 300 {
			t.Fatalf("title runes = %d", n)
		}
		if n := utf8.RuneCountInString(got[0].Snippet); n != 800 {
			t.Fatalf("snippet runes = %d", n)
		}
		if !utf8.ValidString(got[0].Title) || !utf8.ValidString(got[0].Snippet) {
			t.Fatal("truncation broke UTF-8")
		}
	})

	t.Run("normalizes only reliable dates", func(t *testing.T) {
		in := []Result{
			{Title: "z", URL: "https://example.com/z", PublishedAt: "2024-05-06T07:08:09Z"},
			{Title: "offset", URL: "https://example.com/offset", PublishedAt: "2024-05-06T07:08:09+02:00"},
			{Title: "date", URL: "https://example.com/date", PublishedAt: "2024-05-06"},
			{Title: "nozone", URL: "https://example.com/nozone", PublishedAt: "2024-05-06T07:08:09"},
			{Title: "garbage", URL: "https://example.com/garbage", PublishedAt: "yesterday"},
			{Title: "unix", URL: "https://example.com/unix", PublishedAt: "1714979289"},
		}
		got := NormalizeResults(in, 10)
		want := map[string]string{
			"z":       "2024-05-06T07:08:09Z",
			"offset":  "2024-05-06T05:08:09Z",
			"date":    "2024-05-06",
			"nozone":  "",
			"garbage": "",
			"unix":    "",
		}
		if len(got) != len(want) {
			t.Fatalf("got %+v", got)
		}
		for _, r := range got {
			var key string
			for k := range want {
				if strings.HasSuffix(r.URL, "/"+k) {
					key = k
				}
			}
			if key == "" {
				t.Fatalf("unexpected result %+v", r)
			}
			if r.PublishedAt != want[key] {
				t.Errorf("%s published_at = %q, want %q", key, r.PublishedAt, want[key])
			}
		}
	})

	t.Run("trims whitespace", func(t *testing.T) {
		in := []Result{{Title: "  t  ", URL: "https://example.com/t", Snippet: "\n s \t"}}
		got := NormalizeResults(in, 5)
		if len(got) != 1 || got[0].Title != "t" || got[0].Snippet != "s" {
			t.Fatalf("got %+v", got)
		}
	})

	t.Run("empty input serializes as empty array", func(t *testing.T) {
		got := NormalizeResults(nil, 5)
		if got == nil {
			t.Fatal("nil slice returned")
		}
		data, err := json.Marshal(got)
		if err != nil {
			t.Fatal(err)
		}
		if string(data) != "[]" {
			t.Fatalf("json = %s", data)
		}
	})

	t.Run("honors limit", func(t *testing.T) {
		in := []Result{
			{Title: "a", URL: "https://example.com/a"},
			{Title: "b", URL: "https://example.com/b"},
			{Title: "c", URL: "https://example.com/c"},
		}
		if got := NormalizeResults(in, 2); len(got) != 2 {
			t.Fatalf("got %d results", len(got))
		}
		if got := NormalizeResults(in, 0); len(got) != 0 || got == nil {
			t.Fatalf("limit 0: %+v", got)
		}
	})
}
