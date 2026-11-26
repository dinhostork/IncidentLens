package main

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/dinhostork/IncidentLens/demo/internal/fault"
	"github.com/dinhostork/IncidentLens/demo/internal/httpx"
	"github.com/dinhostork/IncidentLens/demo/internal/pg"
)

// cartDB reports every cart with a fixed status. Exec affects no rows, as a
// conditional write to a cart that is not open would.
type cartDB struct{ status string }

func (cartDB) Exec(context.Context, string, ...any) (pgconn.CommandTag, error) {
	return pgconn.NewCommandTag("INSERT 0 0"), nil
}

func (cartDB) Query(context.Context, string, ...any) (pgx.Rows, error) { panic("not used") }

func (db cartDB) QueryRow(context.Context, string, ...any) pgx.Row {
	return rowFunc(func(dest ...any) error {
		*dest[0].(*string) = db.status
		return nil
	})
}

func (cartDB) InTx(context.Context, func(pg.Querier) error) error { panic("not used") }

type rowFunc func(dest ...any) error

func (fn rowFunc) Scan(dest ...any) error { return fn(dest...) }

func TestTotal(t *testing.T) {
	items := []item{{ProductID: 1, Quantity: 2, UnitPriceCents: 1250}, {ProductID: 7, Quantity: 1, UnitPriceCents: 99}}
	if got := total(items); got != 2599 {
		t.Fatalf("total = %d, want 2599", got)
	}
	if got := total(nil); got != 0 {
		t.Fatalf("total(nil) = %d, want 0", got)
	}
}

func TestParseID(t *testing.T) {
	const canonical = "c0ffee00-0000-0000-0000-000000000001"
	for _, in := range []string{canonical, "urn:uuid:" + canonical, "{" + canonical + "}", "C0FFEE00-0000-0000-0000-000000000001"} {
		if got, err := parseID(in); err != nil || got != canonical {
			t.Errorf("parseID(%q) = %q, %v; want %q", in, got, err, canonical)
		}
	}
	for _, in := range []string{"", "nope", "c0ffee00"} {
		if _, err := parseID(in); !errors.Is(err, errNotFound) {
			t.Errorf("parseID(%q) err = %v, want errNotFound", in, err)
		}
	}
}

func TestAddItem(t *testing.T) {
	tests := []struct {
		name          string
		cartStatus    string
		body          string
		catalogStatus int
		want          int
	}{
		{"bad quantity", cartOpen, `{"product_id":1,"quantity":0}`, http.StatusOK, http.StatusBadRequest},
		{"too many", cartOpen, `{"product_id":1,"quantity":11}`, http.StatusOK, http.StatusBadRequest},
		{"unknown product", cartOpen, `{"product_id":999,"quantity":1}`, http.StatusNotFound, http.StatusNotFound},
		{"catalog down", cartOpen, `{"product_id":1,"quantity":1}`, http.StatusInternalServerError, http.StatusBadGateway},
		{"cart checking out", cartCheckingOut, `{"product_id":1,"quantity":1}`, http.StatusOK, http.StatusConflict},
		{"cart checked out", cartCheckedOut, `{"product_id":1,"quantity":1}`, http.StatusOK, http.StatusConflict},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			catalog := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				httpx.WriteJSON(w, tt.catalogStatus, map[string]int{"id": 1, "price_cents": 100})
			}))
			defer catalog.Close()
			s := &server{db: cartDB{status: tt.cartStatus}, catalog: httpx.NewClient("catalog", catalog.URL, time.Second, fault.Nop{})}

			r := httptest.NewRequest(http.MethodPost, "/carts/x/items", strings.NewReader(tt.body))
			r.SetPathValue("id", "c0ffee00-0000-0000-0000-000000000001")
			w := httptest.NewRecorder()
			s.addItem(w, r)
			if w.Code != tt.want {
				t.Fatalf("status = %d, want %d (body %s)", w.Code, tt.want, w.Body)
			}
		})
	}
}
