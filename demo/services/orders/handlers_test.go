package main

import (
	"context"
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

// openCartDB reports every cart as open and fails anything else.
type openCartDB struct{}

func (openCartDB) Exec(context.Context, string, ...any) (pgconn.CommandTag, error) {
	return pgconn.CommandTag{}, nil
}

func (openCartDB) Query(context.Context, string, ...any) (pgx.Rows, error) { panic("not used") }

func (openCartDB) QueryRow(context.Context, string, ...any) pgx.Row {
	return rowFunc(func(dest ...any) error {
		*dest[0].(*string) = cartOpen
		return nil
	})
}

func (openCartDB) InTx(context.Context, func(pg.Querier) error) error { panic("not used") }

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

func TestAddItemCatalogErrors(t *testing.T) {
	tests := []struct {
		name          string
		body          string
		catalogStatus int
		want          int
	}{
		{"bad quantity", `{"product_id":1,"quantity":0}`, http.StatusOK, http.StatusBadRequest},
		{"too many", `{"product_id":1,"quantity":11}`, http.StatusOK, http.StatusBadRequest},
		{"unknown product", `{"product_id":999,"quantity":1}`, http.StatusNotFound, http.StatusNotFound},
		{"catalog down", `{"product_id":1,"quantity":1}`, http.StatusInternalServerError, http.StatusBadGateway},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			catalog := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				http.Error(w, "{}", tt.catalogStatus)
			}))
			defer catalog.Close()
			s := &server{db: openCartDB{}, catalog: httpx.NewClient("catalog", catalog.URL, time.Second, fault.Nop{})}

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
