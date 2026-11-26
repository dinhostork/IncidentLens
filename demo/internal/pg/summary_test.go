package pg

import "testing"

func TestSummarize(t *testing.T) {
	tests := []struct {
		sql, op, coll string
	}{
		{"SELECT id, name FROM products WHERE id = $1", "SELECT", "products"},
		{"select count(*) from cart_items", "SELECT", "cart_items"},
		{"INSERT INTO orders (id, cart_id) VALUES ($1, $2)", "INSERT", "orders"},
		{"INSERT INTO payments(id) VALUES ($1)", "INSERT", "payments"},
		{"UPDATE carts SET status = 'checked_out' WHERE id = $1", "UPDATE", "carts"},
		{"DELETE FROM cart_items WHERE cart_id = $1", "DELETE", "cart_items"},
		{"CREATE TABLE IF NOT EXISTS carts (\n id uuid)", "CREATE", "carts"},
		{"  begin", "BEGIN", ""},
		{"SELECT 1", "SELECT", ""},
		{"", "", ""},
	}
	for _, tt := range tests {
		op, coll := summarize(tt.sql)
		if op != tt.op || coll != tt.coll {
			t.Errorf("summarize(%q) = %q, %q; want %q, %q", tt.sql, op, coll, tt.op, tt.coll)
		}
	}
}
