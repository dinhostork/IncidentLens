// Command orders is the demo orders service. It keeps carts and orders in
// Postgres, looks up prices in catalog and charges through payments.
package main

import (
	"context"
	"net/http"
	"time"

	"github.com/dinhostork/IncidentLens/demo/internal/fault"
	"github.com/dinhostork/IncidentLens/demo/internal/httpx"
	"github.com/dinhostork/IncidentLens/demo/internal/pg"
	"github.com/dinhostork/IncidentLens/demo/internal/service"
)

func main() {
	service.Main(setup)
}

func setup(ctx context.Context, hook fault.Hook) (http.Handler, func(), error) {
	dsn, err := service.RequireEnv("DATABASE_URL")
	if err != nil {
		return nil, nil, err
	}
	db, err := pg.Open(ctx, dsn, hook)
	if err != nil {
		return nil, nil, err
	}
	if _, err := db.Exec(ctx, schema); err != nil {
		db.Close()
		return nil, nil, err
	}
	s := &server{
		db:       db,
		catalog:  httpx.NewClient("catalog", service.Env("CATALOG_URL", "http://catalog:8080"), 2*time.Second, hook),
		payments: httpx.NewClient("payments", service.Env("PAYMENTS_URL", "http://payments:8080"), 3*time.Second, hook),
	}
	mux := httpx.NewMux(hook)
	mux.HandleFunc("POST /carts", s.createCart)
	mux.HandleFunc("GET /carts/{id}", s.getCart)
	mux.HandleFunc("POST /carts/{id}/items", s.addItem)
	mux.HandleFunc("POST /carts/{id}/checkout", s.checkout)
	return mux.Handler(), db.Close, nil
}
