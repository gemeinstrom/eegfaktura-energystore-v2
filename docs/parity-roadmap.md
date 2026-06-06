# energystore-v2 Functional Parity Roadmap

Tracking document for replacing v1 (`eegfaktura-energystore`) completely.
v2 must be a **drop-in replacement** — no v1 endpoint, calculation, or
behaviour may be missing once this roadmap is complete.

Source-of-truth for "what v1 does":
[`gemeinstrom/eegfaktura-energystore`](https://github.com/gemeinstrom/eegfaktura-energystore)
(forked from the upstream `eegfaktura/eegfaktura-energystore`).

## Status legend

- ✅ shipped (merged to main)
- 🟢 PR open
- 🟡 in flight on a feature branch
- ⚪ not yet started

## Workstreams

### Foundation (already in flight)

| # | Workstream | Status | Notes |
|---|---|---|---|
| 1 | Schema (Hypertable + Compression + CAs) | ✅ | `0001_init.sql` |
| 2 | `UpsertSlots` hot path + `QueryRange` | 🟢 | PR #6 |
| 3 | Embedded migration runner (`migrate` subcommand) | 🟢 | PR #6 |
| 4 | MQTT-5 Shared-Subscription subscriber | ✅ | Skeleton |
| 5 | v1-binary-compatible MQTT decoder | 🟢 | PR #7 |
| 6 | First REST reads (`/range`, `/last-record-date`) | 🟢 | PR #7 |

### Parity blockers (must complete before v2 can replace v1)

| # | Workstream | LoC v1 | Status | Notes |
|---|---|---|---|---|
| A | **Auth middleware** (OIDC + JWT verify + tenant header) | ~700 | ⚪ | `middleware/`; v1 uses go-oidc + Keycloak client-cred bootstrap. Port verbatim modulo logger choice. |
| B | **Counterpoint metadata** layer | ~250 | ⚪ | populate + read `counterpoint_meta`. Source = XLSX-Import. v1: BadgerDB bucket `metadata`. |
| C | **Counterpoint XLSX import** + GraphQL `singleUpload` | ~180 | ⚪ | `excel/import.go` + GraphQL mutation. Brings master-data into v2. |
| D | **Inverter MQTT importer** | ~200 | ⚪ | Second handler `NewMqttInverterImporter` in v1; topic tree distinct from energy. |
| E | **Calculation layer port** | ~1800 | ⚪ | `calculation/`: `AllocateLine`, `EnergyFunctions`, `EEGCalculationV2`, `energy.go`. Algorithms are storage-agnostic; rewrite only the data-loader against `energy_data` + `counterpoint_meta`. |
| F | **Query-engine port** (long-schema replacement of v1 `store/`) | ~470 | ⚪ | `store/{aggregate,default,intraday,loadcurve,raw,summary}_function.go` → SQL against TimescaleDB CA + base table. |
| G | **REST v1-parity endpoints** | ~470 | ⚪ | All 12 `/eeg/...` routes: report (legacy + v2), intra-day, summary, combined, load-curve, raw, meta, excel export+download, lastRecordDate. |
| H | **Excel export layer** | ~1700 | ⚪ | `excel/`: EnergyExport, EnergySheet, SummarySheet, QoVSheet. Pure formatting on top of calc results; ports cleanly with `xuri/excelize/v2`. |
| I | **GraphQL endpoint** (gqlgen) | ~57 hand + generated | ⚪ | Schema is tiny: `lastEnergyDate`, `report`, `singleUpload`. Switch resolvers to v2 store + calc layer. |
| J | **Structured logging** (slog) | — | ⚪ | Replace `log.Printf` and ad-hoc fmt-prints throughout. v1 uses logrus + glog mixed. v2: stdlib slog. |
| K | **Prometheus metrics** endpoint | — | ⚪ | Counters: messages_total, decode_errors, upsert_latency_seconds, upsert_batch_size. Histogram for query latency per endpoint. |
| L | **MQTT health** in `/readyz` | — | ⚪ | Expose paho-client connection state. Currently only DB ping is checked. |
| M | **Dead-letter queue** | — | ⚪ | Failed decodes/upserts: insert into `mqtt_dlq` table with full payload + error + topic + timestamp. Replay job stub. |
| N | **CORS** + standard middlewares | — | ⚪ | v1 uses `gorilla/handlers`; v2 will get equivalent on `net/http`. |

### Out of explicit scope (not v1 features)

These are *not* required for parity but worth noting so we don't lose them:

- Tenant-sharding readiness (multi-cluster routing) — Wachstumspfad, ADR-0010 §5
- Custom HPA metric (`mqtt_lag_seconds` via Prometheus Adapter)
- Source-link footer (AGPL §13) — applies to frontend, not energystore
- Snyk-MCP integration — IaC level

## Coordination

Each workstream is a single GitHub issue plus one or more PRs. Stacked PRs
where possible; foundation pieces land first. The dependency edges that
matter most:

```
Schema (0001_init)
   └── UpsertSlots (#6)
         └── MQTT decoder (#7)
               ├── Dead-letter (M)
               └── REST reads (G — depends on calc F+E)

Auth (A) ───────────────────► all REST endpoints (G)
                              └── GraphQL (I)
Counterpoint meta (B) ───┐
Counterpoint XLSX (C) ───┴── Calculation port (E)
                              └── Query engine (F)
                                    └── REST endpoints (G)
                                          └── Excel export (H)
                                          └── GraphQL (I)
```

## Acceptance: when is parity reached

The cut-over criterion for retiring v1 in any cluster:

1. All v1 REST endpoints respond with byte-identical payload shape for the
   same input (modulo timestamps and ordering of equivalent-value rows).
2. v1 GraphQL queries return identical shapes on identical input data.
3. Calculation parity: golden-file regression suite (run v1 + v2 against
   identical seed data, diff outputs to zero numeric delta within 1e-9).
4. XLSX-import roundtrip parity: same source XLSX → same `counterpoint_meta`
   rows.
5. Per-pod auth identical: same tokens accepted/rejected.
6. Observability: `/metrics` scrapes cleanly; `/healthz` + `/readyz` honest.

Until then v2 may coexist with v1 (current pilot overlay does that), but
v2 must not be advertised as a replacement.
