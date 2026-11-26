package httpx

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"

	"github.com/dinhostork/IncidentLens/demo/internal/fault"
)

// Client calls one other service over HTTP. Each request gets a client span,
// and the fault hook runs inside that span, so injected latency and errors
// show up on the caller's side of the edge.
type Client struct {
	base string
	http *http.Client
}

// NewClient returns a Client for dependency, the service name of the callee
// ("catalog"), reachable at baseURL.
func NewClient(dependency, baseURL string, timeout time.Duration, hook fault.Hook) *Client {
	var rt http.RoundTripper = &faultTransport{dep: dependency, hook: hook, next: http.DefaultTransport}
	rt = otelhttp.NewTransport(rt,
		otelhttp.WithSpanNameFormatter(func(_ string, r *http.Request) string { return r.Method }),
	)
	return &Client{
		base: strings.TrimRight(baseURL, "/"),
		http: &http.Client{Transport: rt, Timeout: timeout},
	}
}

// StatusError is returned when the callee answers with a non-2xx status.
type StatusError struct {
	Code int
	Body string
}

func (e *StatusError) Error() string {
	return fmt.Sprintf("unexpected status %d: %s", e.Code, e.Body)
}

// Do sends a request with an optional JSON body and decodes a JSON response
// into out, if out is not nil.
func (c *Client) Do(ctx context.Context, method, path string, in, out any) error {
	var body io.Reader
	if in != nil {
		b, err := json.Marshal(in)
		if err != nil {
			return err
		}
		body = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.base+path, body)
	if err != nil {
		return err
	}
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return &StatusError{Code: resp.StatusCode, Body: strings.TrimSpace(string(b))}
	}
	if out == nil {
		_, _ = io.Copy(io.Discard, resp.Body)
		return nil
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

type faultTransport struct {
	dep  string
	hook fault.Hook
	next http.RoundTripper
}

func (t *faultTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	p := fault.Point{Kind: fault.Outbound, Dependency: t.dep, Operation: r.Method + " " + r.URL.Path}
	if err := t.hook.Before(r.Context(), p); err != nil {
		if r.Body != nil {
			_ = r.Body.Close()
		}
		return nil, err
	}
	return t.next.RoundTrip(r)
}
