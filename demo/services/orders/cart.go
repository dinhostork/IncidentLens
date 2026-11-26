package main

import (
	"context"
	"errors"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/dinhostork/IncidentLens/demo/internal/pg"
)

// Cart states. A cart moves open -> checking_out -> checked_out, or back to
// open if the checkout fails before payments could have charged it.
const (
	cartOpen        = "open"
	cartCheckingOut = "checking_out"
	cartCheckedOut  = "checked_out"
	maxQuantity     = 10
)

var errNotFound = errors.New("cart not found")

type cart struct {
	ID         string `json:"id"`
	Status     string `json:"status"`
	Items      []item `json:"items"`
	TotalCents int64  `json:"total_cents"`
}

type item struct {
	ProductID      int64 `json:"product_id"`
	Quantity       int   `json:"quantity"`
	UnitPriceCents int64 `json:"unit_price_cents"`
}

func total(items []item) int64 {
	var t int64
	for _, it := range items {
		t += int64(it.Quantity) * it.UnitPriceCents
	}
	return t
}

// parseID returns the canonical form of a cart ID, or errNotFound if s is not
// a UUID. A malformed ID is the caller's mistake: answering it here avoids a
// query that Postgres would reject and that would count as a failed database
// call. uuid.Parse accepts forms Postgres does not (urn:uuid:...), so the
// canonical string is what gets sent.
func parseID(s string) (string, error) {
	u, err := uuid.Parse(s)
	if err != nil {
		return "", errNotFound
	}
	return u.String(), nil
}

func cartStatus(ctx context.Context, q pg.Querier, id string) (string, error) {
	var status string
	err := q.QueryRow(ctx, `SELECT status FROM carts WHERE id = $1`, id).Scan(&status)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", errNotFound
	}
	return status, err
}

func cartItems(ctx context.Context, q pg.Querier, id string) ([]item, error) {
	rows, err := q.Query(ctx,
		`SELECT product_id, quantity, unit_price_cents FROM cart_items WHERE cart_id = $1 ORDER BY product_id`, id)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (item, error) {
		var it item
		err := row.Scan(&it.ProductID, &it.Quantity, &it.UnitPriceCents)
		return it, err
	})
}

func loadCart(ctx context.Context, q pg.Querier, id string) (cart, error) {
	status, err := cartStatus(ctx, q, id)
	if err != nil {
		return cart{}, err
	}
	items, err := cartItems(ctx, q, id)
	if err != nil {
		return cart{}, err
	}
	return cart{ID: id, Status: status, Items: items, TotalCents: total(items)}, nil
}
