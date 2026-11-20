package config

import (
	"strings"
	"testing"
)

func lookupFrom(values map[string]string) func(string) (string, bool) {
	return func(key string) (string, bool) {
		v, ok := values[key]
		return v, ok
	}
}

func validEnv() map[string]string {
	return map[string]string{
		"MCP_BEARER_TOKEN":  strings.Repeat("t", 32),
		"KEENABLE_API_KEY":  "keenable-key",
		"TINYFISH_API_KEY":  "tinyfish-key",
		"ANYSEARCH_API_KEY": "anysearch-key",
	}
}

func TestLoadDefaults(t *testing.T) {
	cfg, err := Load(lookupFrom(validEnv()))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.ListenAddr != "127.0.0.1:18081" {
		t.Fatalf("listen addr = %q", cfg.ListenAddr)
	}
	want := []string{"keenable", "tinyfish", "anysearch"}
	if len(cfg.Providers) != len(want) {
		t.Fatalf("providers = %v", cfg.Providers)
	}
	for i := range want {
		if cfg.Providers[i] != want[i] {
			t.Fatalf("providers = %v", cfg.Providers)
		}
	}
	if len(cfg.APIKeys) != 3 || cfg.APIKeys["tinyfish"] != "tinyfish-key" {
		t.Fatalf("api keys = %v", cfg.APIKeys)
	}
}

func TestLoadSubset(t *testing.T) {
	env := map[string]string{
		"MCP_BEARER_TOKEN":  strings.Repeat("t", 32),
		"SEARCH_PROVIDERS":  "anysearch,tinyfish",
		"TINYFISH_API_KEY":  "tinyfish-key",
		"ANYSEARCH_API_KEY": "anysearch-key",
	}
	cfg, err := Load(lookupFrom(env))
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Providers) != 2 || cfg.Providers[0] != "anysearch" || cfg.Providers[1] != "tinyfish" {
		t.Fatalf("providers = %v", cfg.Providers)
	}
	if len(cfg.APIKeys) != 2 {
		t.Fatalf("api keys = %v", cfg.APIKeys)
	}
	if _, ok := cfg.APIKeys["keenable"]; ok {
		t.Fatal("disabled provider key registered")
	}
}

func TestLoadCustomAddress(t *testing.T) {
	env := validEnv()
	env["LISTEN_ADDR"] = "0.0.0.0:18081"
	cfg, err := Load(lookupFrom(env))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.ListenAddr != "0.0.0.0:18081" {
		t.Fatalf("listen addr = %q", cfg.ListenAddr)
	}
}

func TestLoadRejectsInvalidOptions(t *testing.T) {
	const badToken = "bad token with spaces"
	const badKey = " key-with-space "

	cases := []struct {
		name   string
		mutate func(map[string]string)
	}{
		{name: "missing token", mutate: func(m map[string]string) { delete(m, "MCP_BEARER_TOKEN") }},
		{name: "short token", mutate: func(m map[string]string) { m["MCP_BEARER_TOKEN"] = "short" }},
		{name: "long token", mutate: func(m map[string]string) { m["MCP_BEARER_TOKEN"] = strings.Repeat("a", 257) }},
		{name: "token bad char", mutate: func(m map[string]string) { m["MCP_BEARER_TOKEN"] = badToken }},
		{name: "empty providers", mutate: func(m map[string]string) { m["SEARCH_PROVIDERS"] = "" }},
		{name: "duplicate provider", mutate: func(m map[string]string) { m["SEARCH_PROVIDERS"] = "keenable,keenable" }},
		{name: "unknown provider", mutate: func(m map[string]string) { m["SEARCH_PROVIDERS"] = "keenable,exa" }},
		{name: "provider trailing space", mutate: func(m map[string]string) { m["SEARCH_PROVIDERS"] = "keenable, tinyfish" }},
		{name: "missing enabled key", mutate: func(m map[string]string) { delete(m, "TINYFISH_API_KEY") }},
		{name: "whitespace-only key", mutate: func(m map[string]string) { m["TINYFISH_API_KEY"] = "   " }},
		{name: "key with whitespace", mutate: func(m map[string]string) { m["TINYFISH_API_KEY"] = badKey }},
		{name: "key with control char", mutate: func(m map[string]string) { m["TINYFISH_API_KEY"] = "key\x00value" }},
		{name: "bad address", mutate: func(m map[string]string) { m["LISTEN_ADDR"] = "not-an-address" }},
		{name: "bad port", mutate: func(m map[string]string) { m["LISTEN_ADDR"] = "127.0.0.1:99999" }},
		{name: "empty port", mutate: func(m map[string]string) { m["LISTEN_ADDR"] = "127.0.0.1:" }},
		{name: "empty address", mutate: func(m map[string]string) { m["LISTEN_ADDR"] = "" }},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			env := validEnv()
			env["MCP_BEARER_TOKEN"] = "valid-token-0123456789abcdefghij"
			tc.mutate(env)
			_, err := Load(lookupFrom(env))
			if err == nil {
				t.Fatal("expected error")
			}
			msg := err.Error()
			if strings.Contains(msg, badToken) || strings.Contains(msg, badKey) {
				t.Fatalf("error leaked a value: %s", msg)
			}
		})
	}
}

func TestLoadDoesNotRequireDisabledKeys(t *testing.T) {
	env := map[string]string{
		"MCP_BEARER_TOKEN": strings.Repeat("t", 32),
		"SEARCH_PROVIDERS": "tinyfish",
		"TINYFISH_API_KEY": "key",
	}
	if _, err := Load(lookupFrom(env)); err != nil {
		t.Fatal(err)
	}
}
