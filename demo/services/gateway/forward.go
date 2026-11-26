package main

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/dinhostork/IncidentLens/demo/internal/httpx"
)

// forward returns a handler that sends the request body to path on c and
// writes the JSON response back with status. "{id}" in path is replaced with
// the request's id path value.
func forward(c *httpx.Client, method, path string, status int) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		target := strings.ReplaceAll(path, "{id}", url.PathEscape(r.PathValue("id")))

		var in any
		if method != http.MethodGet {
			body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 1<<20))
			if err != nil {
				httpx.Fail(w, r, http.StatusBadRequest, err)
				return
			}
			if len(body) > 0 {
				if !json.Valid(body) {
					httpx.Fail(w, r, http.StatusBadRequest, errors.New("body is not valid JSON"))
					return
				}
				in = json.RawMessage(body)
			}
		}

		var out json.RawMessage
		if err := c.Do(r.Context(), method, target, in, &out); err != nil {
			var se *httpx.StatusError
			if errors.As(err, &se) && se.Code < 500 {
				// Pass the callee's client error through unchanged.
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(se.Code)
				_, _ = io.WriteString(w, se.Body)
				return
			}
			httpx.ServerError(w, r, httpx.Upstream(err), "call to "+c.Dependency()+" failed", err)
			return
		}
		httpx.WriteJSON(w, status, out)
	}
}
