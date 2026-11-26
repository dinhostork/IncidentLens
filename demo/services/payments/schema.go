package main

const schema = `
CREATE TABLE IF NOT EXISTS payments (
	id           uuid PRIMARY KEY DEFAULT gen_random_uuid(),
	order_id     uuid NOT NULL UNIQUE,
	amount_cents bigint NOT NULL CHECK (amount_cents > 0),
	currency     text NOT NULL,
	status       text NOT NULL,
	created_at   timestamptz NOT NULL DEFAULT now()
);
`
