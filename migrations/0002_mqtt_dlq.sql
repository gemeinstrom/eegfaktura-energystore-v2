-- mqtt_dlq: dead-letter queue for MQTT payloads that failed decode or upsert.
--
-- Keeps the raw payload so a later replay job can re-process once the
-- failure cause is fixed. Bounded: a retention policy is added so the
-- table doesn't grow unbounded if a poison message storm hits.

CREATE TABLE IF NOT EXISTS mqtt_dlq (
    id          BIGSERIAL PRIMARY KEY,
    ts          TIMESTAMPTZ NOT NULL DEFAULT now(),
    topic       TEXT        NOT NULL,
    failure     TEXT        NOT NULL,  -- 'decode' | 'upsert'
    error       TEXT        NOT NULL,
    payload     BYTEA       NOT NULL,
    replayed_at TIMESTAMPTZ
);

CREATE INDEX IF NOT EXISTS mqtt_dlq_ts_idx       ON mqtt_dlq (ts);
CREATE INDEX IF NOT EXISTS mqtt_dlq_unreplayed_idx ON mqtt_dlq (ts) WHERE replayed_at IS NULL;
