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
	"errors"
	"time"

	"github.com/GoogleCloudPlatform/scion/pkg/store"
)

// ============================================================================
// ProjectProvider Operations

// ============================================================================

func (s *PostgresStore) AddProjectProvider(ctx context.Context, provider *store.ProjectProvider) error {
	// Set LinkedAt to now if not already set
	if provider.LinkedAt.IsZero() && provider.LinkedBy != "" {
		provider.LinkedAt = time.Now()
	}

	_, err := s.db.ExecContext(ctx, `
		INSERT INTO project_contributors (project_id, broker_id, broker_name, local_path, mode, status, profiles, last_seen, linked_by, linked_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
		ON CONFLICT (project_id, broker_id) DO UPDATE SET
			broker_name = EXCLUDED.broker_name,
			local_path = EXCLUDED.local_path,
			mode = EXCLUDED.mode,
			status = EXCLUDED.status,
			profiles = EXCLUDED.profiles,
			last_seen = EXCLUDED.last_seen,
			linked_by = EXCLUDED.linked_by,
			linked_at = EXCLUDED.linked_at
	`,
		provider.ProjectID, provider.BrokerID, provider.BrokerName, provider.LocalPath, "", provider.Status,
		"[]", provider.LastSeen, // profiles column kept for schema compat but no longer used
		nullableString(provider.LinkedBy), nullableTime(provider.LinkedAt),
	)
	return err
}

func (s *PostgresStore) RemoveProjectProvider(ctx context.Context, projectID, brokerID string) error {
	result, err := s.db.ExecContext(ctx, "DELETE FROM project_contributors WHERE project_id = $1 AND broker_id = $2", projectID, brokerID)
	if err != nil {
		return err
	}
	rowsAffected, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if rowsAffected == 0 {
		return store.ErrNotFound
	}
	return nil
}

func (s *PostgresStore) GetProjectProvider(ctx context.Context, projectID, brokerID string) (*store.ProjectProvider, error) {
	var provider store.ProjectProvider
	var localPath, linkedBy sql.NullString
	var providerMode, profiles string // unused columns kept for schema compat
	var lastSeen, linkedAt sql.NullTime

	err := s.db.QueryRowContext(ctx, `
		SELECT project_id, broker_id, broker_name, local_path, mode, status, profiles, last_seen, linked_by, linked_at
		FROM project_contributors WHERE project_id = $1 AND broker_id = $2
	`, projectID, brokerID).Scan(
		&provider.ProjectID, &provider.BrokerID, &provider.BrokerName, &localPath, &providerMode, &provider.Status,
		&profiles, &lastSeen, &linkedBy, &linkedAt,
	)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, store.ErrNotFound
		}
		return nil, err
	}

	if localPath.Valid {
		provider.LocalPath = localPath.String
	}
	if lastSeen.Valid {
		provider.LastSeen = lastSeen.Time
	}
	if linkedBy.Valid {
		provider.LinkedBy = linkedBy.String
	}
	if linkedAt.Valid {
		provider.LinkedAt = linkedAt.Time
	}
	// profiles column no longer used - lookup from RuntimeBroker.Profiles instead

	return &provider, nil
}

func (s *PostgresStore) GetProjectProviders(ctx context.Context, projectID string) ([]store.ProjectProvider, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT project_id, broker_id, broker_name, local_path, mode, status, profiles, last_seen, linked_by, linked_at
		FROM project_contributors WHERE project_id = $1
	`, projectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var providers []store.ProjectProvider
	for rows.Next() {
		var provider store.ProjectProvider
		var localPath, linkedBy sql.NullString
		var providerMode, profiles string // unused columns kept for schema compat
		var lastSeen, linkedAt sql.NullTime

		if err := rows.Scan(
			&provider.ProjectID, &provider.BrokerID, &provider.BrokerName, &localPath, &providerMode, &provider.Status,
			&profiles, &lastSeen, &linkedBy, &linkedAt,
		); err != nil {
			return nil, err
		}

		if localPath.Valid {
			provider.LocalPath = localPath.String
		}
		if lastSeen.Valid {
			provider.LastSeen = lastSeen.Time
		}
		if linkedBy.Valid {
			provider.LinkedBy = linkedBy.String
		}
		if linkedAt.Valid {
			provider.LinkedAt = linkedAt.Time
		}
		// profiles column no longer used - lookup from RuntimeBroker.Profiles instead

		providers = append(providers, provider)
	}

	return providers, nil
}

func (s *PostgresStore) GetBrokerProjects(ctx context.Context, brokerID string) ([]store.ProjectProvider, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT project_id, broker_id, broker_name, local_path, mode, status, profiles, last_seen, linked_by, linked_at
		FROM project_contributors WHERE broker_id = $1
	`, brokerID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var providers []store.ProjectProvider
	for rows.Next() {
		var provider store.ProjectProvider
		var localPath, linkedBy sql.NullString
		var providerMode, profiles string // unused columns kept for schema compat
		var lastSeen, linkedAt sql.NullTime

		if err := rows.Scan(
			&provider.ProjectID, &provider.BrokerID, &provider.BrokerName, &localPath, &providerMode, &provider.Status,
			&profiles, &lastSeen, &linkedBy, &linkedAt,
		); err != nil {
			return nil, err
		}

		if localPath.Valid {
			provider.LocalPath = localPath.String
		}
		if lastSeen.Valid {
			provider.LastSeen = lastSeen.Time
		}
		if linkedBy.Valid {
			provider.LinkedBy = linkedBy.String
		}
		if linkedAt.Valid {
			provider.LinkedAt = linkedAt.Time
		}
		// profiles column no longer used - lookup from RuntimeBroker.Profiles instead

		providers = append(providers, provider)
	}

	return providers, nil
}

func (s *PostgresStore) UpdateProviderStatus(ctx context.Context, projectID, brokerID, status string) error {
	now := time.Now()

	result, err := s.db.ExecContext(ctx, `
		UPDATE project_contributors SET status = $1, last_seen = $2 WHERE project_id = $3 AND broker_id = $4
	`, status, now, projectID, brokerID)
	if err != nil {
		return err
	}

	rowsAffected, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if rowsAffected == 0 {
		return store.ErrNotFound
	}
	return nil
}
