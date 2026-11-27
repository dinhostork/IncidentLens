package httpx

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/dinhostork/IncidentLens/demo/internal/fault"
)

// failOn fails calls to one dependency, or inbound requests if dep is empty,
// and records what it saw.
type failOn struct {
	fault.Nop
	kind fault.Kind
	dep  string
	seen []fault.Point
}

func (f *failOn) Before(_ context.Context, p fault.Point) error {
	f.seen = append(f.seen, p)
	if p.Kind == f.kind && p.Dependency == f.dep {
		return errors.New("connection refused")
	}
	return nil
}

func TestClientRunsHook(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		WriteJSON(w, http.StatusOK, map[string]int{"n": 1})
	}))
	defer srv.Close()

	hook := &failOn{kind: fault.Outbound, dep: "payments"}
	ok := NewClient("catalog", srv.URL, time.Second, hook)
	broken := NewClient("payments", srv.URL, time.Second, hook)

	var out struct{ N int }
	if err := ok.Do(context.Background(), http.MethodGet, "/x", nil, &out); err != nil || out.N != 1 {
		t.Fatalf("catalog call: out=%v err=%v", out, err)
	}
	if err := broken.Do(context.Background(), http.MethodPost, "/payments", map[string]int{"a": 1}, nil); err == nil {
		t.Fatal("payments call: want error from hook")
	}
	if n := calls.Load(); n != 1 {
		t.Fatalf("server got %d calls, want 1", n)
	}
	want := fault.Point{Kind: fault.Outbound, Dependency: "payments", Operation: "POST", Address: strings.TrimPrefix(srv.URL, "http://")}
	if got := hook.seen[len(hook.seen)-1]; got != want {
		t.Fatalf("hook saw %+v, want %+v", got, want)
	}
}

func TestClientStatusError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		WriteJSON(w, http.StatusNotFound, map[string]string{"error": "nope"})
	}))
	defer srv.Close()

	err := NewClient("catalog", srv.URL, time.Second, fault.Nop{}).Do(context.Background(), http.MethodGet, "/x", nil, nil)
	var se *StatusError
	if !errors.As(err, &se) || se.Code != http.StatusNotFound {
		t.Fatalf("err = %v, want StatusError 404", err)
	}
	if got := Upstream(err); got != http.StatusNotFound {
		t.Fatalf("Upstream = %d, want 404", got)
	}
	if got := Upstream(errors.New("timeout")); got != http.StatusBadGateway {
		t.Fatalf("Upstream = %d, want 502", got)
	}
}

func TestMuxRunsHook(t *testing.T) {
	hook := &failOn{kind: fault.Inbound}
	m := NewMux(hook)
	m.HandleFunc("GET /things/{id}", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	h := m.Handler()

	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/things/1", nil))
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", w.Code)
	}
	if got := hook.seen[0].Operation; got != "GET /things/{id}" {
		t.Fatalf("operation = %q, want route pattern", got)
	}

	w = httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	if w.Code != http.StatusNoContent || len(hook.seen) != 1 {
		t.Fatalf("healthz: status %d, hook calls %d", w.Code, len(hook.seen))
	}
}

func TestStatusErrorKeepsFullBody(t *testing.T) {
	long := `{"error":"` + strings.Repeat("x", 2000) + `"}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusConflict)
		_, _ = w.Write([]byte(long))
	}))
	defer srv.Close()

	err := NewClient("orders", srv.URL, time.Second, fault.Nop{}).Do(context.Background(), http.MethodGet, "/", nil, nil)
	var se *StatusError
	if !errors.As(err, &se) || se.Body != long {
		t.Fatalf("body not kept in full: %v", err)
	}
	if n := len(se.Error()); n > 300 {
		t.Fatalf("error message is %d bytes, want it truncated", n)
	}
}

func TestServerErrorHidesDetails(t *testing.T) {
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	Fail(w, r, http.StatusBadGateway, errors.New("dial tcp 10.0.0.7:8080: connection refused"))
	if w.Code != http.StatusBadGateway || strings.Contains(w.Body.String(), "10.0.0.7") {
		t.Fatalf("got %d %s", w.Code, w.Body)
	}

	w = httptest.NewRecorder()
	Fail(w, r, http.StatusNotFound, errors.New("cart not found"))
	if !strings.Contains(w.Body.String(), "cart not found") {
		t.Fatalf("4xx message lost: %s", w.Body)
	}
}
