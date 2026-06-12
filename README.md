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

**Live in pilot since 2026-06-06.** The service runs as the active
energy-data backend in the `eeg-pilot` cluster. v1 is held at
`replicas=0` as rollback reserve; both `eegfaktura-energystore` and
`eegfaktura-energystore-v2` k8s services route to the v2 pod via
selector-sharing.

Implemented:

- TimescaleDB store driver (`internal/store/`) with hypertable +
  compression + continuous aggregates from `migrations/0001_init.sql`.
- MQTT 5 consumer (`internal/mqtt/`) with shared-subscription support
  (paho.golang) and dead-letter queue (`dlq_writes_total` Prometheus
  counter).
- REST API handlers (`internal/api/`) at parity with v1's `/api/v1/...`
  surface, plus `/healthz` + `/readyz` + `/metrics`.
- GraphQL API (`internal/graphqlapi/`).
- OIDC/JWT auth middleware (`internal/auth/`) with Keycloak discovery,
  tenant-claim binding, and admin-vs-member protection.
- Structured logging via `log/slog`.
- Counterpoint metadata CRUD (`internal/counterpoint/`).
- Decryption layer for prod-MQTT CR_MSG payload (`internal/decode/`,
  ENV-gated, default-off for Pilot).
- Excel export + XLSX import.
- Migration tooling (`internal/migrate/`) for Plan-B Batch-ETL from v1
  BadgerDB.

Open:

- CORS middleware — currently unused because the customer-web reaches
  v2 same-origin via Caddy/ingress, but required for cross-origin
  clients ([#22](https://github.com/gemeinstrom/eegfaktura-energystore-v2/issues/22)).
- Phase-2: ELWG-Refactor — T/R-Slots, Mehrfachteilnahme, P2P,
  Eigennutzung ([#40](https://github.com/gemeinstrom/eegfaktura-energystore-v2/issues/40)).
- Phase-2: Smart Nachfordern — `GET /eeg/{ecid}/completeness`
  ([#43](https://github.com/gemeinstrom/eegfaktura-energystore-v2/issues/43)).


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
