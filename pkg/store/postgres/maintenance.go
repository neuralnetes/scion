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

package postgres

import (
	"context"
	"database/sql"
	"time"

	"github.com/GoogleCloudPlatform/scion/pkg/store"
)

// ============================================================================
// Maintenance Operation Operations
// ============================================================================

// ListMaintenanceOperations returns all registered operations and migrations.
func (s *PostgresStore) ListMaintenanceOperations(ctx context.Context) ([]store.MaintenanceOperation, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, key, title, description, category, status,
			created_at, started_at, completed_at, started_by, result, metadata
		FROM maintenance_operations
		ORDER BY category, created_at
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var ops []store.MaintenanceOperation
	for rows.Next() {
		var op store.MaintenanceOperation
		var startedAt, completedAt sql.NullTime
		var startedBy, result, metadata sql.NullString

		if err := rows.Scan(
			&op.ID, &op.Key, &op.Title, &op.Description, &op.Category, &op.Status,
			&op.CreatedAt, &startedAt, &completedAt, &startedBy, &result, &metadata,
		); err != nil {
			return nil, err
		}

		if startedAt.Valid {
			op.StartedAt = &startedAt.Time
		}
		if completedAt.Valid {
			op.CompletedAt = &completedAt.Time
		}
		op.StartedBy = startedBy.String
		op.Result = result.String
		op.Metadata = metadata.String

		ops = append(ops, op)
	}
	return ops, rows.Err()
}

// GetMaintenanceOperation returns a single operation by key.
func (s *PostgresStore) GetMaintenanceOperation(ctx context.Context, key string) (*store.MaintenanceOperation, error) {
	op := &store.MaintenanceOperation{}
	var startedAt, completedAt sql.NullTime
	var startedBy, result, metadata sql.NullString

	err := s.db.QueryRowContext(ctx, `
		SELECT id, key, title, description, category, status,
			created_at, started_at, completed_at, started_by, result, metadata
		FROM maintenance_operations WHERE key = $1
	`, key).Scan(
		&op.ID, &op.Key, &op.Title, &op.Description, &op.Category, &op.Status,
		&op.CreatedAt, &startedAt, &completedAt, &startedBy, &result, &metadata,
	)
	if err == sql.ErrNoRows {
		return nil, store.ErrNotFound
	}
	if err != nil {
		return nil, err
	}

	if startedAt.Valid {
		op.StartedAt = &startedAt.Time
	}
	if completedAt.Valid {
		op.CompletedAt = &completedAt.Time
	}
	op.StartedBy = startedBy.String
	op.Result = result.String
	op.Metadata = metadata.String

	return op, nil
}

// UpdateMaintenanceOperation updates an operation's status and result fields.
func (s *PostgresStore) UpdateMaintenanceOperation(ctx context.Context, op *store.MaintenanceOperation) error {
	res, err := s.db.ExecContext(ctx, `
		UPDATE maintenance_operations
		SET status = $1, started_at = $2, completed_at = $3, started_by = $4, result = $5, metadata = $6
		WHERE key = $7
	`,
		op.Status,
		nullableTime(timeFromPtr(op.StartedAt)),
		nullableTime(timeFromPtr(op.CompletedAt)),
		nullableString(op.StartedBy),
		nullableString(op.Result),
		nullableString(op.Metadata),
		op.Key,
	)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return store.ErrNotFound
	}
	return nil
}

// CreateMaintenanceRun inserts a new run record.
func (s *PostgresStore) CreateMaintenanceRun(ctx context.Context, run *store.MaintenanceOperationRun) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO maintenance_operation_runs (
			id, operation_key, status, started_at, completed_at, started_by, result, log
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
	`,
		run.ID, run.OperationKey, run.Status, run.StartedAt,
		nullableTime(timeFromPtr(run.CompletedAt)),
		nullableString(run.StartedBy),
		nullableString(run.Result),
		run.Log,
	)
	return err
}

// UpdateMaintenanceRun updates a run's status, result, and log.
func (s *PostgresStore) UpdateMaintenanceRun(ctx context.Context, run *store.MaintenanceOperationRun) error {
	res, err := s.db.ExecContext(ctx, `
		UPDATE maintenance_operation_runs
		SET status = $1, completed_at = $2, result = $3, log = $4
		WHERE id = $5
	`,
		run.Status,
		nullableTime(timeFromPtr(run.CompletedAt)),
		nullableString(run.Result),
		run.Log,
		run.ID,
	)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return store.ErrNotFound
	}
	return nil
}

// GetMaintenanceRun returns a single run by ID.
func (s *PostgresStore) GetMaintenanceRun(ctx context.Context, id string) (*store.MaintenanceOperationRun, error) {
	run := &store.MaintenanceOperationRun{}
	var completedAt sql.NullTime
	var startedBy, result sql.NullString

	err := s.db.QueryRowContext(ctx, `
		SELECT id, operation_key, status, started_at, completed_at, started_by, result, log
		FROM maintenance_operation_runs WHERE id = $1
	`, id).Scan(
		&run.ID, &run.OperationKey, &run.Status, &run.StartedAt,
		&completedAt, &startedBy, &result, &run.Log,
	)
	if err == sql.ErrNoRows {
		return nil, store.ErrNotFound
	}
	if err != nil {
		return nil, err
	}

	if completedAt.Valid {
		run.CompletedAt = &completedAt.Time
	}
	run.StartedBy = startedBy.String
	run.Result = result.String

	return run, nil
}

// AbortRunningMaintenanceOps transitions any "running" operation runs and
// migrations to "failed" with an appropriate result message. This is called at
// server startup to clean up operations interrupted by a restart.
func (s *PostgresStore) AbortRunningMaintenanceOps(ctx context.Context) (int64, int64, error) {
	now := sql.NullTime{Time: time.Now(), Valid: true}
	result := `{"error":"aborted: server was restarted while operation was running"}`

	// Abort stalled runs.
	res, err := s.db.ExecContext(ctx, `
		UPDATE maintenance_operation_runs
		SET status = 'failed', completed_at = $1, result = $2
		WHERE status = 'running'
	`, now, result)
	if err != nil {
		return 0, 0, err
	}
	runs, _ := res.RowsAffected()

	// Reset stalled migrations back to pending (they can be retried).
	res, err = s.db.ExecContext(ctx, `
		UPDATE maintenance_operations
		SET status = 'pending', started_at = NULL, completed_at = NULL, result = $1
		WHERE status = 'running' AND category = 'migration'
	`, result)
	if err != nil {
		return runs, 0, err
	}
	migrations, _ := res.RowsAffected()

	return runs, migrations, nil
}

// ListMaintenanceRuns returns runs for a given operation key, ordered by started_at DESC.
func (s *PostgresStore) ListMaintenanceRuns(ctx context.Context, operationKey string, limit int) ([]store.MaintenanceOperationRun, error) {
	if limit <= 0 {
		limit = 20
	}

	rows, err := s.db.QueryContext(ctx, `
		SELECT id, operation_key, status, started_at, completed_at, started_by, result, log
		FROM maintenance_operation_runs
		WHERE operation_key = $1
		ORDER BY started_at DESC
		LIMIT $2
	`, operationKey, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var runs []store.MaintenanceOperationRun
	for rows.Next() {
		var run store.MaintenanceOperationRun
		var completedAt sql.NullTime
		var startedBy, result sql.NullString

		if err := rows.Scan(
			&run.ID, &run.OperationKey, &run.Status, &run.StartedAt,
			&completedAt, &startedBy, &result, &run.Log,
		); err != nil {
			return nil, err
		}

		if completedAt.Valid {
			run.CompletedAt = &completedAt.Time
		}
		run.StartedBy = startedBy.String
		run.Result = result.String

		runs = append(runs, run)
	}
	return runs, rows.Err()
}
