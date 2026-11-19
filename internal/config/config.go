package config

import (
	"errors"
	"fmt"
	"net"
	"strconv"
	"strings"
	"unicode"
)

const (
	defaultListenAddr    = "127.0.0.1:18081"
	defaultProviderOrder = "keenable,tinyfish,anysearch"
)

// providerKeyEnv maps provider names to the environment variable holding
// their API key.
var providerKeyEnv = map[string]string{
	"keenable":  "KEENABLE_API_KEY",
	"tinyfish":  "TINYFISH_API_KEY",
	"anysearch": "ANYSEARCH_API_KEY",
}

// Config is the validated process configuration. APIKeys is indexed by
// provider name and only contains enabled providers.
type Config struct {
	ListenAddr  string
	BearerToken string
	Providers   []string
	APIKeys     map[string]string
}

// Load reads and validates configuration through lookup. Errors name the
// offending variable but never echo its value.
func Load(lookup func(string) (string, bool)) (Config, error) {
	cfg := Config{APIKeys: make(map[string]string)}

	if v, ok := lookup("LISTEN_ADDR"); ok {
		if strings.TrimSpace(v) == "" {
			return Config{}, errors.New("LISTEN_ADDR must not be empty")
		}
		cfg.ListenAddr = v
	} else {
		cfg.ListenAddr = defaultListenAddr
	}
	if err := validateListenAddr(cfg.ListenAddr); err != nil {
		return Config{}, fmt.Errorf("LISTEN_ADDR is invalid: %w", err)
	}

	token, ok := lookup("MCP_BEARER_TOKEN")
	if !ok || token == "" {
		return Config{}, errors.New("MCP_BEARER_TOKEN is required")
	}
	if err := validateToken(token); err != nil {
		return Config{}, err
	}
	cfg.BearerToken = token

	var names []string
	if raw, ok := lookup("SEARCH_PROVIDERS"); ok {
		if raw == "" {
			return Config{}, errors.New("SEARCH_PROVIDERS must not be empty")
		}
		names = strings.Split(raw, ",")
	} else {
		names = strings.Split(defaultProviderOrder, ",")
	}

	seen := make(map[string]bool, len(names))
	for _, name := range names {
		keyEnv, known := providerKeyEnv[name]
		if !known {
			return Config{}, fmt.Errorf("SEARCH_PROVIDERS contains an unknown provider (%d characters)", len(name))
		}
		if seen[name] {
			return Config{}, fmt.Errorf("SEARCH_PROVIDERS contains duplicate provider %q", name)
		}
		seen[name] = true

		key, ok := lookup(keyEnv)
		if !ok || strings.TrimSpace(key) == "" {
			return Config{}, fmt.Errorf("%s is required for provider %q", keyEnv, name)
		}
		if err := validateKey(key); err != nil {
			return Config{}, fmt.Errorf("%s is invalid", keyEnv)
		}
		cfg.APIKeys[name] = key
	}
	cfg.Providers = names
	return cfg, nil
}

func validateListenAddr(addr string) error {
	_, port, err := net.SplitHostPort(addr)
	if err != nil {
		return errors.New("want host:port")
	}
	n, err := strconv.Atoi(port)
	if err != nil || n < 1 || n > 65535 {
		return errors.New("port must be between 1 and 65535")
	}
	return nil
}

func validateToken(token string) error {
	if len(token) < 32 || len(token) > 256 {
		return errors.New("MCP_BEARER_TOKEN must be 32-256 characters")
	}
	for i := 0; i < len(token); i++ {
		c := token[i]
		if (c >= 'A' && c <= 'Z') || (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') || c == '_' || c == '-' {
			continue
		}
		return errors.New("MCP_BEARER_TOKEN must use only [A-Za-z0-9_-]")
	}
	return nil
}

func validateKey(key string) error {
	for _, r := range key {
		if unicode.IsSpace(r) || unicode.IsControl(r) {
			return errors.New("key contains whitespace or control characters")
		}
	}
	return nil
}
