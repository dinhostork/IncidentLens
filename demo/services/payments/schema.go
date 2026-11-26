package main

// schema is applied statement by statement on startup, so each statement
// gets its own span.
var schema = []string{
	`CREATE TABLE IF NOT EXISTS payments (
	id           uuid PRIMARY KEY DEFAULT gen_random_uuid(),
	order_id     uuid NOT NULL UNIQUE,
	amount_cents bigint NOT NULL CHECK (amount_cents > 0),
	currency     text NOT NULL,
	status       text NOT NULL,
	created_at   timestamptz NOT NULL DEFAULT now()
)`,
}
