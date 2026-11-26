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
)

// fakeDB answers every QueryRow with a fixed row.
type fakeDB struct {
	scan func(dest ...any) error
}

func (f fakeDB) Exec(context.Context, string, ...any) (pgconn.CommandTag, error) {
	return pgconn.CommandTag{}, nil
}

func (f fakeDB) Query(context.Context, string, ...any) (pgx.Rows, error) {
	panic("not used")
}

func (f fakeDB) QueryRow(context.Context, string, ...any) pgx.Row { return rowFunc(f.scan) }

type rowFunc func(dest ...any) error

func (fn rowFunc) Scan(dest ...any) error { return fn(dest...) }

func TestCreatePayment(t *testing.T) {
	db := fakeDB{scan: func(dest ...any) error {
		*dest[0].(*string) = "c0ffee00-0000-0000-0000-000000000001"
		*dest[1].(*string) = "captured"
		*dest[2].(*time.Time) = time.Unix(0, 0)
		return nil
	}}
	s := &server{db: db}

	tests := []struct {
		name string
		body string
		want int
	}{
		{"ok", `{"order_id":"c0ffee00-0000-0000-0000-000000000002","amount_cents":1250,"currency":"USD"}`, http.StatusCreated},
		{"missing order", `{"amount_cents":1250,"currency":"USD"}`, http.StatusBadRequest},
		{"zero amount", `{"order_id":"x","amount_cents":0,"currency":"USD"}`, http.StatusBadRequest},
		{"other currency", `{"order_id":"x","amount_cents":10,"currency":"EUR"}`, http.StatusBadRequest},
		{"unknown field", `{"order_id":"x","amount_cents":10,"currency":"USD","card":"4242"}`, http.StatusBadRequest},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodPost, "/payments", strings.NewReader(tt.body))
			w := httptest.NewRecorder()
			s.createPayment(w, r)
			if w.Code != tt.want {
				t.Fatalf("status = %d, want %d (body %s)", w.Code, tt.want, w.Body)
			}
		})
	}
}

func TestCreatePaymentDuplicate(t *testing.T) {
	s := &server{db: fakeDB{scan: func(...any) error { return &pgconn.PgError{Code: "23505"} }}}
	body := `{"order_id":"c0ffee00-0000-0000-0000-000000000002","amount_cents":1250,"currency":"USD"}`
	w := httptest.NewRecorder()
	s.createPayment(w, httptest.NewRequest(http.MethodPost, "/payments", strings.NewReader(body)))
	if w.Code != http.StatusConflict {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusConflict)
	}
}
