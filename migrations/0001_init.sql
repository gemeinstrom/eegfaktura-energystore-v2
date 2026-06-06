-- energystore-v2 Initial Schema
--
-- Long-Schema: eine Row pro (tenant, ec_id, metering_point, meter_code, ts).
-- Ersetzt das Wide-Array-Schema aus eegfaktura-energystore v1 (BadgerDB),
-- damit Read-Modify-Write des gesamten Zeitbereichs entfaellt — gezielter
-- UPSERT pro geliefertem Slot.
--
-- Voraussetzung: TimescaleDB-Extension in der Postgres-Instanz installiert.

BEGIN;

CREATE EXTENSION IF NOT EXISTS timescaledb;

-- ---------------------------------------------------------------------------
-- energy_data: Zeitreihen-Tabelle fuer 15-Minuten-Lastgang-Daten.
-- ---------------------------------------------------------------------------

CREATE TABLE IF NOT EXISTS energy_data (
    tenant_id       TEXT             NOT NULL,
    ec_id           TEXT             NOT NULL,
    metering_point  TEXT             NOT NULL,
    meter_code      TEXT             NOT NULL,  -- OBIS-Code, z.B. G.01, G.02, G.03, P.01
    ts              TIMESTAMPTZ      NOT NULL,
    value           DOUBLE PRECISION NOT NULL,
    qov             SMALLINT         NOT NULL,  -- Quality-of-Value: 0=raw, 1=replaced, 2=interpolated, ...
    PRIMARY KEY (tenant_id, ec_id, metering_point, meter_code, ts)
);

-- Hypertable nach Zeit, mit Tenant als Space-Partition.
-- 7-Tage-Chunks: Balance zwischen Compression-Ratio und Chunk-Anzahl.
-- 16 Tenant-Partitions: ueberprovisioniert fuer 500 EEGs, hash-stabil.
SELECT create_hypertable(
    'energy_data', 'ts',
    chunk_time_interval => INTERVAL '7 days',
    partitioning_column => 'tenant_id',
    number_partitions   => 16,
    if_not_exists       => TRUE
);

-- Compression: spaltenbasiert pro Chunk, segmentiert nach den
-- Such-Dimensionen, sortiert nach ts (descending fuer hot-data-first reads).
ALTER TABLE energy_data SET (
    timescaledb.compress,
    timescaledb.compress_segmentby = 'tenant_id, ec_id, metering_point, meter_code',
    timescaledb.compress_orderby   = 'ts DESC'
);

-- Auto-Compression: Chunks aelter als 30 Tage werden komprimiert.
-- 30 Tage decken den ueblichen Nachforderungs-Zeitraum (Abrechnungs-Lauf)
-- ab; aeltere Korrekturen koennen die Dekompressions-Kosten tragen.
SELECT add_compression_policy(
    'energy_data', INTERVAL '30 days',
    if_not_exists => TRUE
);

-- ---------------------------------------------------------------------------
-- counterpoint_meta: pro Zaehlpunkt Stamm-Metadaten (Dir, Periode, Idx).
-- Ersetzt das BadgerDB-Bucket "metadata" aus v1. Klein, kein Hypertable.
-- ---------------------------------------------------------------------------

CREATE TABLE IF NOT EXISTS counterpoint_meta (
    tenant_id       TEXT NOT NULL,
    ec_id           TEXT NOT NULL,
    metering_point  TEXT NOT NULL,
    direction       SMALLINT NOT NULL,           -- 1=consumer, 2=producer
    source_idx      INTEGER NOT NULL,            -- Position innerhalb der Direction
    period_start    TIMESTAMPTZ,
    period_end      TIMESTAMPTZ,
    payload         JSONB,                       -- Erweiterungs-Felder, bei Bedarf
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (tenant_id, ec_id, metering_point)
);

CREATE INDEX IF NOT EXISTS counterpoint_meta_tenant_ec_dir_idx
    ON counterpoint_meta (tenant_id, ec_id, direction, source_idx);

-- ---------------------------------------------------------------------------
-- Continuous Aggregates: Stunden- und Tagessummen.
--
-- Ziel: Chart-Reads und Billing-Aggregate gegen die CA fragen, nicht
-- gegen energy_data direkt. Real-time Aggregation (refresh-on-read) ist
-- moeglich; hier explizit als "materialized only" angelegt, damit der
-- Lese-Pfad zuverlaessig schnell ist und das materialize-Schedule der
-- Refresh-Policy ueberlassen wird.
-- ---------------------------------------------------------------------------

CREATE MATERIALIZED VIEW IF NOT EXISTS energy_hourly
WITH (timescaledb.continuous) AS
SELECT
    tenant_id,
    ec_id,
    metering_point,
    meter_code,
    time_bucket('1 hour', ts) AS bucket,
    SUM(value)                AS sum_value,
    COUNT(*)                  AS slot_count,
    MIN(qov)                  AS min_qov
FROM energy_data
GROUP BY tenant_id, ec_id, metering_point, meter_code, bucket
WITH NO DATA;

SELECT add_continuous_aggregate_policy(
    'energy_hourly',
    start_offset      => INTERVAL '7 days',
    end_offset        => INTERVAL '1 hour',
    schedule_interval => INTERVAL '15 minutes',
    if_not_exists     => TRUE
);

CREATE MATERIALIZED VIEW IF NOT EXISTS energy_daily
WITH (timescaledb.continuous) AS
SELECT
    tenant_id,
    ec_id,
    metering_point,
    meter_code,
    time_bucket('1 day', ts) AS bucket,
    SUM(value)               AS sum_value,
    COUNT(*)                 AS slot_count,
    MIN(qov)                 AS min_qov
FROM energy_data
GROUP BY tenant_id, ec_id, metering_point, meter_code, bucket
WITH NO DATA;

SELECT add_continuous_aggregate_policy(
    'energy_daily',
    start_offset      => INTERVAL '30 days',
    end_offset        => INTERVAL '1 day',
    schedule_interval => INTERVAL '1 hour',
    if_not_exists     => TRUE
);

-- Monatssummen werden aus energy_daily aggregiert (kein CA noetig:
-- Lese-Anfrage liest 28-31 daily-Rows pro Metering-Point + Code).

COMMIT;
