package mcpserver

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/ShawnMa123/search-relay-mcp/internal/search"
)

type fakeSearcher struct {
	mu    sync.Mutex
	calls int
	last  search.Request
	resp  search.Response
	err   error
}

func (f *fakeSearcher) Search(_ context.Context, req search.Request) (search.Response, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	f.last = req
	return f.resp, f.err
}

func (f *fakeSearcher) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

func connectInMemory(t *testing.T, searcher Searcher) *mcp.ClientSession {
	t.Helper()
	server := NewServer(searcher)
	ct, st := mcp.NewInMemoryTransports()
	ctx := context.Background()
	ss, err := server.Connect(ctx, st, nil)
	if err != nil {
		t.Fatal(err)
	}
	client := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "0.0.1"}, nil)
	cs, err := client.Connect(ctx, ct, nil)
	if err != nil {
		ss.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cs.Close()
		ss.Close()
	})
	return cs
}

func TestSearchToolContract(t *testing.T) {
	ctx := context.Background()
	searcher := &fakeSearcher{resp: search.Response{
		Query:    "go",
		Provider: "keenable",
		Results:  []search.Result{{Title: "Release notes", URL: "https://go.dev/doc", Snippet: "history"}},
	}}
	session := connectInMemory(t, searcher)

	tools, err := session.ListTools(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(tools.Tools) != 1 || tools.Tools[0].Name != "web_search" {
		t.Fatalf("tools = %+v", tools.Tools)
	}

	res, err := session.CallTool(ctx, &mcp.CallToolParams{
		Name:      "web_search",
		Arguments: map[string]any{"query": "go"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.IsError {
		t.Fatalf("tool error: %+v", res.Content)
	}
	if searcher.last.Limit != 5 {
		t.Fatalf("default limit = %d", searcher.last.Limit)
	}

	structured, err := json.Marshal(res.StructuredContent)
	if err != nil {
		t.Fatal(err)
	}
	var asMap map[string]any
	if err := json.Unmarshal(structured, &asMap); err != nil {
		t.Fatal(err)
	}
	if asMap["query"] != "go" || asMap["provider"] != "keenable" {
		t.Fatalf("structured = %v", asMap)
	}
	if len(res.Content) != 1 {
		t.Fatalf("content = %+v", res.Content)
	}
	text, ok := res.Content[0].(*mcp.TextContent)
	if !ok {
		t.Fatalf("content type = %T", res.Content[0])
	}
	var textMap map[string]any
	if err := json.Unmarshal([]byte(text.Text), &textMap); err != nil {
		t.Fatalf("text is not JSON: %v", err)
	}
	if !reflect.DeepEqual(asMap, textMap) {
		t.Fatalf("text and structured differ:\n%s\n%s", structured, text.Text)
	}
}

func TestSearchToolRejectsInvalidInput(t *testing.T) {
	ctx := context.Background()
	cases := []struct {
		name string
		args map[string]any
	}{
		{name: "extra field", args: map[string]any{"query": "go", "extra": true}},
		{name: "null limit", args: map[string]any{"query": "go", "limit": nil}},
		{name: "zero limit", args: map[string]any{"query": "go", "limit": 0}},
		{name: "eleven limit", args: map[string]any{"query": "go", "limit": 11}},
		{name: "decimal limit", args: map[string]any{"query": "go", "limit": 1.5}},
		{name: "string limit", args: map[string]any{"query": "go", "limit": "5"}},
		{name: "empty query", args: map[string]any{"query": ""}},
		{name: "whitespace query", args: map[string]any{"query": "   "}},
		{name: "non-string query", args: map[string]any{"query": 5}},
		{name: "missing query", args: map[string]any{}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			searcher := &fakeSearcher{resp: search.Response{Query: "go", Provider: "fake", Results: []search.Result{}}}
			session := connectInMemory(t, searcher)
			res, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "web_search", Arguments: tc.args})
			if err != nil {
				t.Fatalf("protocol error: %v", err)
			}
			if !res.IsError {
				t.Fatalf("expected tool error, got %+v", res)
			}
			if searcher.callCount() != 0 {
				t.Fatalf("searcher called %d times", searcher.callCount())
			}
		})
	}
}

func TestSearchToolErrorMapping(t *testing.T) {
	ctx := context.Background()

	t.Run("busy", func(t *testing.T) {
		session := connectInMemory(t, &fakeSearcher{err: search.ErrBusy})
		res, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "web_search", Arguments: map[string]any{"query": "go"}})
		if err != nil {
			t.Fatal(err)
		}
		if !res.IsError || !textContains(t, res, "busy") {
			t.Fatalf("result = %+v", res)
		}
	})

	t.Run("unavailable lists safe classifications", func(t *testing.T) {
		session := connectInMemory(t, &fakeSearcher{err: &search.UnavailableError{Attempts: []search.AttemptFailure{
			{Provider: "keenable", Kind: search.KindUpstreamError, Status: 500},
			{Provider: "tinyfish", Kind: search.KindRateLimited, Status: 429},
			{Provider: "anysearch", Kind: search.KindCooldown},
		}}})
		res, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "web_search", Arguments: map[string]any{"query": "go"}})
		if err != nil {
			t.Fatal(err)
		}
		text := textContent(t, res)
		if !res.IsError || !strings.Contains(text, "upstream_unavailable") ||
			!strings.Contains(text, "keenable=upstream_error(500)") ||
			!strings.Contains(text, "anysearch=cooldown") {
			t.Fatalf("text = %s", text)
		}
	})

	t.Run("legal empty results are a success", func(t *testing.T) {
		session := connectInMemory(t, &fakeSearcher{resp: search.Response{Query: "go", Provider: "keenable", Results: nil}})
		res, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "web_search", Arguments: map[string]any{"query": "go"}})
		if err != nil {
			t.Fatal(err)
		}
		if res.IsError {
			t.Fatalf("empty results reported as error: %+v", res.Content)
		}
		if !strings.Contains(textContent(t, res), `"results":[]`) {
			t.Fatalf("text = %s", textContent(t, res))
		}
	})

	t.Run("unknown error text is not echoed", func(t *testing.T) {
		session := connectInMemory(t, &fakeSearcher{err: errors.New("secret-upstream-body")})
		res, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "web_search", Arguments: map[string]any{"query": "go"}})
		if err != nil {
			t.Fatal(err)
		}
		text := textContent(t, res)
		if strings.Contains(text, "secret-upstream-body") {
			t.Fatalf("leaked error text: %s", text)
		}
		if !res.IsError {
			t.Fatal("expected tool error")
		}
	})
}

func textContent(t *testing.T, res *mcp.CallToolResult) string {
	t.Helper()
	if len(res.Content) != 1 {
		t.Fatalf("content = %+v", res.Content)
	}
	text, ok := res.Content[0].(*mcp.TextContent)
	if !ok {
		t.Fatalf("content type = %T", res.Content[0])
	}
	return text.Text
}

func textContains(t *testing.T, res *mcp.CallToolResult, substr string) bool {
	t.Helper()
	return strings.Contains(textContent(t, res), substr)
}
