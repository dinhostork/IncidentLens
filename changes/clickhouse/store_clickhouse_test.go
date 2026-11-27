//go:build clickhouse

// Tests against a real ClickHouse server. Run with `make test-clickhouse`
// while the local stack is up, or set CLICKHOUSE_URL, CLICKHOUSE_USER and
// CLICKHOUSE_PASSWORD for another server. Each run creates its own database
// from deploy/clickhouse/init and drops it at the end.
package clickhouse

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/dinhostork/IncidentLens/changes/api"
)

func env(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

// testStore creates a database named incidentlens_test_<n>, applies the init
// SQL files to it with the database name swapped, and drops it afterwards.
func testStore(t *testing.T) *Store {
	t.Helper()
	db := fmt.Sprintf("incidentlens_test_%d", time.Now().UnixNano())
	s, err := New(Config{
		URL:      env("CLICKHOUSE_URL", "http://127.0.0.1:8123"),
		User:     env("CLICKHOUSE_USER", "incidentlens"),
		Password: env("CLICKHOUSE_PASSWORD", "incidentlens"),
		Database: db,
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	exec := func(sql string) error {
		body, err := s.do(ctx, sql, nil, nil)
		if err != nil {
			return err
		}
		return body.Close()
	}

	files, err := filepath.Glob("../../deploy/clickhouse/init/*.sql")
	if err != nil || len(files) == 0 {
		t.Fatalf("no init SQL files found: %v", err)
	}
	comment := regexp.MustCompile(`--[^\n]*`)
	dbName := regexp.MustCompile(`\bincidentlens\b`)
	t.Cleanup(func() {
		if err := exec("DROP DATABASE IF EXISTS `" + db + "`"); err != nil {
			t.Errorf("drop test database: %v", err)
		}
	})
	for _, f := range files {
		data, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		sql := dbName.ReplaceAllString(comment.ReplaceAllString(string(data), ""), db)
		// The HTTP interface takes one statement per request.
		for _, stmt := range strings.Split(sql, ";") {
			if stmt = strings.TrimSpace(stmt); stmt != "" {
				if err := exec(stmt); err != nil {
					t.Fatalf("%s: %v", filepath.Base(f), err)
				}
			}
		}
	}
	return s
}

func at(minute, second, ms int) time.Time {
	return time.Date(2026, 12, 1, 14, minute, second, ms*1e6, time.UTC)
}

func event(id string, t time.Time, kind, env, service string) api.Event {
	return api.Event{ID: id, Time: t, ReceivedAt: at(59, 0, 0), Kind: kind, Environment: env, Service: service, Source: "test"}
}

func equal(a, b api.Event) bool {
	return a.ID == b.ID && a.Time.Equal(b.Time) && a.ReceivedAt.Equal(b.ReceivedAt) &&
		a.Kind == b.Kind && a.Environment == b.Environment && a.Service == b.Service &&
		a.Description == b.Description && a.Source == b.Source &&
		len(a.Attributes) == len(b.Attributes) && fmt.Sprint(a.Attributes) == fmt.Sprint(b.Attributes)
}

func TestStore(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()

	full := api.Event{
		ID:          "gha-1",
		Time:        at(3, 12, 123),
		ReceivedAt:  at(3, 20, 7),
		Kind:        "deploy",
		Environment: "demo",
		Service:     "payments",
		Description: "line one\nline 'two' \"quoted\" ção",
		Attributes:  map[string]string{"version.from": "", "version.to": "1.0.1", "note": "it's {not:String} a param"},
		Source:      "github-actions",
	}
	bare := api.Event{ID: "cfg-1", Time: at(0, 0, 0), ReceivedAt: at(0, 0, 1), Kind: "config", Environment: "demo", Source: "manual"}

	t.Run("round trip", func(t *testing.T) {
		for _, ev := range []api.Event{full, bare} {
			if err := s.Insert(ctx, ev); err != nil {
				t.Fatal(err)
			}
			got, ok, err := s.Get(ctx, ev.ID)
			if err != nil || !ok {
				t.Fatalf("get %s: %v %v", ev.ID, ok, err)
			}
			if !equal(got, ev) {
				t.Errorf("got  %+v\nwant %+v", got, ev)
			}
		}
		if bare, _, _ := s.Get(ctx, "cfg-1"); bare.Attributes != nil {
			t.Errorf("want nil attributes for an event without any, got %#v", bare.Attributes)
		}
	})

	t.Run("missing id", func(t *testing.T) {
		for _, id := range []string{"nope", "x' OR 1=1 --", "gha-1' OR '1'='1"} {
			if _, ok, err := s.Get(ctx, id); err != nil || ok {
				t.Errorf("get %q: found=%v err=%v", id, ok, err)
			}
		}
	})

	t.Run("duplicate insert is read once", func(t *testing.T) {
		dup := event("dup-1", at(40, 0, 0), "note", "dup", "")
		dup.Description = "twice"
		for range 2 {
			if err := s.Insert(ctx, dup); err != nil {
				t.Fatal(err)
			}
		}
		got, err := s.List(ctx, api.Query{Start: at(0, 0, 0), End: at(59, 0, 0), Environment: "dup", Limit: 10})
		if err != nil {
			t.Fatal(err)
		}
		if len(got) != 1 {
			t.Errorf("got %d events, want 1", len(got))
		}
	})

	t.Run("list", func(t *testing.T) {
		for _, ev := range []api.Event{
			event("w-d1", at(10, 0, 0), "deploy", "win", "payments"),
			event("w-f1", at(15, 0, 0), "flag", "win", ""),
			event("w-b", at(20, 0, 0), "deploy", "win", "orders"),
			event("w-a", at(20, 0, 0), "note", "win", "orders"),
			event("w-s1", at(25, 0, 0), "deploy", "win-staging", "payments"),
			event("w-d2", at(30, 0, 0), "deploy", "win", "payments"),
			event("w-ms", at(30, 0, 1), "deploy", "win", "payments"),
		} {
			if err := s.Insert(ctx, ev); err != nil {
				t.Fatal(err)
			}
		}
		tests := []struct {
			name string
			q    api.Query
			ids  []string
		}{
			{"start inclusive, end exclusive", api.Query{Start: at(10, 0, 0), End: at(30, 0, 0), Environment: "win"}, []string{"w-d1", "w-f1", "w-a", "w-b"}},
			{"millisecond bounds", api.Query{Start: at(30, 0, 0), End: at(30, 0, 1), Environment: "win"}, []string{"w-d2"}},
			{"no filters", api.Query{Start: at(24, 0, 0), End: at(26, 0, 0)}, []string{"w-s1"}},
			{"service", api.Query{Start: at(10, 0, 0), End: at(59, 0, 0), Service: "payments"}, []string{"w-d1", "w-s1", "w-d2", "w-ms"}},
			{"kind and environment", api.Query{Start: at(10, 0, 0), End: at(59, 0, 0), Environment: "win", Kind: "deploy"}, []string{"w-d1", "w-b", "w-d2", "w-ms"}},
			{"nothing", api.Query{Start: at(10, 0, 0), End: at(59, 0, 0), Environment: "win", Kind: "flag", Service: "payments"}, nil},
			{"limit", api.Query{Start: at(10, 0, 0), End: at(59, 0, 0), Environment: "win", Limit: 2}, []string{"w-d1", "w-f1"}},
		}
		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				if tt.q.Limit == 0 {
					tt.q.Limit = 100
				}
				got, err := s.List(ctx, tt.q)
				if err != nil {
					t.Fatal(err)
				}
				var ids []string
				for _, ev := range got {
					ids = append(ids, ev.ID)
				}
				if !slices.Equal(ids, tt.ids) {
					t.Errorf("got %v, want %v", ids, tt.ids)
				}
			})
		}
	})
}

// TestAPI runs the HTTP API on top of the real store.
func TestAPI(t *testing.T) {
	srv := httptest.NewServer(api.New(testStore(t), api.Options{}))
	defer srv.Close()

	call := func(method, path, body string) (int, string) {
		t.Helper()
		req, err := http.NewRequest(method, srv.URL+path, strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
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
		return res.StatusCode, string(b)
	}

	deploy := `{"id":"it-1","time":"2026-12-01T11:03:12.5-03:00","kind":"deploy","environment":"demo","service":"payments",
		"attributes":{"version.from":"1.0.0","version.to":"1.0.1"},"source":"test"}`
	status, first := call(http.MethodPost, "/v1/changes", deploy)
	if status != http.StatusCreated {
		t.Fatalf("post: %d %s", status, first)
	}
	if status, again := call(http.MethodPost, "/v1/changes", deploy); status != http.StatusOK || again != first {
		t.Errorf("retry: %d %s, want 200 %s", status, again, first)
	}
	changed := strings.Replace(deploy, `"1.0.1"`, `"1.0.2"`, 1)
	if status, body := call(http.MethodPost, "/v1/changes", changed); status != http.StatusConflict {
		t.Errorf("conflicting retry: %d %s", status, body)
	}
	status, note := call(http.MethodPost, "/v1/changes", `{"time":"2026-12-01T14:04:00Z","kind":"note","environment":"demo","description":"x","source":"test"}`)
	if status != http.StatusCreated {
		t.Fatalf("post note: %d %s", status, note)
	}

	status, body := call(http.MethodGet, "/v1/changes?start=2026-12-01T14:00:00Z&end=2026-12-01T15:00:00Z&environment=demo", "")
	if status != http.StatusOK {
		t.Fatalf("list: %d %s", status, body)
	}
	var list struct {
		Changes []json.RawMessage `json:"changes"`
	}
	if err := json.Unmarshal([]byte(body), &list); err != nil {
		t.Fatal(err)
	}
	if len(list.Changes) != 2 {
		t.Fatalf("want 2 events, got %s", body)
	}
	var stored bytes.Buffer
	if err := json.Compact(&stored, []byte(first)); err != nil {
		t.Fatal(err)
	}
	if string(list.Changes[0]) != stored.String() {
		t.Errorf("listed %s\nwant the stored event %s", list.Changes[0], stored.String())
	}
	if !strings.Contains(first, `"time":"2026-12-01T14:03:12.5Z"`) {
		t.Errorf("want the time in UTC, got %s", first)
	}
}
