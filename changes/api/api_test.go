package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/dinhostork/IncidentLens/changes/schema"
)

// memStore keeps events in memory and applies the same window and filter
// rules as the ClickHouse store.
type memStore struct {
	mu      sync.Mutex
	events  []Event
	err     error
	inserts int
	queries []Query
	// getDelay is how long Get takes to answer after reading, so concurrent
	// posts can overlap between the lookup and the insert.
	getDelay time.Duration
}

func (s *memStore) Get(_ context.Context, id string) (Event, bool, error) {
	ev, found, err := s.get(id)
	time.Sleep(s.getDelay)
	return ev, found, err
}

func (s *memStore) get(id string) (Event, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.err != nil {
		return Event{}, false, s.err
	}
	for _, ev := range s.events {
		if ev.ID == id {
			return ev, true, nil
		}
	}
	return Event{}, false, nil
}

func (s *memStore) Insert(_ context.Context, ev Event) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.err != nil {
		return s.err
	}
	s.inserts++
	s.events = append(s.events, ev)
	return nil
}

func (s *memStore) List(_ context.Context, q Query) ([]Event, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.queries = append(s.queries, q)
	if s.err != nil {
		return nil, s.err
	}
	var out []Event
	for _, ev := range s.events {
		if ev.Time.Before(q.Start) || !ev.Time.Before(q.End) ||
			(q.Environment != "" && ev.Environment != q.Environment) ||
			(q.Service != "" && ev.Service != q.Service) ||
			(q.Kind != "" && ev.Kind != q.Kind) {
			continue
		}
		out = append(out, ev)
	}
	slices.SortFunc(out, func(a, b Event) int {
		if c := a.Time.Compare(b.Time); c != 0 {
			return c
		}
		return strings.Compare(a.ID, b.ID)
	})
	if len(out) > q.Limit {
		out = out[:q.Limit]
	}
	return out, nil
}

var received = time.Date(2026, 12, 1, 14, 5, 0, 123_456_789, time.UTC)

// server runs the API over store with a fixed clock and IDs chg_1, chg_2...
func server(t *testing.T, store Store, token string) *httptest.Server {
	t.Helper()
	h := newHandler(store, Options{Token: token})
	h.now = func() time.Time { return received }
	var mu sync.Mutex
	n := 0
	h.newID = func() string {
		mu.Lock()
		defer mu.Unlock()
		n++
		return fmt.Sprintf("chg_%d", n)
	}
	srv := httptest.NewServer(h.routes())
	t.Cleanup(srv.Close)
	return srv
}

type response struct {
	status int
	body   []byte
}

func (r response) event(t *testing.T) Event {
	t.Helper()
	var ev Event
	if err := json.Unmarshal(r.body, &ev); err != nil {
		t.Fatalf("decode event: %v: %s", err, r.body)
	}
	return ev
}

func do(t *testing.T, method, url, token string, body []byte) response {
	t.Helper()
	req, err := http.NewRequest(method, url, bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = res.Body.Close() }()
	b, err := io.ReadAll(res.Body)
	if err != nil {
		t.Fatal(err)
	}
	return response{res.StatusCode, b}
}

func post(t *testing.T, srv *httptest.Server, body []byte) response {
	t.Helper()
	return do(t, http.MethodPost, srv.URL+"/v1/changes", "", body)
}

// example returns one of the schema's valid examples, changed by edit.
func example(t *testing.T, name string, edit func(ev map[string]any)) []byte {
	t.Helper()
	data, err := os.ReadFile("../schema/testdata/valid/" + name + ".json")
	if err != nil {
		t.Fatal(err)
	}
	var ev map[string]any
	if err := json.Unmarshal(data, &ev); err != nil {
		t.Fatal(err)
	}
	if edit != nil {
		edit(ev)
	}
	out, err := json.Marshal(ev)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func withID(id string) func(map[string]any) {
	return func(ev map[string]any) { ev["id"] = id }
}

func withoutID(ev map[string]any) { delete(ev, "id") }

func TestCreateAssignsID(t *testing.T) {
	store := &memStore{}
	srv := server(t, store, "")

	res := post(t, srv, example(t, "deploy", withoutID))
	if res.status != http.StatusCreated {
		t.Fatalf("status %d: %s", res.status, res.body)
	}
	got := res.event(t)
	want := Event{
		ID:          "chg_1",
		Time:        time.Date(2026, 12, 1, 14, 3, 12, 0, time.UTC),
		ReceivedAt:  received.Truncate(time.Millisecond),
		Kind:        "deploy",
		Environment: "demo",
		Service:     "payments",
		Attributes: map[string]string{
			"version.from":          "1.0.0",
			"version.to":            "1.0.1",
			"vcs.ref.head.revision": "4f2c9e1",
		},
		Source: "github-actions",
	}
	if !sameContent(got, want) || !got.ReceivedAt.Equal(want.ReceivedAt) {
		t.Errorf("got %+v\nwant %+v", got, want)
	}
	if len(store.events) != 1 || !sameContent(store.events[0], want) {
		t.Errorf("stored %+v", store.events)
	}

	// Without an ID every post is a new event.
	if res := post(t, srv, example(t, "deploy", withoutID)); res.status != http.StatusCreated || res.event(t).ID != "chg_2" {
		t.Errorf("second post: %d %s", res.status, res.body)
	}
}

func TestCreateKeepsClientID(t *testing.T) {
	srv := server(t, &memStore{}, "")
	res := post(t, srv, example(t, "flag", withID("flagd:payments:42")))
	if res.status != http.StatusCreated || res.event(t).ID != "flagd:payments:42" {
		t.Errorf("%d %s", res.status, res.body)
	}
}

func TestCreateStoresMilliseconds(t *testing.T) {
	store := &memStore{}
	srv := server(t, store, "")
	res := post(t, srv, example(t, "note", func(ev map[string]any) {
		ev["time"] = "2026-12-01T11:03:12.123987-03:00"
	}))
	if res.status != http.StatusCreated {
		t.Fatalf("status %d: %s", res.status, res.body)
	}
	want := time.Date(2026, 12, 1, 14, 3, 12, 123_000_000, time.UTC)
	if got := res.event(t).Time; !got.Equal(want) {
		t.Errorf("time %v, want %v", got, want)
	}
	if !store.events[0].Time.Equal(want) {
		t.Errorf("stored time %v, want %v", store.events[0].Time, want)
	}
}

func TestRetry(t *testing.T) {
	tests := []struct {
		name   string
		first  []byte
		second []byte
		status int
	}{
		{
			"identical body",
			example(t, "deploy", nil),
			example(t, "deploy", nil),
			http.StatusOK,
		},
		{
			"same instant in another offset and below a millisecond",
			example(t, "deploy", func(ev map[string]any) { ev["time"] = "2026-12-01T14:03:12.000Z" }),
			example(t, "deploy", func(ev map[string]any) { ev["time"] = "2026-12-01T11:03:12.0004-03:00" }),
			http.StatusOK,
		},
		{
			"empty attributes and no attributes",
			example(t, "config", func(ev map[string]any) { ev["id"] = "c-1"; ev["attributes"] = map[string]any{} }),
			example(t, "config", withID("c-1")),
			http.StatusOK,
		},
		{
			"different version",
			example(t, "deploy", nil),
			example(t, "deploy", func(ev map[string]any) { ev["attributes"].(map[string]any)["version.to"] = "1.0.2" }),
			http.StatusConflict,
		},
		{
			"different time",
			example(t, "deploy", nil),
			example(t, "deploy", func(ev map[string]any) { ev["time"] = "2026-12-01T14:03:13Z" }),
			http.StatusConflict,
		},
		{
			"same id on another kind",
			example(t, "deploy", withID("x-1")),
			example(t, "note", withID("x-1")),
			http.StatusConflict,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := &memStore{}
			srv := server(t, store, "")
			first := post(t, srv, tt.first)
			if first.status != http.StatusCreated {
				t.Fatalf("first post: %d %s", first.status, first.body)
			}
			second := post(t, srv, tt.second)
			if second.status != tt.status {
				t.Fatalf("second post: status %d, want %d: %s", second.status, tt.status, second.body)
			}
			if store.inserts != 1 {
				t.Errorf("%d inserts, want 1", store.inserts)
			}
			if tt.status == http.StatusOK && !bytes.Equal(first.body, second.body) {
				t.Errorf("retry returned\n%s\nwant the stored event\n%s", second.body, first.body)
			}
		})
	}
}

func TestConcurrentRetries(t *testing.T) {
	store := &memStore{getDelay: 10 * time.Millisecond}
	srv := server(t, store, "")
	body := example(t, "deploy", nil)
	var wg sync.WaitGroup
	statuses := make([]int, 20)
	for i := range statuses {
		wg.Add(1)
		go func() {
			defer wg.Done()
			statuses[i] = post(t, srv, body).status
		}()
	}
	wg.Wait()
	if store.inserts != 1 {
		t.Errorf("%d inserts, want 1", store.inserts)
	}
	slices.Sort(statuses)
	if statuses[0] != http.StatusOK || statuses[len(statuses)-2] != http.StatusOK || statuses[len(statuses)-1] != http.StatusCreated {
		t.Errorf("want one 201 and the rest 200, got %v", statuses)
	}
}

func TestCreateInvalid(t *testing.T) {
	tests := []struct {
		name     string
		body     []byte
		status   int
		location string // expected among the problems, when status is 400 from the schema
	}{
		{"not JSON", []byte(`{"kind": "deploy",`), http.StatusBadRequest, ""},
		{"empty body", nil, http.StatusBadRequest, ""},
		{"missing version", example(t, "deploy", func(ev map[string]any) {
			delete(ev["attributes"].(map[string]any), "version.to")
		}), http.StatusBadRequest, "/attributes"},
		{"received_at from the client", example(t, "deploy", func(ev map[string]any) {
			ev["received_at"] = "2026-12-01T14:03:12Z"
		}), http.StatusBadRequest, ""},
		{"time without offset", example(t, "deploy", func(ev map[string]any) {
			ev["time"] = "2026-12-01T14:03:12"
		}), http.StatusBadRequest, "/time"},
		{"time before 1900", example(t, "deploy", func(ev map[string]any) {
			ev["time"] = "1850-01-01T00:00:00Z"
		}), http.StatusBadRequest, "/time"},
		{"body too large", example(t, "note", func(ev map[string]any) {
			ev["description"] = strings.Repeat("x", maxBody)
		}), http.StatusRequestEntityTooLarge, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := &memStore{}
			res := post(t, server(t, store, ""), tt.body)
			if res.status != tt.status {
				t.Fatalf("status %d, want %d: %s", res.status, tt.status, res.body)
			}
			if store.inserts != 0 {
				t.Errorf("%d inserts, want 0", store.inserts)
			}
			var body struct {
				Error    string           `json:"error"`
				Problems []schema.Problem `json:"problems"`
			}
			if err := json.Unmarshal(res.body, &body); err != nil || body.Error == "" {
				t.Fatalf("want a JSON error, got %s", res.body)
			}
			if tt.location == "" && body.Problems == nil {
				return
			}
			for _, p := range body.Problems {
				if p.Location == tt.location && p.Message != "" {
					return
				}
			}
			t.Errorf("no problem at %q in %s", tt.location, res.body)
		})
	}
}

func TestAuth(t *testing.T) {
	srv := server(t, &memStore{}, "s3cret")
	body := example(t, "note", nil)
	list := srv.URL + "/v1/changes?start=2026-12-01T00:00:00Z&end=2026-12-02T00:00:00Z"

	for _, token := range []string{"", "wrong", "S3CRET"} {
		if res := do(t, http.MethodPost, srv.URL+"/v1/changes", token, body); res.status != http.StatusUnauthorized {
			t.Errorf("POST with token %q: status %d", token, res.status)
		}
		if res := do(t, http.MethodGet, list, token, nil); res.status != http.StatusUnauthorized {
			t.Errorf("GET with token %q: status %d", token, res.status)
		}
	}
	if res := do(t, http.MethodPost, srv.URL+"/v1/changes", "s3cret", body); res.status != http.StatusCreated {
		t.Errorf("POST with the token: %d %s", res.status, res.body)
	}
	if res := do(t, http.MethodGet, list, "s3cret", nil); res.status != http.StatusOK {
		t.Errorf("GET with the token: %d %s", res.status, res.body)
	}
	if res := do(t, http.MethodGet, srv.URL+"/healthz", "", nil); res.status != http.StatusOK {
		t.Errorf("healthz: %d", res.status)
	}
}

func TestHealthzWithoutStore(t *testing.T) {
	srv := server(t, &memStore{err: errors.New("down")}, "")
	if res := do(t, http.MethodGet, srv.URL+"/healthz", "", nil); res.status != http.StatusOK {
		t.Errorf("healthz: %d", res.status)
	}
}

func at(minute, second int) time.Time {
	return time.Date(2026, 12, 1, 14, minute, second, 0, time.UTC)
}

func seeded() *memStore {
	return &memStore{events: []Event{
		{ID: "d1", Time: at(0, 0), Kind: "deploy", Environment: "demo", Service: "payments", Source: "t"},
		{ID: "f1", Time: at(10, 0), Kind: "flag", Environment: "demo", Source: "t"},
		{ID: "b", Time: at(20, 0), Kind: "deploy", Environment: "demo", Service: "orders", Source: "t"},
		{ID: "a", Time: at(20, 0), Kind: "note", Environment: "demo", Service: "orders", Source: "t"},
		{ID: "s1", Time: at(25, 0), Kind: "deploy", Environment: "staging", Service: "payments", Source: "t"},
		{ID: "d2", Time: at(30, 0), Kind: "deploy", Environment: "demo", Service: "payments", Source: "t"},
	}}
}

func TestList(t *testing.T) {
	tests := []struct {
		query string
		ids   []string
	}{
		// start is inclusive, end is exclusive.
		{"start=2026-12-01T14:00:00Z&end=2026-12-01T14:30:00Z", []string{"d1", "f1", "a", "b", "s1"}},
		{"start=2026-12-01T14:00:00.001Z&end=2026-12-01T14:30:00.001Z", []string{"f1", "a", "b", "s1", "d2"}},
		{"start=2026-12-01T11:00:00-03:00&end=2026-12-01T11:10:00-03:00", []string{"d1"}},
		{"start=2026-12-01T14:00:00Z&end=2026-12-01T15:00:00Z&environment=demo", []string{"d1", "f1", "a", "b", "d2"}},
		{"start=2026-12-01T14:00:00Z&end=2026-12-01T15:00:00Z&service=payments", []string{"d1", "s1", "d2"}},
		{"start=2026-12-01T14:00:00Z&end=2026-12-01T15:00:00Z&kind=deploy&environment=demo", []string{"d1", "b", "d2"}},
		{"start=2026-12-01T14:00:00Z&end=2026-12-01T15:00:00Z&kind=flag&service=payments", []string{}},
		{"start=2026-12-02T00:00:00Z&end=2026-12-03T00:00:00Z", []string{}},
	}
	srv := server(t, seeded(), "")
	for _, tt := range tests {
		t.Run(tt.query, func(t *testing.T) {
			res := do(t, http.MethodGet, srv.URL+"/v1/changes?"+tt.query, "", nil)
			if res.status != http.StatusOK {
				t.Fatalf("status %d: %s", res.status, res.body)
			}
			var body struct {
				Changes []Event `json:"changes"`
			}
			if err := json.Unmarshal(res.body, &body); err != nil || body.Changes == nil {
				t.Fatalf("want a changes array, got %s", res.body)
			}
			ids := []string{}
			for _, ev := range body.Changes {
				ids = append(ids, ev.ID)
			}
			if !slices.Equal(ids, tt.ids) {
				t.Errorf("got %v, want %v", ids, tt.ids)
			}
		})
	}
}

func TestListRoundsBoundsUp(t *testing.T) {
	store := seeded()
	srv := server(t, store, "")
	do(t, http.MethodGet, srv.URL+"/v1/changes?start=2026-12-01T14:00:00.0001Z&end=2026-12-01T14:00:00.0019Z", "", nil)
	q := store.queries[0]
	if want := at(0, 0).Add(time.Millisecond); !q.Start.Equal(want) {
		t.Errorf("start %v, want %v", q.Start, want)
	}
	if want := at(0, 0).Add(2 * time.Millisecond); !q.End.Equal(want) {
		t.Errorf("end %v, want %v", q.End, want)
	}
}

func TestListInvalid(t *testing.T) {
	for _, query := range []string{
		"",
		"start=2026-12-01T14:00:00Z",
		"end=2026-12-01T14:00:00Z",
		"start=2026-12-01T14:00:00&end=2026-12-01T15:00:00Z",
		"start=1764597600&end=2026-12-01T15:00:00Z",
		"start=2026-12-01T15:00:00Z&end=2026-12-01T15:00:00Z",
		"start=2026-12-01T15:00:00Z&end=2026-12-01T14:00:00Z",
		"start=2026-12-01T14:00:00Z&end=2026-12-01T15:00:00Z&kind=rollback",
		"start=2026-12-01T14:00:00Z&end=2026-12-01T15:00:00Z&services=payments",
		"start=2026-12-01T14:00:00Z&end=2026-12-01T15:00:00Z&kind=deploy&kind=flag",
	} {
		t.Run(query, func(t *testing.T) {
			store := seeded()
			res := do(t, http.MethodGet, server(t, store, "").URL+"/v1/changes?"+query, "", nil)
			if res.status != http.StatusBadRequest {
				t.Errorf("status %d: %s", res.status, res.body)
			}
			if len(store.queries) != 0 {
				t.Errorf("store was queried")
			}
		})
	}
}

func TestListTooMany(t *testing.T) {
	store := &memStore{}
	for i := range MaxList + 1 {
		store.events = append(store.events, Event{ID: fmt.Sprint(i), Time: at(0, 0), Kind: "note", Environment: "demo", Source: "t"})
	}
	res := do(t, http.MethodGet, server(t, store, "").URL+"/v1/changes?start=2026-12-01T14:00:00Z&end=2026-12-01T15:00:00Z", "", nil)
	if res.status != http.StatusUnprocessableEntity {
		t.Errorf("status %d", res.status)
	}

	store.events = store.events[:MaxList]
	res = do(t, http.MethodGet, server(t, store, "").URL+"/v1/changes?start=2026-12-01T14:00:00Z&end=2026-12-01T15:00:00Z", "", nil)
	if res.status != http.StatusOK {
		t.Errorf("status %d with exactly %d events", res.status, MaxList)
	}
}

func TestStoreUnavailable(t *testing.T) {
	srv := server(t, &memStore{err: errors.New("connection refused")}, "")
	for name, res := range map[string]response{
		"post without id": post(t, srv, example(t, "deploy", withoutID)),
		"post with id":    post(t, srv, example(t, "deploy", nil)),
		"list":            do(t, http.MethodGet, srv.URL+"/v1/changes?start=2026-12-01T14:00:00Z&end=2026-12-01T15:00:00Z", "", nil),
	} {
		if res.status != http.StatusServiceUnavailable {
			t.Errorf("%s: status %d", name, res.status)
		}
		if strings.Contains(string(res.body), "connection refused") {
			t.Errorf("%s: store error leaked to the client: %s", name, res.body)
		}
	}
}

func TestMethodNotAllowed(t *testing.T) {
	srv := server(t, &memStore{}, "")
	if res := do(t, http.MethodDelete, srv.URL+"/v1/changes", "", nil); res.status != http.StatusMethodNotAllowed {
		t.Errorf("status %d", res.status)
	}
}
