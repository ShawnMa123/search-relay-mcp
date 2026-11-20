package providers

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/ShawnMa123/search-relay-mcp/internal/search"
)

// maxResponseBytes caps the decoded upstream body. Anything larger is a
// failed attempt, not a partial result.
const maxResponseBytes = 1 << 20

var errBodyTooLarge = errors.New("upstream response exceeds size limit")

// NewHTTPClient returns the shared client used by every adapter: no
// redirects, no proxy environment inheritance and no connection reuse (which
// avoids Go's transparent GET retry on stale connections).
func NewHTTPClient() *http.Client {
	return &http.Client{
		Transport: &http.Transport{
			Proxy:               nil,
			DialContext:         (&net.Dialer{Timeout: 5 * time.Second}).DialContext,
			TLSHandshakeTimeout: 5 * time.Second,
			// Pin ALPN to HTTP/1.1 and leave TLSNextProto as a non-nil empty
			// map so the transport cannot enable HTTP/2 implicitly. HTTP/2
			// can transparently retry refused streams, which would break the
			// one-request-per-provider contract.
			TLSClientConfig: &tls.Config{
				MinVersion: tls.VersionTLS12,
				NextProtos: []string{"http/1.1"},
			},
			TLSNextProto:      map[string]func(string, *tls.Conn) http.RoundTripper{},
			DisableKeepAlives: true,
			ForceAttemptHTTP2: false,
		},
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
}

type httpResult struct {
	Status int
	Header http.Header
	Body   []byte
}

// fetch performs one request with a bounded read. Non-2xx statuses are
// returned in the response so adapters can inspect documented error markers;
// callers must classify the status before decoding.
func fetch(client *http.Client, req *http.Request) (*httpResult, error) {
	resp, err := client.Do(req)
	if err != nil {
		return nil, classifyTransportError(err)
	}
	defer resp.Body.Close()
	body, err := readBounded(resp.Body, maxResponseBytes)
	if err != nil {
		// Headers are already trustworthy: an unreadable error body must
		// still drive rate-limit/quota cool-downs. Incomplete 2xx bodies are
		// never treated as parseable success.
		if resp.StatusCode < 200 || resp.StatusCode > 299 {
			return nil, statusError(resp.StatusCode, resp.Header)
		}
		return nil, &search.ProviderError{Kind: search.KindInvalidResponse, Status: resp.StatusCode}
	}
	return &httpResult{Status: resp.StatusCode, Header: resp.Header, Body: body}, nil
}

// doJSON runs a request that must answer 2xx with one strict JSON document.
func doJSON(client *http.Client, req *http.Request, out any) error {
	res, err := fetch(client, req)
	if err != nil {
		return err
	}
	if res.Status < 200 || res.Status > 299 {
		return statusError(res.Status, res.Header)
	}
	if err := decodeStrict(res.Body, out); err != nil {
		return &search.ProviderError{Kind: search.KindInvalidResponse, Status: res.Status}
	}
	return nil
}

func readBounded(r io.Reader, limit int64) ([]byte, error) {
	var buf bytes.Buffer
	n, err := io.Copy(&buf, io.LimitReader(r, limit+1))
	if err != nil {
		return nil, err
	}
	if n > limit {
		return nil, errBodyTooLarge
	}
	return buf.Bytes(), nil
}

func decodeStrict(body []byte, out any) error {
	dec := json.NewDecoder(bytes.NewReader(body))
	if err := dec.Decode(out); err != nil {
		return err
	}
	var tail json.RawMessage
	if err := dec.Decode(&tail); err != io.EOF {
		return errors.New("trailing data after JSON document")
	}
	return nil
}

func classifyTransportError(err error) *search.ProviderError {
	if errors.Is(err, context.DeadlineExceeded) {
		return &search.ProviderError{Kind: search.KindTimeout}
	}
	return &search.ProviderError{Kind: search.KindNetwork}
}

// statusError maps an HTTP status to a safe classification. The body is
// deliberately not included: it is untrusted upstream data.
func statusError(status int, header http.Header) *search.ProviderError {
	e := &search.ProviderError{Kind: classifyStatus(status), Status: status}
	if e.Kind == search.KindRateLimited || e.Kind == search.KindQuotaExhausted {
		if t, ok := parseRetryAfter(header.Get("Retry-After"), time.Now()); ok {
			e.RetryAt = t
		}
	}
	return e
}

func classifyStatus(status int) search.Kind {
	switch {
	case status == http.StatusUnauthorized || status == http.StatusForbidden:
		return search.KindAuthFailed
	case status == http.StatusPaymentRequired:
		return search.KindQuotaExhausted
	case status == http.StatusTooManyRequests:
		return search.KindRateLimited
	default:
		return search.KindUpstreamError
	}
}

// parseRetryAfter accepts delay-seconds and HTTP-date forms.
func parseRetryAfter(value string, now time.Time) (time.Time, bool) {
	value = strings.TrimSpace(value)
	if value == "" {
		return time.Time{}, false
	}
	if secs, err := strconv.Atoi(value); err == nil {
		if secs < 0 {
			secs = 0
		}
		return now.Add(time.Duration(secs) * time.Second), true
	}
	if t, err := http.ParseTime(value); err == nil {
		return t, true
	}
	return time.Time{}, false
}
