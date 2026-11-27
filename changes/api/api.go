// Package api is the HTTP interface of the changes service: it accepts change
// events, stores them and lists them by time window.
package api

import (
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"slices"
	"sync"
	"time"

	"github.com/oklog/ulid/v2"

	"github.com/dinhostork/IncidentLens/changes/schema"
)

const (
	// The largest valid event is about 270 KiB (64 attributes of 4 KiB).
	maxBody = 1 << 20
	// MaxList is the most events one GET returns. A window with more is
	// rejected rather than cut short.
	MaxList = 10000
)

// Options configures the handler.
type Options struct {
	// Token, when set, must be sent as "Authorization: Bearer <token>" on
	// every /v1 request. /healthz is always open.
	Token  string
	Logger *slog.Logger
}

type handler struct {
	store Store
	token string
	log   *slog.Logger
	now   func() time.Time
	newID func() string

	// Held between looking up a client-supplied ID and inserting it, so two
	// posts with the same ID in this process cannot both insert.
	mu sync.Mutex
}

// New returns the HTTP handler for the API.
func New(store Store, opts Options) http.Handler {
	return newHandler(store, opts).routes()
}

func newHandler(store Store, opts Options) *handler {
	h := &handler{
		store: store,
		token: opts.Token,
		log:   opts.Logger,
		now:   time.Now,
		newID: func() string { return "chg_" + ulid.Make().String() },
	}
	if h.log == nil {
		h.log = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	return h
}

func (h *handler) routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		io.WriteString(w, "ok\n") //nolint:errcheck // nothing to do if the client is gone
	})
	mux.Handle("POST /v1/changes", h.auth(h.create))
	mux.Handle("GET /v1/changes", h.auth(h.list))
	return mux
}

func (h *handler) auth(next http.HandlerFunc) http.Handler {
	if h.token == "" {
		return next
	}
	want := []byte("Bearer " + h.token)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if subtle.ConstantTimeCompare([]byte(r.Header.Get("Authorization")), want) != 1 {
			w.Header().Set("WWW-Authenticate", "Bearer")
			writeError(w, http.StatusUnauthorized, "missing or wrong token")
			return
		}
		next(w, r)
	})
}

func (h *handler) create(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxBody))
	if err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			writeError(w, http.StatusRequestEntityTooLarge, fmt.Sprintf("body is larger than %d bytes", maxBody))
			return
		}
		writeError(w, http.StatusBadRequest, "could not read body")
		return
	}
	if err := schema.Validate(body); err != nil {
		if problems := schema.Problems(err); problems != nil {
			writeProblems(w, problems)
			return
		}
		writeError(w, http.StatusBadRequest, "body is not valid JSON")
		return
	}
	ev, problems := parseEvent(body)
	if problems != nil {
		writeProblems(w, problems)
		return
	}
	ev.ReceivedAt = h.now().UTC().Truncate(time.Millisecond)

	if ev.ID == "" {
		ev.ID = h.newID()
		h.insert(w, r, ev)
		return
	}

	h.mu.Lock()
	defer h.mu.Unlock()
	stored, found, err := h.store.Get(r.Context(), ev.ID)
	if err != nil {
		h.storeError(w, "look up change", err)
		return
	}
	if !found {
		h.insert(w, r, ev)
		return
	}
	if !sameContent(stored, ev) {
		h.log.Warn("change rejected, id already stored with different content", "id", ev.ID)
		writeError(w, http.StatusConflict, fmt.Sprintf("an event with id %q is already stored with different content", ev.ID))
		return
	}
	writeJSON(w, http.StatusOK, stored)
}

func (h *handler) insert(w http.ResponseWriter, r *http.Request, ev Event) {
	if err := h.store.Insert(r.Context(), ev); err != nil {
		h.storeError(w, "insert change", err)
		return
	}
	h.log.Info("change stored", "id", ev.ID, "kind", ev.Kind, "environment", ev.Environment, "service", ev.Service, "time", ev.Time)
	writeJSON(w, http.StatusCreated, ev)
}

var listParams = []string{"start", "end", "environment", "service", "kind"}

func (h *handler) list(w http.ResponseWriter, r *http.Request) {
	params := r.URL.Query()
	for name, values := range params {
		if !slices.Contains(listParams, name) {
			writeError(w, http.StatusBadRequest, fmt.Sprintf("unknown parameter %q", name))
			return
		}
		if len(values) > 1 {
			writeError(w, http.StatusBadRequest, fmt.Sprintf("parameter %q given more than once", name))
			return
		}
	}
	start, err := windowBound(params.Get("start"), "start")
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	end, err := windowBound(params.Get("end"), "end")
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if !end.After(start) {
		writeError(w, http.StatusBadRequest, "end must be after start")
		return
	}
	kind := params.Get("kind")
	if kind != "" && !slices.Contains(Kinds, kind) {
		writeError(w, http.StatusBadRequest, fmt.Sprintf("kind must be one of %q", Kinds))
		return
	}

	events, err := h.store.List(r.Context(), Query{
		Start:       start,
		End:         end,
		Environment: params.Get("environment"),
		Service:     params.Get("service"),
		Kind:        kind,
		Limit:       MaxList + 1,
	})
	if err != nil {
		h.storeError(w, "list changes", err)
		return
	}
	if len(events) > MaxList {
		writeError(w, http.StatusUnprocessableEntity, fmt.Sprintf("more than %d events in the window, ask for a shorter one", MaxList))
		return
	}
	if events == nil {
		events = []Event{}
	}
	writeJSON(w, http.StatusOK, struct {
		Changes []Event `json:"changes"`
	}{events})
}

// windowBound parses a window bound and rounds it up to the millisecond,
// the precision times are stored at. Rounding up keeps start <= time < end
// exact for stored times.
func windowBound(v, name string) (time.Time, error) {
	if v == "" {
		return time.Time{}, fmt.Errorf("%s is required", name)
	}
	t, err := time.Parse(time.RFC3339Nano, v)
	if err != nil {
		return time.Time{}, fmt.Errorf("%s is not an RFC 3339 timestamp: %q", name, v)
	}
	t = t.UTC()
	if r := t.Truncate(time.Millisecond); !r.Equal(t) {
		t = r.Add(time.Millisecond)
	}
	return t, nil
}

func (h *handler) storeError(w http.ResponseWriter, op string, err error) {
	h.log.Error(op+" failed", "err", err)
	writeError(w, http.StatusServiceUnavailable, "storage is unavailable")
}

func writeProblems(w http.ResponseWriter, problems []schema.Problem) {
	writeJSON(w, http.StatusBadRequest, struct {
		Error    string           `json:"error"`
		Problems []schema.Problem `json:"problems"`
	}{"event does not match the schema", problems})
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, struct {
		Error string `json:"error"`
	}{msg})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v) //nolint:errcheck // nothing to do if the client is gone
}
