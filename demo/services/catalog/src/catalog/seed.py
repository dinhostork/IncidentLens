"""Product schema and seed data. Applied on every start, idempotently."""

SCHEMA = """
CREATE TABLE IF NOT EXISTS products (
    id          bigint PRIMARY KEY,
    name        text NOT NULL,
    category    text NOT NULL,
    price_cents bigint NOT NULL CHECK (price_cents > 0)
)
"""

INSERT = """
INSERT INTO products (id, name, category, price_cents) VALUES ($1, $2, $3, $4)
ON CONFLICT (id) DO NOTHING
"""

_CATEGORIES = {
    "kitchen": ["kettle", "pan", "knife", "grinder", "teapot", "colander"],
    "office": ["notebook", "desk lamp", "stapler", "monitor stand", "pen set", "chair mat"],
    "outdoor": ["tent", "headlamp", "water bottle", "camp stove", "hammock", "daypack"],
    "garden": ["trowel", "hose", "planter", "pruner", "watering can", "seed tray"],
}
_ADJECTIVES = ["basic", "classic"]


def products() -> list[tuple[int, str, str, int]]:
    """48 products with stable IDs 1..48 and deterministic prices."""
    rows = []
    for category, nouns in _CATEGORIES.items():
        for adjective in _ADJECTIVES:
            for noun in nouns:
                pid = len(rows) + 1
                price = 500 + (pid * 1373) % 9000
                rows.append((pid, f"{adjective.title()} {noun}", category, price))
    return rows
