// Command payments is the demo payments service. It records payments for
// orders in Postgres. There is no real payment provider: every valid request
// is captured.
package main

import (
	"context"
	"net/http"

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
	for _, stmt := range schema {
		if _, err := db.Exec(ctx, stmt); err != nil {
			db.Close()
			return nil, nil, err
		}
	}
	s := &server{db: db}
	mux := httpx.NewMux(hook)
	mux.HandleFunc("POST /payments", s.createPayment)
	mux.HandleFunc("GET /payments/{id}", s.getPayment)
	return mux.Handler(), db.Close, nil
}
