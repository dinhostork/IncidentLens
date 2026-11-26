package main

import (
	"context"
	"errors"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/dinhostork/IncidentLens/demo/internal/pg"
)

const (
	cartOpen       = "open"
	cartCheckedOut = "checked_out"
	maxQuantity    = 10
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

func cartStatus(ctx context.Context, q pg.Querier, id string) (string, error) {
	// A malformed ID is the caller's mistake. Answer it here instead of
	// sending a query that Postgres rejects, which would count as a failed
	// database call.
	if uuid.Validate(id) != nil {
		return "", errNotFound
	}
	var status string
	err := q.QueryRow(ctx, `SELECT status FROM carts WHERE id = $1`, id).Scan(&status)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", errNotFound
	}
	return status, err
}

func loadCart(ctx context.Context, q pg.Querier, id string) (cart, error) {
	status, err := cartStatus(ctx, q, id)
	if err != nil {
		return cart{}, err
	}
	rows, err := q.Query(ctx,
		`SELECT product_id, quantity, unit_price_cents FROM cart_items WHERE cart_id = $1 ORDER BY product_id`, id)
	if err != nil {
		return cart{}, err
	}
	items, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (item, error) {
		var it item
		err := row.Scan(&it.ProductID, &it.Quantity, &it.UnitPriceCents)
		return it, err
	})
	if err != nil {
		return cart{}, err
	}
	return cart{ID: id, Status: status, Items: items, TotalCents: total(items)}, nil
}
