-- Change events posted to the changes API. Format and design in
-- docs/adr/0005-change-events.md; the request body is checked against
-- changes/schema/change.schema.json before it gets here.
--
-- Runs on every `make up`, so every statement must be safe to repeat.
-- IF NOT EXISTS does not update a table that already exists: a later change
-- to the columns needs its own ALTER statement in a new file.

CREATE DATABASE IF NOT EXISTS incidentlens;

CREATE TABLE IF NOT EXISTS incidentlens.changes
(
    id          String,
    time        DateTime64(3, 'UTC'),
    received_at DateTime64(3, 'UTC'),
    kind        Enum8('deploy' = 1, 'config' = 2, 'flag' = 3, 'note' = 4),
    environment LowCardinality(String),
    service     LowCardinality(String),
    description String,
    attributes  Map(LowCardinality(String), String),
    source      LowCardinality(String),
    INDEX idx_id id TYPE bloom_filter(0.001) GRANULARITY 1
)
-- Rows with the same (environment, time, id) are merged in the background, so
-- a duplicate insert of the same event does not stay. Until a merge happens
-- both rows are visible: read with FINAL.
ENGINE = ReplacingMergeTree
PARTITION BY toYYYYMM(time)
ORDER BY (environment, time, id)
TTL toDateTime(time) + INTERVAL 90 DAY;
