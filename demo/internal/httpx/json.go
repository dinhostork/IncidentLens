package httpx

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
)

// WriteJSON writes v as a JSON response with the given status.
func WriteJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// ReadJSON decodes the request body into v, rejecting unknown fields.
func ReadJSON(r *http.Request, v any) error {
	dec := json.NewDecoder(http.MaxBytesReader(nil, r.Body, 1<<20))
	dec.DisallowUnknownFields()
	return dec.Decode(v)
}

// Fail writes an error response. 5xx errors are logged with the request
// context so the log record carries the trace and span IDs.
func Fail(w http.ResponseWriter, r *http.Request, status int, err error) {
	if status >= 500 {
		slog.ErrorContext(r.Context(), "request failed",
			"http.route", r.Pattern, "http.response.status_code", status, "error", err.Error())
	}
	WriteJSON(w, status, map[string]string{"error": err.Error()})
}

// Upstream maps an error from a call to another service to a response status:
// the callee's 4xx is passed through, anything else becomes 502.
func Upstream(err error) int {
	var se *StatusError
	if errors.As(err, &se) && se.Code >= 400 && se.Code < 500 {
		return se.Code
	}
	return http.StatusBadGateway
}
