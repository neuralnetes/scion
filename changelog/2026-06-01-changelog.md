# Release Notes (Jun 1, 2026)

This release introduces a Postgres store backend for the hub, enabling stateless control-plane deployments backed by an external database instead of a node-local SQLite PVC. The ent-layer has supported Postgres for some time; this change brings the hand-written store layer to parity.

## 🚀 Features
* **[Store]: Postgres Backend.** The hub now accepts `database.driver: postgres` (environment variables `SCION_SERVER_DATABASE_DRIVER=postgres` and `SCION_SERVER_DATABASE_URL=<dsn>`) to connect to an external Postgres database instead of the default node-local SQLite file.
    * **Stateless Control Plane.** With Postgres as the backing store the hub StatefulSet and its associated PVC are no longer required for state durability, enabling fully stateless hub deployments that can scale horizontally or restart without data loss.
    * **Ent Parity.** The ent-generated layer has supported Postgres since its introduction; this change adds the hand-written store's Postgres twin so that all hub persistence paths (agents, sessions, secrets, projects) are covered by both drivers.
    * **Migration Note.** The grove → project backfill that runs on SQLite databases at startup is skipped automatically on a fresh Postgres deployment; no manual intervention is needed.

## 🐛 Fixes
* **[Infrastructure]:** Continued monitoring and stabilization of the agent dispatch pipeline.
