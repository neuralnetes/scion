// Copyright 2026 Google LLC
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

// Package postgres provides a PostgreSQL implementation of the Store interface.
package postgres

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"
)

// PostgresStore implements the Store interface using PostgreSQL.
type PostgresStore struct {
	db *sql.DB
}

// New creates a new Postgres store with the given connection URL.
// The URL is passed directly to lib/pq (e.g. "postgres://user:pass@host/db?sslmode=disable").
func New(connURL string) (*PostgresStore, error) {
	db, err := sql.Open("postgres", connURL)
	if err != nil {
		return nil, fmt.Errorf("failed to open database: %w", err)
	}

	// SQLite pins this at 1 (single-writer). Postgres is built for concurrent
	// access, and the whole point of this backend is a horizontally-scaled,
	// stateless hub — 4 would starve under concurrent load. 25 is a sane default.
	db.SetMaxOpenConns(25)
	db.SetMaxIdleConns(25)

	return &PostgresStore{db: db}, nil
}

// Close closes the database connection.
func (s *PostgresStore) Close() error {
	return s.db.Close()
}

// DB returns the underlying *sql.DB for direct access in tests.
func (s *PostgresStore) DB() *sql.DB {
	return s.db
}

// Ping checks database connectivity.
func (s *PostgresStore) Ping(ctx context.Context) error {
	return s.db.PingContext(ctx)
}

// advisoryLockID is an arbitrary, stable key for the migration advisory lock.
// In a stateless, horizontally-scaled hub, multiple replicas may start and run
// Migrate concurrently; a database-global advisory lock serializes them so only
// one replica applies migrations while the others wait.
const advisoryLockID = 0x5C104D16 // "SCIONDB" mnemonic

// Migrate applies database migrations.
func (s *PostgresStore) Migrate(ctx context.Context) error {
	// Serialize migrations across replicas with a session-level advisory lock.
	// The lock and unlock MUST run on the same session, so pin them to a
	// dedicated connection; the migrations themselves run via the pool — the
	// lock is database-global and blocks other replicas regardless of which
	// connection runs the DDL.
	lockConn, err := s.db.Conn(ctx)
	if err != nil {
		return fmt.Errorf("failed to acquire connection for migration lock: %w", err)
	}
	defer lockConn.Close()
	if _, err := lockConn.ExecContext(ctx, "SELECT pg_advisory_lock($1)", advisoryLockID); err != nil {
		return fmt.Errorf("failed to acquire migration advisory lock: %w", err)
	}
	defer func() {
		// Best-effort unlock; closing the connection also releases the lock.
		_, _ = lockConn.ExecContext(ctx, "SELECT pg_advisory_unlock($1)", advisoryLockID)
	}()

	migrations := []any{
		migrationV1,
		migrationV2,
		migrationV3,
		migrationV4,
		migrationV5,
		migrationV6,
		migrationV7,
		migrationV8,
		migrationV9,
		migrationV10,
		migrationV11,
		migrationV12,
		migrationV13,
		migrationV14,
		migrationV15,
		migrationV16,
		migrationV17,
		migrationV18,
		migrationV19,
		migrationV20,
		migrationV21,
		migrationV22,
		migrationV23,
		migrationV24,
		migrationV25,
		migrationV26,
		migrationV27,
		migrationV28,
		migrationV29,
		migrationV30,
		migrationV31,
		migrationV32,
		migrationV33,
		migrationV34,
		migrationV35,
		migrationV36,
		migrationV37,
		migrationV38,
		migrationV39,
		migrationV40,
		migrationV41,
		migrationV42,
		migrationV43,
		migrationV44,
		migrationV45,
		migrationV46,
		migrationV47,
		migrationV48,
		migrationV49,
		migrateV50,
		migrationV51,
		migrationV52,
		migrationV53,
	}

	// Create migrations table if not exists
	if _, err := s.db.ExecContext(ctx, `
		CREATE TABLE IF NOT EXISTS schema_migrations (
			version INTEGER PRIMARY KEY,
			applied_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP
		)
	`); err != nil {
		return fmt.Errorf("failed to create migrations table: %w", err)
	}

	// Get current version
	var currentVersion int
	err = s.db.QueryRowContext(ctx, "SELECT COALESCE(MAX(version), 0) FROM schema_migrations").Scan(&currentVersion)
	if err != nil {
		return fmt.Errorf("failed to get current schema version: %w", err)
	}

	// In SQLite these migrations need PRAGMA foreign_keys=OFF because they
	// recreate a parent table (DROP + rename), which would otherwise cascade.
	// The Postgres translations use in-place ALTER TABLE instead, so none
	// actually need special handling — the map is kept (empty) plus
	// applyMigrationWithFKOff so the runner shape stays 1-to-1 with SQLite.
	foreignKeysOffMigrations := map[int]bool{}

	// Apply pending migrations
	for i, migration := range migrations {
		version := i + 1
		if version <= currentVersion {
			continue
		}

		switch m := migration.(type) {
		case string:
			needsFKOff := foreignKeysOffMigrations[version]

			if needsFKOff {
				if err := s.applyMigrationWithFKOff(ctx, version, m); err != nil {
					return err
				}
				continue
			}

			tx, err := s.db.BeginTx(ctx, nil)
			if err != nil {
				return fmt.Errorf("failed to start transaction for migration %d: %w", version, err)
			}

			if _, err := tx.ExecContext(ctx, m); err != nil {
				tx.Rollback()
				return fmt.Errorf("failed to apply migration %d: %w", version, err)
			}

			if _, err := tx.ExecContext(ctx, "INSERT INTO schema_migrations (version) VALUES ($1)", version); err != nil {
				tx.Rollback()
				return fmt.Errorf("failed to record migration %d: %w", version, err)
			}

			if err := tx.Commit(); err != nil {
				return fmt.Errorf("failed to commit migration %d: %w", version, err)
			}

		case func(ctx context.Context, tx *sql.Tx) error:
			tx, err := s.db.BeginTx(ctx, nil)
			if err != nil {
				return fmt.Errorf("failed to start transaction for migration %d: %w", version, err)
			}

			if err := m(ctx, tx); err != nil {
				tx.Rollback()
				return fmt.Errorf("failed to apply migration %d: %w", version, err)
			}

			if _, err := tx.ExecContext(ctx, "INSERT INTO schema_migrations (version) VALUES ($1)", version); err != nil {
				tx.Rollback()
				return fmt.Errorf("failed to record migration %d: %w", version, err)
			}

			if err := tx.Commit(); err != nil {
				return fmt.Errorf("failed to commit migration %d: %w", version, err)
			}

		default:
			return fmt.Errorf("migration %d: unsupported type %T", version, migration)
		}
	}

	return nil
}

// applyMigrationWithFKOff runs a migration that requires PRAGMA
// foreign_keys=OFF. In Postgres, foreign key deferral is handled within the
// migration SQL itself (e.g. DROP ... CASCADE / explicit FK drops), so this
// function simply runs the migration in a plain transaction. The
// foreignKeysOffMigrations map and this function are kept so the runner shape
// stays 1-to-1 with the SQLite implementation.
func (s *PostgresStore) applyMigrationWithFKOff(ctx context.Context, version int, migration string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("failed to start transaction for migration %d: %w", version, err)
	}

	if _, err := tx.ExecContext(ctx, migration); err != nil {
		tx.Rollback()
		return fmt.Errorf("failed to apply migration %d: %w", version, err)
	}

	if _, err := tx.ExecContext(ctx, "INSERT INTO schema_migrations (version) VALUES ($1)", version); err != nil {
		tx.Rollback()
		return fmt.Errorf("failed to record migration %d: %w", version, err)
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("failed to commit migration %d: %w", version, err)
	}

	return nil
}

// Helper functions for JSON marshaling/unmarshaling
func marshalJSON(v interface{}) string {
	if v == nil {
		return ""
	}
	data, err := json.Marshal(v)
	if err != nil {
		return ""
	}
	return string(data)
}

func unmarshalJSON[T any](data string, v *T) {
	if data == "" {
		return
	}
	json.Unmarshal([]byte(data), v)
}

// nullableString returns a sql.NullString for database insertion.
// Empty strings become NULL, which is important for UNIQUE and FK constraints.
func nullableString(s string) sql.NullString {
	if s == "" {
		return sql.NullString{Valid: false}
	}
	return sql.NullString{String: s, Valid: true}
}

// nullableTime returns a sql.NullTime for database insertion.
// Zero time values become NULL.
func nullableTime(t time.Time) sql.NullTime {
	if t.IsZero() {
		return sql.NullTime{Valid: false}
	}
	return sql.NullTime{Time: t, Valid: true}
}

// nullableInt64 returns a sql.NullInt64 for database insertion.
// Nil pointers become NULL.
func nullableInt64(v *int64) sql.NullInt64 {
	if v == nil {
		return sql.NullInt64{Valid: false}
	}
	return sql.NullInt64{Int64: *v, Valid: true}
}

// marshalJSONPtr marshals a pointer value to JSON string, returning empty string for nil pointers.
// Unlike marshalJSON, this correctly detects nil typed pointers.
func marshalJSONPtr[T any](v *T) string {
	if v == nil {
		return ""
	}
	data, err := json.Marshal(v)
	if err != nil {
		return ""
	}
	return string(data)
}

// nullableTimePtr returns a *time.Time for scanning nullable timestamps.
func nullableTimePtr(t sql.NullTime) *time.Time {
	if !t.Valid {
		return nil
	}
	return &t.Time
}

// ptrToNullTime converts a *time.Time to sql.NullTime for database insertion.
// Nil pointers become NULL.
func ptrToNullTime(t *time.Time) sql.NullTime {
	if t == nil {
		return sql.NullTime{Valid: false}
	}
	return sql.NullTime{Time: *t, Valid: true}
}
