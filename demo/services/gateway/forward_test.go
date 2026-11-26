package main

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/dinhostork/IncidentLens/demo/internal/fault"
	"github.com/dinhostork/IncidentLens/demo/internal/httpx"
)

// backend records the last request it got and answers with a fixed status
// and body.
type backend struct {
	status int
	body   string
	got    string
	gotIn  string
}

func (b *backend) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	in, _ := io.ReadAll(r.Body)
	b.got, b.gotIn = r.Method+" "+r.URL.Path, string(in)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(b.status)
	_, _ = io.WriteString(w, b.body)
}

func TestRoutes(t *testing.T) {
	tests := []struct {
		name, method, path, body string
		backendStatus            int
		wantBackend              string
		wantStatus               int
	}{
		{"list products", "GET", "/api/products", "", 200, "GET /products", 200},
		{"one product", "GET", "/api/products/12", "", 200, "GET /products/12", 200},
		{"unknown product", "GET", "/api/products/999", "", 404, "GET /products/999", 404},
		{"create cart", "POST", "/api/carts", "", 201, "POST /carts", 201},
		{"add item", "POST", "/api/carts/abc/items", `{"product_id":1,"quantity":2}`, 200, "POST /carts/abc/items", 200},
		{"checkout fails", "POST", "/api/carts/abc/checkout", "", 503, "POST /carts/abc/checkout", 502},
		{"invalid body", "POST", "/api/carts/abc/items", `{`, 200, "", 400},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			b := &backend{status: tt.backendStatus, body: `{"ok":true}`}
			srv := httptest.NewServer(b)
			defer srv.Close()
			c := httpx.NewClient("test", srv.URL, time.Second, fault.Nop{})
			h := routes(fault.Nop{}, c, c).Handler()

			r := httptest.NewRequest(tt.method, tt.path, strings.NewReader(tt.body))
			w := httptest.NewRecorder()
			h.ServeHTTP(w, r)

			if w.Code != tt.wantStatus {
				t.Errorf("status = %d, want %d (body %s)", w.Code, tt.wantStatus, w.Body)
			}
			if b.got != tt.wantBackend {
				t.Errorf("backend got %q, want %q", b.got, tt.wantBackend)
			}
			if tt.body != "" && tt.wantBackend != "" && b.gotIn != tt.body {
				t.Errorf("backend body = %q, want %q", b.gotIn, tt.body)
			}
		})
	}
}
