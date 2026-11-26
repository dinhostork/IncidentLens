package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/dinhostork/IncidentLens/demo/internal/httpx"
	"github.com/dinhostork/IncidentLens/demo/internal/pg"
)

type server struct {
	db pg.Querier
}

type paymentRequest struct {
	OrderID     string `json:"order_id"`
	AmountCents int64  `json:"amount_cents"`
	Currency    string `json:"currency"`
}

type payment struct {
	ID          string    `json:"id"`
	OrderID     string    `json:"order_id"`
	AmountCents int64     `json:"amount_cents"`
	Currency    string    `json:"currency"`
	Status      string    `json:"status"`
	CreatedAt   time.Time `json:"created_at"`
}

func (req paymentRequest) validate() error {
	switch {
	case req.OrderID == "":
		return errors.New("order_id is required")
	case uuid.Validate(req.OrderID) != nil:
		return errors.New("order_id is not a UUID")
	case req.AmountCents <= 0:
		return errors.New("amount_cents must be positive")
	case req.Currency != "USD":
		return fmt.Errorf("unsupported currency %q", req.Currency)
	}
	return nil
}

func (s *server) createPayment(w http.ResponseWriter, r *http.Request) {
	var req paymentRequest
	if err := httpx.ReadJSON(r, &req); err != nil {
		httpx.Fail(w, r, http.StatusBadRequest, err)
		return
	}
	if err := req.validate(); err != nil {
		httpx.Fail(w, r, http.StatusBadRequest, err)
		return
	}
	p, err := s.insert(r.Context(), req)
	var pgErr *pgconn.PgError
	switch {
	case errors.As(err, &pgErr) && pgErr.Code == "23505": // unique_violation
		httpx.Fail(w, r, http.StatusConflict, errors.New("order already paid"))
		return
	case err != nil:
		httpx.Fail(w, r, http.StatusInternalServerError, err)
		return
	}
	slog.InfoContext(r.Context(), "payment captured", "payment.id", p.ID, "order.id", p.OrderID, "amount_cents", p.AmountCents)
	httpx.WriteJSON(w, http.StatusCreated, p)
}

func (s *server) insert(ctx context.Context, req paymentRequest) (payment, error) {
	p := payment{OrderID: req.OrderID, AmountCents: req.AmountCents, Currency: req.Currency}
	err := s.db.QueryRow(ctx,
		`INSERT INTO payments (order_id, amount_cents, currency, status)
		 VALUES ($1, $2, $3, 'captured')
		 RETURNING id, status, created_at`,
		req.OrderID, req.AmountCents, req.Currency,
	).Scan(&p.ID, &p.Status, &p.CreatedAt)
	return p, err
}

func (s *server) getPayment(w http.ResponseWriter, r *http.Request) {
	if uuid.Validate(r.PathValue("id")) != nil {
		httpx.Fail(w, r, http.StatusNotFound, errors.New("payment not found"))
		return
	}
	var p payment
	err := s.db.QueryRow(r.Context(),
		`SELECT id, order_id, amount_cents, currency, status, created_at FROM payments WHERE id = $1`,
		r.PathValue("id"),
	).Scan(&p.ID, &p.OrderID, &p.AmountCents, &p.Currency, &p.Status, &p.CreatedAt)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		httpx.Fail(w, r, http.StatusNotFound, errors.New("payment not found"))
		return
	case err != nil:
		httpx.Fail(w, r, http.StatusInternalServerError, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, p)
}
