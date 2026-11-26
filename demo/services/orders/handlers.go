package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"

	"github.com/dinhostork/IncidentLens/demo/internal/httpx"
	"github.com/dinhostork/IncidentLens/demo/internal/pg"
)

type store interface {
	pg.Querier
	InTx(ctx context.Context, fn func(pg.Querier) error) error
}

type server struct {
	db       store
	catalog  *httpx.Client
	payments *httpx.Client
}

func (s *server) createCart(w http.ResponseWriter, r *http.Request) {
	c := cart{Items: []item{}}
	err := s.db.QueryRow(r.Context(), `INSERT INTO carts DEFAULT VALUES RETURNING id, status`).Scan(&c.ID, &c.Status)
	if err != nil {
		httpx.Fail(w, r, http.StatusInternalServerError, err)
		return
	}
	httpx.WriteJSON(w, http.StatusCreated, c)
}

func (s *server) getCart(w http.ResponseWriter, r *http.Request) {
	c, err := loadCart(r.Context(), s.db, r.PathValue("id"))
	if err != nil {
		failCart(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, c)
}

type addItemRequest struct {
	ProductID int64 `json:"product_id"`
	Quantity  int   `json:"quantity"`
}

type product struct {
	ID         int64 `json:"id"`
	PriceCents int64 `json:"price_cents"`
}

func (s *server) addItem(w http.ResponseWriter, r *http.Request) {
	ctx, id := r.Context(), r.PathValue("id")
	var req addItemRequest
	if err := httpx.ReadJSON(r, &req); err != nil {
		httpx.Fail(w, r, http.StatusBadRequest, err)
		return
	}
	if req.ProductID <= 0 || req.Quantity < 1 || req.Quantity > maxQuantity {
		httpx.Fail(w, r, http.StatusBadRequest,
			fmt.Errorf("product_id must be positive and quantity between 1 and %d", maxQuantity))
		return
	}

	status, err := cartStatus(ctx, s.db, id)
	if err != nil {
		failCart(w, r, err)
		return
	}
	if status != cartOpen {
		httpx.Fail(w, r, http.StatusConflict, errors.New("cart is already checked out"))
		return
	}

	var p product
	if err := s.catalog.Do(ctx, http.MethodGet, fmt.Sprintf("/products/%d", req.ProductID), nil, &p); err != nil {
		httpx.Fail(w, r, httpx.Upstream(err), fmt.Errorf("catalog: %w", err))
		return
	}

	_, err = s.db.Exec(ctx,
		`INSERT INTO cart_items (cart_id, product_id, quantity, unit_price_cents) VALUES ($1, $2, $3, $4)
		 ON CONFLICT (cart_id, product_id)
		 DO UPDATE SET quantity = LEAST(cart_items.quantity + EXCLUDED.quantity, $5), unit_price_cents = EXCLUDED.unit_price_cents`,
		id, p.ID, req.Quantity, p.PriceCents, maxQuantity)
	if err != nil {
		httpx.Fail(w, r, http.StatusInternalServerError, err)
		return
	}

	c, err := loadCart(ctx, s.db, id)
	if err != nil {
		failCart(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, c)
}

type order struct {
	ID         string `json:"id"`
	CartID     string `json:"cart_id"`
	TotalCents int64  `json:"total_cents"`
	Status     string `json:"status"`
	PaymentID  string `json:"payment_id,omitempty"`
}

type paymentRequest struct {
	OrderID     string `json:"order_id"`
	AmountCents int64  `json:"amount_cents"`
	Currency    string `json:"currency"`
}

type paymentResponse struct {
	ID string `json:"id"`
}

func (s *server) checkout(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	c, err := loadCart(ctx, s.db, r.PathValue("id"))
	if err != nil {
		failCart(w, r, err)
		return
	}
	switch {
	case c.Status != cartOpen:
		httpx.Fail(w, r, http.StatusConflict, errors.New("cart is already checked out"))
		return
	case len(c.Items) == 0:
		httpx.Fail(w, r, http.StatusBadRequest, errors.New("cart is empty"))
		return
	}

	o := order{CartID: c.ID, TotalCents: c.TotalCents, Status: "pending"}
	err = s.db.QueryRow(ctx,
		`INSERT INTO orders (cart_id, total_cents, status) VALUES ($1, $2, $3) RETURNING id`,
		o.CartID, o.TotalCents, o.Status).Scan(&o.ID)
	if err != nil {
		httpx.Fail(w, r, http.StatusInternalServerError, err)
		return
	}

	var pay paymentResponse
	err = s.payments.Do(ctx, http.MethodPost, "/payments",
		paymentRequest{OrderID: o.ID, AmountCents: o.TotalCents, Currency: "USD"}, &pay)
	if err != nil {
		// Record the failure even if the request was cancelled.
		_, uerr := s.db.Exec(context.WithoutCancel(ctx), `UPDATE orders SET status = 'payment_failed' WHERE id = $1`, o.ID)
		httpx.Fail(w, r, httpx.Upstream(err), errors.Join(fmt.Errorf("payments: %w", err), uerr))
		return
	}

	o.Status, o.PaymentID = "paid", pay.ID
	err = s.db.InTx(ctx, func(q pg.Querier) error {
		if _, err := q.Exec(ctx, `UPDATE orders SET status = $2, payment_id = $3 WHERE id = $1`, o.ID, o.Status, o.PaymentID); err != nil {
			return err
		}
		_, err := q.Exec(ctx, `UPDATE carts SET status = $2 WHERE id = $1`, c.ID, cartCheckedOut)
		return err
	})
	if err != nil {
		httpx.Fail(w, r, http.StatusInternalServerError, err)
		return
	}
	slog.InfoContext(ctx, "order placed", "order.id", o.ID, "payment.id", o.PaymentID, "total_cents", o.TotalCents)
	httpx.WriteJSON(w, http.StatusCreated, o)
}

func failCart(w http.ResponseWriter, r *http.Request, err error) {
	if errors.Is(err, errNotFound) {
		httpx.Fail(w, r, http.StatusNotFound, err)
		return
	}
	httpx.Fail(w, r, http.StatusInternalServerError, err)
}
