package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/dinhostork/IncidentLens/demo/internal/httpx"
	"github.com/dinhostork/IncidentLens/demo/internal/pg"
)

// checkoutTimeout bounds a checkout once it has started. It is longer than
// the gateway's timeout on purpose: see checkout.
const checkoutTimeout = 8 * time.Second

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
		httpx.ServerError(w, r, http.StatusInternalServerError, "database query failed", err)
		return
	}
	httpx.WriteJSON(w, http.StatusCreated, c)
}

func (s *server) getCart(w http.ResponseWriter, r *http.Request) {
	id, err := parseID(r.PathValue("id"))
	if err != nil {
		failCart(w, r, err)
		return
	}
	c, err := loadCart(r.Context(), s.db, id)
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
	ctx := r.Context()
	id, err := parseID(r.PathValue("id"))
	if err != nil {
		failCart(w, r, err)
		return
	}
	var req addItemRequest
	if err := httpx.ReadJSON(w, r, &req); err != nil {
		httpx.Fail(w, r, http.StatusBadRequest, err)
		return
	}
	if req.ProductID <= 0 || req.Quantity < 1 || req.Quantity > maxQuantity {
		httpx.Fail(w, r, http.StatusBadRequest,
			fmt.Errorf("product_id must be positive and quantity between 1 and %d", maxQuantity))
		return
	}

	var p product
	if err := s.catalog.Do(ctx, http.MethodGet, fmt.Sprintf("/products/%d", req.ProductID), nil, &p); err != nil {
		switch code := httpx.Upstream(err); {
		case code == http.StatusNotFound:
			httpx.Fail(w, r, code, fmt.Errorf("product %d not found", req.ProductID))
		case code < 500:
			httpx.Fail(w, r, code, err)
		default:
			httpx.ServerError(w, r, code, "call to catalog failed", err)
		}
		return
	}

	// Only insert into an open cart, in the same statement, so a checkout
	// that starts in between cannot end up with items added after it.
	tag, err := s.db.Exec(ctx,
		`INSERT INTO cart_items (cart_id, product_id, quantity, unit_price_cents)
		 SELECT $1, $2::bigint, $3::int, $4::bigint
		 WHERE EXISTS (SELECT 1 FROM carts WHERE id = $1 AND status = 'open')
		 ON CONFLICT (cart_id, product_id)
		 DO UPDATE SET quantity = LEAST(cart_items.quantity + EXCLUDED.quantity, $5), unit_price_cents = EXCLUDED.unit_price_cents`,
		id, p.ID, req.Quantity, p.PriceCents, maxQuantity)
	if err != nil {
		httpx.ServerError(w, r, http.StatusInternalServerError, "database query failed", err)
		return
	}
	if tag.RowsAffected() == 0 {
		s.notOpen(w, r, id)
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

// checkout claims the cart, creates a pending order, charges it through
// payments and then marks the order paid and the cart checked out.
//
// Once the cart is claimed the checkout runs on a context detached from the
// request, with its own deadline. If the caller gives up halfway, payments may
// already have charged, and stopping there would leave order, payment and
// cart disagreeing.
//
// If payments answers with an error the charge did not happen: the order is
// marked payment_failed and the cart reopened. If it does not answer
// (timeout, connection error) the outcome is unknown, so the order stays
// pending and the cart checking_out.
func (s *server) checkout(w http.ResponseWriter, r *http.Request) {
	id, err := parseID(r.PathValue("id"))
	if err != nil {
		failCart(w, r, err)
		return
	}
	tag, err := s.db.Exec(r.Context(),
		`UPDATE carts SET status = $2 WHERE id = $1 AND status = $3`, id, cartCheckingOut, cartOpen)
	if err != nil {
		httpx.ServerError(w, r, http.StatusInternalServerError, "database query failed", err)
		return
	}
	if tag.RowsAffected() == 0 {
		s.notOpen(w, r, id)
		return
	}

	ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), checkoutTimeout)
	defer cancel()

	items, err := cartItems(ctx, s.db, id)
	if err != nil {
		s.reopen(ctx, id)
		httpx.ServerError(w, r, http.StatusInternalServerError, "database query failed", err)
		return
	}
	if len(items) == 0 {
		s.reopen(ctx, id)
		httpx.Fail(w, r, http.StatusBadRequest, errors.New("cart is empty"))
		return
	}

	o := order{CartID: id, TotalCents: total(items), Status: "pending"}
	err = s.db.QueryRow(ctx,
		`INSERT INTO orders (cart_id, total_cents, status) VALUES ($1, $2, $3) RETURNING id`,
		o.CartID, o.TotalCents, o.Status).Scan(&o.ID)
	if err != nil {
		s.reopen(ctx, id)
		httpx.ServerError(w, r, http.StatusInternalServerError, "database query failed", err)
		return
	}

	var pay paymentResponse
	err = s.payments.Do(ctx, http.MethodPost, "/payments",
		paymentRequest{OrderID: o.ID, AmountCents: o.TotalCents, Currency: "USD"}, &pay)
	if err != nil {
		var se *httpx.StatusError
		if errors.As(err, &se) {
			s.paymentFailed(ctx, o.ID, id)
		}
		if code := httpx.Upstream(err); code < 500 {
			httpx.Fail(w, r, code, errors.New("payment rejected"))
		} else {
			httpx.ServerError(w, r, code, "call to payments failed", err)
		}
		return
	}

	o.Status, o.PaymentID = "paid", pay.ID
	err = s.db.InTx(ctx, func(q pg.Querier) error {
		if _, err := q.Exec(ctx, `UPDATE orders SET status = $2, payment_id = $3 WHERE id = $1`, o.ID, o.Status, o.PaymentID); err != nil {
			return err
		}
		_, err := q.Exec(ctx, `UPDATE carts SET status = $2 WHERE id = $1`, id, cartCheckedOut)
		return err
	})
	if err != nil {
		// Charged but not recorded as paid: the order stays pending with the
		// payment findable by order_id in payments.
		httpx.ServerError(w, r, http.StatusInternalServerError, "database query failed", err)
		return
	}
	slog.InfoContext(ctx, "order placed", "order.id", o.ID, "payment.id", o.PaymentID, "total_cents", o.TotalCents)
	httpx.WriteJSON(w, http.StatusCreated, o)
}

// reopen releases a claimed cart after a checkout failed before payments was
// called.
func (s *server) reopen(ctx context.Context, cartID string) {
	_, err := s.db.Exec(ctx, `UPDATE carts SET status = $2 WHERE id = $1 AND status = $3`, cartID, cartOpen, cartCheckingOut)
	if err != nil {
		slog.ErrorContext(ctx, "could not reopen cart", "cart.id", cartID, "error", err.Error())
	}
}

// paymentFailed records a charge that payments refused and reopens the cart.
func (s *server) paymentFailed(ctx context.Context, orderID, cartID string) {
	err := s.db.InTx(ctx, func(q pg.Querier) error {
		if _, err := q.Exec(ctx, `UPDATE orders SET status = 'payment_failed' WHERE id = $1`, orderID); err != nil {
			return err
		}
		_, err := q.Exec(ctx, `UPDATE carts SET status = $2 WHERE id = $1 AND status = $3`, cartID, cartOpen, cartCheckingOut)
		return err
	})
	if err != nil {
		slog.ErrorContext(ctx, "could not record failed payment", "order.id", orderID, "error", err.Error())
	}
}

// notOpen answers a write to a cart that is missing or not open.
func (s *server) notOpen(w http.ResponseWriter, r *http.Request, id string) {
	if _, err := cartStatus(r.Context(), s.db, id); err != nil {
		failCart(w, r, err)
		return
	}
	httpx.Fail(w, r, http.StatusConflict, errors.New("cart is not open"))
}

func failCart(w http.ResponseWriter, r *http.Request, err error) {
	if errors.Is(err, errNotFound) {
		httpx.Fail(w, r, http.StatusNotFound, err)
		return
	}
	httpx.ServerError(w, r, http.StatusInternalServerError, "database query failed", err)
}
