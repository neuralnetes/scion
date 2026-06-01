# PostgreSQL Store Implementation

**Date:** 2026-06-01

## Motivation

The hub needs to run stateless in a hosted/cloud topology where the database is
a GitOps-configured external service (e.g. Cloud SQL, Lakebase, any managed
Postgres). SQLite is process-local and cannot be shared across replicas.
The `database.driver` + `database.url` fields already existed in `GlobalConfig`
to hold a connection URL; what was missing was a `Store` implementation that
consumed them. See `.design/hosted/resource-storage-refactor.md` §1.1
("Cloud / hosted mode — the storage backend is GCS") for the broader hosted
architecture context that motivated a stateless control plane.

## What landed

### `pkg/store/postgres/`

A new package, parallel in shape to `pkg/store/sqlite/`, implementing the full
`store.Store` interface against PostgreSQL.

- **`postgres.go`** — `PostgresStore` struct wrapping `*sql.DB`, `New(connURL
  string)`, `Migrate(ctx)`, `Ping`, `Close`. Connection pool fixed at
  `MaxOpenConns=4` / `MaxIdleConns=4`.
- **`driver.go`** — blank import of `github.com/lib/pq` (database/sql driver
  name `postgres`) guarded by `//go:build !no_postgres`.
- **`migrations.go`** — 53 versioned migrations tracked in a
  `schema_migrations` table (`version INTEGER PRIMARY KEY`). `Migrate` is
  idempotent: it reads `MAX(version)` and skips already-applied steps. Each
  migration runs in its own transaction; a `foreignKeysOffMigrations` map is
  preserved for shape-parity with the SQLite runner (in Postgres, FK deferral
  is handled inside the migration SQL itself via `CASCADE`/explicit FK drops,
  so the function body is a plain transaction).
- **Per-entity files** (`agents.go`, `users.go`, `projects.go`, `secrets.go`,
  `messages.go`, `groups.go`, `policies.go`, `tokens.go`, `invites.go`,
  `brokers.go`, `envvars.go`, `schedule.go`, `scheduled_event.go`,
  `notification.go`, `templates.go`, `harness_configs.go`, `allowlist.go`,
  `brokersecret.go`, `providers.go`, `project_sync_state.go`,
  `gcp_service_account.go`, `github_installation.go`, `maintenance.go`) —
  one file per entity group, matching the sqlite layout.

### `initStore` case in `cmd/server_foreground.go`

`initStore` gained a `"postgres"` branch: `postgres.New(cfg.Database.URL)` →
`pgStore.Migrate` → `entc.OpenPostgres(cfg.Database.URL)` → `entc.AutoMigrate`
→ `entadapter.NewCompositeStore`. The grove→project data backfill
(`entc.MigrateGroveToProjectData`) is **not** called on the postgres path (see
below).

### Dialect translation rules applied throughout

| SQLite pattern | PostgreSQL replacement |
|---|---|
| `?` positional placeholder | `$N` numbered placeholder |
| `INSERT OR IGNORE` / `INSERT OR REPLACE` | `ON CONFLICT … DO NOTHING` / `ON CONFLICT … DO UPDATE SET` |
| `sqlite_master` / `pragma_table_info` | `information_schema.tables` / `information_schema.columns` (both scoped to `table_schema='public'`, queried with `$1`/$`$2` params) |
| `randomblob(16)` | `gen_random_uuid()::text` (pgcrypto built-in; used in several data-backfill migrations) |
| `json_each(…)` | `json_array_elements_text(…::json)` (used in agent ancestry filter) |
| Case-insensitive email uniqueness via `UNIQUE` on TEXT | `CREATE UNIQUE INDEX … ON allow_list (LOWER(email))` (functional unique index) |
| `BLOB` | `BYTEA` (broker secret key column) |

## What was deliberately skipped

**Grove→project data backfill** (`entc.MigrateGroveToProjectData`) is omitted
from the postgres `initStore` path. A fresh postgres database starts with the
post-rename schema (V50 renames `groves` → `projects` and all `grove_id`
columns → `project_id` in-place); there is no legacy ent sqlite data to
backfill. The backfill only applies to existing SQLite deployments upgrading
in-place.

## How it is tested

`pkg/store/postgres/postgres_test.go` contains integration tests (migration
idempotency, CRUD + filter coverage for users, projects, agents, secrets,
groups, policies, invite codes, env vars) that run against a live Postgres
instance. Tests skip automatically when `SCION_TEST_POSTGRES_URL` is not set:

```go
const envVarDSN = "SCION_TEST_POSTGRES_URL"
// ...
if dsn == "" {
    t.Skipf("set %s to run Postgres tests", envVarDSN)
}
```

Each test calls `resetSchema` (`DROP SCHEMA public CASCADE; CREATE SCHEMA
public`) before applying migrations, giving a clean slate per test function.

`make test-fast` (which passes `-tags no_sqlite`) excludes the SQLite driver
and exercises the rest of the codebase including the postgres package files; CI
runs this path. The full Postgres integration suite requires a live DSN and is
not wired into CI at this time.
