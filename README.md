# eegfaktura-energystore-v2

Time-series storage and ingest service for the eegfaktura platform.

This component is a re-implementation of [eegfaktura-energystore](https://github.com/gemeinstrom/eegfaktura-energystore)
(v1, BadgerDB-backed). v2 moves the storage layer to **TimescaleDB**, becomes
**stateless** and **multi-replica** capable, and replaces the full-range
Read-Modify-Write ingest pattern with a targeted `INSERT ... ON CONFLICT DO
UPDATE` on the actually delivered cells.

See [ADR-0010](https://github.com/gemeinstrom/eegfaktura-platform/blob/main/docs/adr/0010-energystore-v2-timescaledb.md)
and the [energystore-scaling-concept](https://github.com/gemeinstrom/eegfaktura-platform/blob/main/docs/energystore-scaling-concept.md)
for the architectural rationale.


## Status

**Skeleton.** This repo currently contains:

- Long-schema DDL (`migrations/0001_init.sql`) — hypertable, compression,
  continuous aggregates.
- Module layout (`cmd/`, `internal/store`, `internal/mqtt`, `internal/api`,
  `internal/config`).
- Multi-stage distroless Dockerfile.
- GitHub Actions skeleton (build, security scans).

Not yet implemented:

- TimescaleDB store driver.
- MQTT-5 Shared Subscription consumer.
- HTTP/REST API handlers (parity with v1's `/api/v1/...` surface).
- Migration tooling for Plan-B Batch-ETL from v1 BadgerDB.


## Architecture summary

```
MQTT 5 Shared Subs  →  energystore-v2 (N replicas, stateless, HPA)
                              │
                              ▼
                       PgBouncer
                              │
                              ▼
                       postgres-energy (Postgres + TimescaleDB)
                       Primary (writes) + Standby (reads)
```

- Stateless service, scales via Kubernetes HPA.
- Storage: dedicated `postgres-energy` cluster with TimescaleDB extension —
  separate from the main eegfaktura `postgres` (master data, billing, auth).
- Ingest: MQTT 5 Shared Subscriptions distribute messages across pods;
  each pod streams a per-message-bound UPSERT batch.
- Read path: Continuous Aggregates (`energy_hourly`, `energy_daily`) back
  the chart and billing API.


## License

[AGPL-3.0](LICENSE). See [NOTICE](NOTICE) for lineage and §13 obligations.
