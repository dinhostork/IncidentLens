// Command gateway is the entry point of the demo system. It exposes the shop
// API and forwards each request to catalog or orders.
package main

import (
	"context"
	"net/http"
	"time"

	"github.com/dinhostork/IncidentLens/demo/internal/fault"
	"github.com/dinhostork/IncidentLens/demo/internal/httpx"
	"github.com/dinhostork/IncidentLens/demo/internal/service"
)

func main() {
	service.Main(setup)
}

func setup(_ context.Context, hook fault.Hook) (http.Handler, func(), error) {
	catalog := httpx.NewClient("catalog", service.Env("CATALOG_URL", "http://catalog:8080"), 2*time.Second, hook)
	orders := httpx.NewClient("orders", service.Env("ORDERS_URL", "http://orders:8080"), 5*time.Second, hook)
	return routes(hook, catalog, orders).Handler(), func() {}, nil
}

func routes(hook fault.Hook, catalog, orders *httpx.Client) *httpx.Mux {
	mux := httpx.NewMux(hook)
	mux.HandleFunc("GET /api/products", forward(catalog, http.MethodGet, "/products", http.StatusOK))
	mux.HandleFunc("GET /api/products/{id}", forward(catalog, http.MethodGet, "/products/{id}", http.StatusOK))
	mux.HandleFunc("POST /api/carts", forward(orders, http.MethodPost, "/carts", http.StatusCreated))
	mux.HandleFunc("GET /api/carts/{id}", forward(orders, http.MethodGet, "/carts/{id}", http.StatusOK))
	mux.HandleFunc("POST /api/carts/{id}/items", forward(orders, http.MethodPost, "/carts/{id}/items", http.StatusOK))
	mux.HandleFunc("POST /api/carts/{id}/checkout", forward(orders, http.MethodPost, "/carts/{id}/checkout", http.StatusCreated))
	return mux
}
