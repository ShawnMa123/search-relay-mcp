package mcpserver

import (
	"crypto/sha256"
	"crypto/subtle"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const maxRequestBodyBytes = 16 << 10

// NewHTTPHandler mounts the Streamable HTTP transport at /mcp and a
// dependency-free health probe at /healthz. Every /mcp request must present
// the bearer token, a loopback Host and no Origin header.
func NewHTTPHandler(server *mcp.Server, bearerToken string) http.Handler {
	sdk := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server {
		return server
	}, &mcp.StreamableHTTPOptions{
		Stateless:                    true,
		JSONResponse:                 true,
		MaxRequestBodyBytes:          maxRequestBodyBytes,
		PropagateRequestCancellation: true,
	})

	mux := http.NewServeMux()
	mux.Handle("/mcp", accessGuard(bearerToken, sdk))
	mux.HandleFunc("/healthz", healthz)
	return mux
}

func accessGuard(bearerToken string, next http.Handler) http.Handler {
	want := sha256.Sum256([]byte(bearerToken))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		values := r.Header.Values("Authorization")
		if len(values) != 1 {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		presented, ok := strings.CutPrefix(values[0], "Bearer ")
		if !ok {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		got := sha256.Sum256([]byte(presented))
		if subtle.ConstantTimeCompare(got[:], want[:]) != 1 {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		if !allowedHost(r.Host) {
			http.Error(w, "forbidden host", http.StatusForbidden)
			return
		}
		if len(r.Header.Values("Origin")) > 0 {
			http.Error(w, "origin not allowed", http.StatusForbidden)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func healthz(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	_, _ = io.WriteString(w, "ok\n")
}

// allowedHost permits only loopback names, with an optional valid port.
// Forwarded host headers are never consulted.
func allowedHost(hostport string) bool {
	if hostport == "" {
		return false
	}
	var host string
	if h, p, err := net.SplitHostPort(hostport); err == nil {
		host = h
		if n, err := strconv.Atoi(p); err != nil || n < 1 || n > 65535 {
			return false
		}
	} else if strings.HasPrefix(hostport, "[") && strings.HasSuffix(hostport, "]") {
		host = strings.TrimSuffix(strings.TrimPrefix(hostport, "["), "]")
	} else if strings.Contains(hostport, ":") {
		return false
	} else {
		host = hostport
	}
	switch host {
	case "localhost", "127.0.0.1", "::1":
		return true
	default:
		return false
	}
}
