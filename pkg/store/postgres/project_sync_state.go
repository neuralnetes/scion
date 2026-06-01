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

	"github.com/GoogleCloudPlatform/scion/pkg/store"
)

// ============================================================================
// Project Sync State Operations
// ============================================================================

// UpsertProjectSyncState creates or updates sync state for a project.
func (s *PostgresStore) UpsertProjectSyncState(ctx context.Context, state *store.ProjectSyncState) error {
	if state.ProjectID == "" {
		return store.ErrInvalidInput
	}

	_, err := s.db.ExecContext(ctx, `
		INSERT INTO project_sync_state (project_id, broker_id, last_sync_time, last_commit_sha, file_count, total_bytes)
		VALUES ($1, $2, $3, $4, $5, $6)
		ON CONFLICT(project_id, broker_id) DO UPDATE SET
			last_sync_time = excluded.last_sync_time,
			last_commit_sha = excluded.last_commit_sha,
			file_count = excluded.file_count,
			total_bytes = excluded.total_bytes
	`, state.ProjectID, state.BrokerID,
		ptrToNullTime(state.LastSyncTime),
		nullableString(state.LastCommitSHA),
		state.FileCount, state.TotalBytes,
	)
	return err
}

// GetProjectSyncState retrieves sync state for a project and optional broker.
func (s *PostgresStore) GetProjectSyncState(ctx context.Context, projectID, brokerID string) (*store.ProjectSyncState, error) {
	state := &store.ProjectSyncState{}
	var lastSyncTime sql.NullTime
	var lastCommitSHA sql.NullString

	err := s.db.QueryRowContext(ctx, `
		SELECT project_id, broker_id, last_sync_time, last_commit_sha, file_count, total_bytes
		FROM project_sync_state
		WHERE project_id = $1 AND broker_id = $2
	`, projectID, brokerID).Scan(
		&state.ProjectID, &state.BrokerID,
		&lastSyncTime, &lastCommitSHA,
		&state.FileCount, &state.TotalBytes,
	)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, store.ErrNotFound
		}
		return nil, err
	}

	if lastSyncTime.Valid {
		state.LastSyncTime = &lastSyncTime.Time
	}
	if lastCommitSHA.Valid {
		state.LastCommitSHA = lastCommitSHA.String
	}

	return state, nil
}

// ListProjectSyncStates returns all sync states for a project.
func (s *PostgresStore) ListProjectSyncStates(ctx context.Context, projectID string) ([]store.ProjectSyncState, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT project_id, broker_id, last_sync_time, last_commit_sha, file_count, total_bytes
		FROM project_sync_state
		WHERE project_id = $1
		ORDER BY broker_id
	`, projectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var states []store.ProjectSyncState
	for rows.Next() {
		var state store.ProjectSyncState
		var lastSyncTime sql.NullTime
		var lastCommitSHA sql.NullString

		if err := rows.Scan(
			&state.ProjectID, &state.BrokerID,
			&lastSyncTime, &lastCommitSHA,
			&state.FileCount, &state.TotalBytes,
		); err != nil {
			return nil, err
		}

		if lastSyncTime.Valid {
			state.LastSyncTime = &lastSyncTime.Time
		}
		if lastCommitSHA.Valid {
			state.LastCommitSHA = lastCommitSHA.String
		}

		states = append(states, state)
	}

	if states == nil {
		states = []store.ProjectSyncState{}
	}
	return states, rows.Err()
}

// DeleteProjectSyncState removes sync state for a project and optional broker.
func (s *PostgresStore) DeleteProjectSyncState(ctx context.Context, projectID, brokerID string) error {
	result, err := s.db.ExecContext(ctx, `
		DELETE FROM project_sync_state WHERE project_id = $1 AND broker_id = $2
	`, projectID, brokerID)
	if err != nil {
		return err
	}

	rows, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if rows == 0 {
		return store.ErrNotFound
	}
	return nil
}
