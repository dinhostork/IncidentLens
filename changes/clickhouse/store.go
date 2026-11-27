// Package clickhouse stores change events in the changes table of a ClickHouse
// server, through its HTTP interface. The table is created by
// deploy/clickhouse/init, never here.
package clickhouse

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/dinhostork/IncidentLens/changes/api"
)

// Config says where the changes table is.
type Config struct {
	URL      string // HTTP interface, e.g. http://127.0.0.1:8123
	User     string
	Password string
	Database string // the table is <Database>.changes
	Timeout  time.Duration
}

// Store implements api.Store.
type Store struct {
	endpoint string
	user     string
	password string
	table    string
	client   *http.Client
}

var identifier = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// New checks cfg and returns a Store. It does not connect.
func New(cfg Config) (*Store, error) {
	u, err := url.Parse(cfg.URL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return nil, fmt.Errorf("clickhouse: URL must be http(s)://host:port, got %q", cfg.URL)
	}
	if !identifier.MatchString(cfg.Database) {
		return nil, fmt.Errorf("clickhouse: invalid database name %q", cfg.Database)
	}
	if cfg.Timeout == 0 {
		cfg.Timeout = 10 * time.Second
	}
	return &Store{
		endpoint: strings.TrimSuffix(u.String(), "/") + "/",
		user:     cfg.User,
		password: cfg.Password,
		table:    "`" + cfg.Database + "`.changes",
		client:   &http.Client{Timeout: cfg.Timeout},
	}, nil
}

// insertRow and readRow are the JSONEachRow shapes of a table row. Times are
// written in ClickHouse's text format and read back as Unix milliseconds,
// which avoids depending on its date output settings.
type insertRow struct {
	ID          string            `json:"id"`
	Time        string            `json:"time"`
	ReceivedAt  string            `json:"received_at"`
	Kind        string            `json:"kind"`
	Environment string            `json:"environment"`
	Service     string            `json:"service"`
	Description string            `json:"description"`
	Attributes  map[string]string `json:"attributes"`
	Source      string            `json:"source"`
}

type readRow struct {
	ID          string            `json:"id"`
	TimeMs      int64             `json:"time_ms"`
	ReceivedMs  int64             `json:"received_ms"`
	Kind        string            `json:"kind"`
	Environment string            `json:"environment"`
	Service     string            `json:"service"`
	Description string            `json:"description"`
	Attributes  map[string]string `json:"attributes"`
	Source      string            `json:"source"`
}

const columns = `id, toUnixTimestamp64Milli(time) AS time_ms, toUnixTimestamp64Milli(received_at) AS received_ms,
	kind, environment, service, description, attributes, source`

// DateTime64(3) text format; the column's time zone is UTC.
const timeFormat = "2006-01-02 15:04:05.000"

// Get implements api.Store.
func (s *Store) Get(ctx context.Context, id string) (api.Event, bool, error) {
	// FINAL folds duplicate inserts of the same row. If two different rows
	// share an ID (the API prevents it, a direct insert does not), the first
	// one received wins.
	q := "SELECT " + columns + " FROM " + s.table + " FINAL WHERE id = {id:String} ORDER BY received_at, time LIMIT 1"
	events, err := s.selectEvents(ctx, q, url.Values{"param_id": {id}})
	if err != nil || len(events) == 0 {
		return api.Event{}, false, err
	}
	return events[0], true, nil
}

// Insert implements api.Store.
func (s *Store) Insert(ctx context.Context, ev api.Event) error {
	attrs := ev.Attributes
	if attrs == nil {
		attrs = map[string]string{}
	}
	data, err := json.Marshal(insertRow{
		ID:          ev.ID,
		Time:        ev.Time.UTC().Format(timeFormat),
		ReceivedAt:  ev.ReceivedAt.UTC().Format(timeFormat),
		Kind:        ev.Kind,
		Environment: ev.Environment,
		Service:     ev.Service,
		Description: ev.Description,
		Attributes:  attrs,
		Source:      ev.Source,
	})
	if err != nil {
		return err
	}
	q := "INSERT INTO " + s.table + " (id, time, received_at, kind, environment, service, description, attributes, source) FORMAT JSONEachRow"
	body, err := s.do(ctx, q, nil, bytes.NewReader(data))
	if err != nil {
		return err
	}
	return body.Close()
}

// List implements api.Store.
func (s *Store) List(ctx context.Context, q api.Query) ([]api.Event, error) {
	params := url.Values{
		"param_start": {q.Start.UTC().Format(timeFormat)},
		"param_end":   {q.End.UTC().Format(timeFormat)},
		"param_limit": {strconv.Itoa(q.Limit)},
	}
	where := []string{"time >= {start:DateTime64(3, 'UTC')}", "time < {end:DateTime64(3, 'UTC')}"}
	for _, f := range []struct{ column, value string }{
		{"environment", q.Environment},
		{"service", q.Service},
		{"kind", q.Kind},
	} {
		if f.value != "" {
			where = append(where, fmt.Sprintf("%s = {%s:String}", f.column, f.column))
			params.Set("param_"+f.column, f.value)
		}
	}
	sql := "SELECT " + columns + " FROM " + s.table + " FINAL WHERE " + strings.Join(where, " AND ") +
		" ORDER BY time, id LIMIT {limit:UInt32}"
	return s.selectEvents(ctx, sql, params)
}

func (s *Store) selectEvents(ctx context.Context, query string, params url.Values) ([]api.Event, error) {
	body, err := s.do(ctx, query+" FORMAT JSONEachRow", params, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = body.Close() }()
	var events []api.Event
	sc := bufio.NewScanner(body)
	sc.Buffer(make([]byte, 0, 64<<10), 4<<20)
	for sc.Scan() {
		var r readRow
		if err := json.Unmarshal(sc.Bytes(), &r); err != nil {
			return nil, fmt.Errorf("clickhouse: decode row: %w", err)
		}
		if len(r.Attributes) == 0 {
			r.Attributes = nil
		}
		events = append(events, api.Event{
			ID:          r.ID,
			Time:        time.UnixMilli(r.TimeMs).UTC(),
			ReceivedAt:  time.UnixMilli(r.ReceivedMs).UTC(),
			Kind:        r.Kind,
			Environment: r.Environment,
			Service:     r.Service,
			Description: r.Description,
			Attributes:  r.Attributes,
			Source:      r.Source,
		})
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("clickhouse: read rows: %w", err)
	}
	return events, nil
}

// do sends one statement. Values for {name:Type} placeholders go in params
// as param_<name>; ClickHouse substitutes them, so they are never part of the
// SQL text. The caller closes the returned body.
func (s *Store) do(ctx context.Context, query string, params url.Values, body io.Reader) (io.ReadCloser, error) {
	v := url.Values{}
	for k, vs := range params {
		v[k] = vs
	}
	v.Set("query", query)
	// Buffer the result so an error halfway through a query is reported with
	// a non-200 status instead of after a 200 and some rows.
	v.Set("wait_end_of_query", "1")
	v.Set("output_format_json_quote_64bit_integers", "0")
	if body == nil {
		body = http.NoBody
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.endpoint+"?"+v.Encode(), body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("X-ClickHouse-User", s.user)
	req.Header.Set("X-ClickHouse-Key", s.password)
	res, err := s.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("clickhouse: %w", err)
	}
	if res.StatusCode != http.StatusOK {
		defer func() { _ = res.Body.Close() }()
		msg, _ := io.ReadAll(io.LimitReader(res.Body, 2048))
		return nil, fmt.Errorf("clickhouse: %s: %s", res.Status, bytes.TrimSpace(msg))
	}
	return res.Body, nil
}
