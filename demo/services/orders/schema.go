package main

const schema = `
CREATE TABLE IF NOT EXISTS carts (
	id         uuid PRIMARY KEY DEFAULT gen_random_uuid(),
	status     text NOT NULL DEFAULT 'open',
	created_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS cart_items (
	cart_id          uuid NOT NULL REFERENCES carts (id),
	product_id       bigint NOT NULL,
	quantity         int NOT NULL CHECK (quantity > 0),
	unit_price_cents bigint NOT NULL,
	PRIMARY KEY (cart_id, product_id)
);

CREATE TABLE IF NOT EXISTS orders (
	id          uuid PRIMARY KEY DEFAULT gen_random_uuid(),
	cart_id     uuid NOT NULL REFERENCES carts (id),
	total_cents bigint NOT NULL,
	status      text NOT NULL,
	payment_id  uuid,
	created_at  timestamptz NOT NULL DEFAULT now()
);
`
