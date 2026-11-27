package api

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"time"

	"github.com/dinhostork/IncidentLens/changes/schema"
)

// Event is a change event as stored. Service, Description and Attributes are
// empty when the client left them out.
type Event struct {
	ID          string            `json:"id"`
	Time        time.Time         `json:"time"`
	ReceivedAt  time.Time         `json:"received_at"`
	Kind        string            `json:"kind"`
	Environment string            `json:"environment"`
	Service     string            `json:"service,omitempty"`
	Description string            `json:"description,omitempty"`
	Attributes  map[string]string `json:"attributes,omitempty"`
	Source      string            `json:"source"`
}

// Query selects the events with Start <= time < End. Empty string filters
// match everything. Limit is the maximum number of events to return.
type Query struct {
	Start, End  time.Time
	Environment string
	Service     string
	Kind        string
	Limit       int
}

// Store keeps change events. IDs are unique across the store.
type Store interface {
	// Get returns the event with the given ID, and false if there is none.
	Get(ctx context.Context, id string) (Event, bool, error)
	Insert(ctx context.Context, ev Event) error
	// List returns the events matching q, ordered by time and then ID.
	List(ctx context.Context, q Query) ([]Event, error)
}

// Kinds are the values the schema allows for kind.
var Kinds = []string{"deploy", "config", "flag", "note"}

// The range of DateTime64 in ClickHouse. Outside it values are clamped
// silently, so they are rejected here instead.
var (
	minTime = time.Date(1900, 1, 1, 0, 0, 0, 0, time.UTC)
	maxTime = time.Date(2299, 12, 31, 23, 59, 59, 999e6, time.UTC)
)

// parseEvent decodes a body that already passed schema.Validate. Times are
// stored to the millisecond, so the event time is truncated to that here and
// two posts that only differ below a millisecond count as the same event.
func parseEvent(body []byte) (Event, []schema.Problem) {
	var in struct {
		ID          string            `json:"id"`
		Time        string            `json:"time"`
		Kind        string            `json:"kind"`
		Environment string            `json:"environment"`
		Service     string            `json:"service"`
		Description string            `json:"description"`
		Attributes  map[string]string `json:"attributes"`
		Source      string            `json:"source"`
	}
	if err := json.Unmarshal(body, &in); err != nil {
		return Event{}, []schema.Problem{{Message: err.Error()}}
	}
	t, err := time.Parse(time.RFC3339Nano, in.Time)
	if err != nil {
		return Event{}, []schema.Problem{{Location: "/time", Message: fmt.Sprintf("not an RFC 3339 timestamp: %v", err)}}
	}
	t = t.UTC().Truncate(time.Millisecond)
	if t.Before(minTime) || t.After(maxTime) {
		return Event{}, []schema.Problem{{Location: "/time", Message: "outside the supported range (years 1900 to 2299)"}}
	}
	if len(in.Attributes) == 0 {
		in.Attributes = nil
	}
	return Event{
		ID:          in.ID,
		Time:        t,
		Kind:        in.Kind,
		Environment: in.Environment,
		Service:     in.Service,
		Description: in.Description,
		Attributes:  in.Attributes,
		Source:      in.Source,
	}, nil
}

// sameContent reports whether two events describe the same change, ignoring
// when each was received.
func sameContent(a, b Event) bool {
	return a.ID == b.ID &&
		a.Time.Equal(b.Time) &&
		a.Kind == b.Kind &&
		a.Environment == b.Environment &&
		a.Service == b.Service &&
		a.Description == b.Description &&
		maps.Equal(a.Attributes, b.Attributes) &&
		a.Source == b.Source
}
