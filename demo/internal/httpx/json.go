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

// ReadJSON decodes the request body into v, rejecting unknown fields and
// bodies over 1 MiB.
func ReadJSON(w http.ResponseWriter, r *http.Request, v any) error {
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20))
	dec.DisallowUnknownFields()
	return dec.Decode(v)
}

// Fail writes a client error (4xx) with err's message as the response body.
// Passing a 5xx status is treated as ServerError with a generic message.
func Fail(w http.ResponseWriter, r *http.Request, status int, err error) {
	if status >= 500 {
		ServerError(w, r, status, "request failed", err)
		return
	}
	WriteJSON(w, status, map[string]string{"error": err.Error()})
}

// ServerError logs a failed request and answers with a generic body, so
// internal details stay in telemetry and out of API responses.
//
// msg becomes the log record body and should name the failure without
// variable parts ("call to payments failed", "database query failed"), so
// that log templates tell failures apart. The error goes into an attribute.
// The record is emitted with the request context and carries its trace and
// span IDs.
func ServerError(w http.ResponseWriter, r *http.Request, status int, msg string, err error) {
	slog.ErrorContext(r.Context(), msg,
		"http.route", r.Pattern, "http.response.status_code", status, "error", err.Error())
	WriteJSON(w, status, map[string]string{"error": http.StatusText(status)})
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
