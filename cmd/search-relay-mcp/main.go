package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/ShawnMa123/search-relay-mcp/internal/config"
	"github.com/ShawnMa123/search-relay-mcp/internal/mcpserver"
	"github.com/ShawnMa123/search-relay-mcp/internal/providers"
	"github.com/ShawnMa123/search-relay-mcp/internal/search"
)

const defaultHealthcheckURL = "http://127.0.0.1:18081/healthz"

func main() {
	if len(os.Args) > 1 && os.Args[1] == "healthcheck" {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := runHealthcheck(ctx, defaultHealthcheckURL); err != nil {
			fmt.Fprintln(os.Stderr, "healthcheck failed:", err)
			os.Exit(1)
		}
		return
	}
	if err := runServer(os.LookupEnv); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func runServer(lookup func(string) (string, bool)) error {
	cfg, err := config.Load(lookup)
	if err != nil {
		return err
	}

	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))
	client := providers.NewHTTPClient()
	router := search.NewRouter(buildProviders(cfg, client), search.RouterOptions{Logger: logger})
	handler := mcpserver.NewHTTPHandler(mcpserver.NewServer(router), cfg.BearerToken)

	srv := &http.Server{
		Addr:              cfg.ListenAddr,
		Handler:           handler,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      20 * time.Second,
		IdleTimeout:       60 * time.Second,
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	serveErr := make(chan error, 1)
	go func() {
		serveErr <- srv.ListenAndServe()
	}()
	logger.Info("listening", "addr", cfg.ListenAddr)

	select {
	case err := <-serveErr:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return errors.New("server stopped unexpectedly")
	case <-ctx.Done():
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		return errors.New("graceful shutdown timed out")
	}
	return nil
}

// buildProviders preserves configured order. Configuration validation has
// already rejected unknown names, so this only wires known providers.
func buildProviders(cfg config.Config, client *http.Client) []search.Provider {
	out := make([]search.Provider, 0, len(cfg.Providers))
	for _, name := range cfg.Providers {
		switch name {
		case "keenable":
			out = append(out, providers.NewKeenable(client, cfg.APIKeys[name]))
		case "tinyfish":
			out = append(out, providers.NewTinyFish(client, cfg.APIKeys[name]))
		case "anysearch":
			out = append(out, providers.NewAnySearch(client, cfg.APIKeys[name]))
		}
	}
	return out
}

// runHealthcheck probes only the local health endpoint. It never reads
// provider configuration or performs a search.
func runHealthcheck(ctx context.Context, endpoint string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return errors.New("invalid healthcheck endpoint")
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return errors.New("healthcheck request failed")
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1024))
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("healthcheck status %d", resp.StatusCode)
	}
	return nil
}
