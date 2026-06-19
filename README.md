# eegfaktura-energystore-v2

Time-series storage and ingest service for the eegfaktura platform.

This component is a re-implementation of [eegfaktura-energystore](https://github.com/gemeinstrom/eegfaktura-energystore)
(v1, BadgerDB-backed). v2 moves the storage layer to **TimescaleDB**, becomes
**stateless** and **multi-replica** capable, and replaces the full-range
Read-Modify-Write ingest pattern with a targeted `INSERT ... ON CONFLICT DO
UPDATE` on the actually delivered cells.

The canonical decision record is
[ADR-0010](https://github.com/gemeinstrom/eegfaktura-platform/blob/main/docs/adr/0010-energystore-v2-timescaledb.md)
with the underlying analysis in
[energystore-scaling-concept](https://github.com/gemeinstrom/eegfaktura-platform/blob/main/docs/energystore-scaling-concept.md);
the sections below summarise the essentials so this repo reads stand-alone.


## Why v2 exists

The v1 energystore peaks at **20+ GiB RAM and 12–20 CPU cores** during large EDA
ingest bursts. Code analysis identified three reinforcing root causes that
cannot be tuned away within the v1 architecture:

1. **Embedded BadgerDB** holds a filesystem lock on its data directory.
   `replicas: 1` + `ReadWriteOnce` PVC are not a deployment quirk — they are
   the only legal configuration for this storage model.
2. **Full-range Read-Modify-Write per message.** Every inbound EDA message
   reads the entire delivery time-range into a Go map, merges the new values,
   and writes the whole map back. For a 1000-member EEG with a year of
   15-minute slots this is on the order of 1.4 GiB of allocations per message,
   plus GC pressure.
3. **Per-tenant in-process mutex.** A global `Turns.lock(tenant)` serialises
   every EEG of a tenant, blocking any concurrency the bus or DB could give.

v2 replaces all three: stateless app pods, an external time-series DB, and
per-message-bound UPSERTs on the actually delivered cells only.


## Why TimescaleDB

Three TimescaleDB features are load-bearing for this workload:

- **Native chunk compression** — 5–10× storage reduction (the v1 BadgerDB
  store is ~950 GiB; the same content compressed in Timescale is projected at
  80–150 GiB).
- **Continuous aggregates with invalidation tracking** — daily and monthly
  rollups stay incrementally fresh under late-arriving back-fills.
- **Auto-chunking** the hypertable on time + tenant — parallel scans on read,
  automatic retention on write.

Alternatives considered and rejected:

| Alternative | Rejection in one line |
|---|---|
| Keep BadgerDB (sharding, pinning, custom Raft wrapper) | Multi-replica still impossible; the RAM-bomb algorithm stays |
| Plain Postgres + `pg_partman` + materialized views | ~5× storage (no compression) and durable maintenance burden of partition + MV jobs |
| ClickHouse | `ReplacingMergeTree` updates are eventually consistent — incompatible with EDA back-fills; weak joins to master data |
| CockroachDB / YugabyteDB | TimescaleDB extension does not work; a storage-engine swap with no upside for this workload |

License: Timescale core is Apache-2, compression + continuous aggregates are
under the Timescale License (TSL). TSL is free to use and compatible with our
AGPL stack; the only restriction is reselling TimescaleDB-as-a-Service, which
is not in scope.


## Status

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


## Operational trade-offs

- **Two components during the transition.** v1 and v2 coexist in the
  cluster; the switch is per cluster via the GitOps overlay (Helm values or
  Kustomize patch, depending on how the consuming platform packages its
  manifests), and a rollback is the same GitOps revert back to v1.
- **A second Postgres cluster.** `postgres-energy` is separate from the main
  `postgres` (master data, billing, auth). Workload isolation, backup
  isolation, and a clean TimescaleDB-extension install are the reasons; the
  cost is one more cluster to size, monitor, and back up.
- **Compressed back-fills are slower.** Re-requesting data older than the
  compression threshold (~30 days) is functionally fine but pays a decompress
  cost on the affected chunks.
- **Greenfield cut-over loses energystore history that EDA cannot replay.**
  A batch ETL from v1 BadgerDB exists in `internal/migrate/` as a backup
  path when that history matters.
- **Tenant migration is DELETE+INSERT.** TimescaleDB hash-partitions by
  `tenant_id` and rejects updates that change a partition key — moving a
  meter between tenants is a delete plus a fresh insert, not an update.
- **Multi-writer scaling is explicitly out of scope.** Vertical scaling of
  the primary is the primary lever (an 8–16-core / 32–64-GiB NVMe primary
  realistically takes 100k+ UPSERTs/s); reads scale via Streaming-Replication
  to standby replicas. Tenant-sharding across multiple Postgres clusters is
  the growth path if and when the primary saturates.


## License

[AGPL-3.0](LICENSE). See [NOTICE](NOTICE) for lineage and §13 obligations.
