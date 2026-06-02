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
	"fmt"
	"strings"
	"time"

	"github.com/GoogleCloudPlatform/scion/pkg/store"
)

func (s *PostgresStore) CreateHarnessConfig(ctx context.Context, hc *store.HarnessConfig) error {
	now := time.Now()
	hc.Created = now
	hc.Updated = now

	if hc.Status == "" {
		hc.Status = store.HarnessConfigStatusActive
	}

	_, err := s.db.ExecContext(ctx, `
		INSERT INTO harness_configs (
			id, name, slug, display_name, description, harness, config,
			content_hash, scope, scope_id,
			storage_uri, storage_bucket, storage_path, files,
			locked, status,
			owner_id, created_by, updated_by, visibility,
			created_at, updated_at
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17, $18, $19, $20, $21, $22)
	`,
		hc.ID, hc.Name, hc.Slug, nullableString(hc.DisplayName), nullableString(hc.Description),
		hc.Harness, marshalJSON(hc.Config),
		nullableString(hc.ContentHash), hc.Scope, nullableString(hc.ScopeID),
		nullableString(hc.StorageURI), nullableString(hc.StorageBucket), nullableString(hc.StoragePath), marshalJSON(hc.Files),
		boolToInt(hc.Locked), hc.Status,
		nullableString(hc.OwnerID), nullableString(hc.CreatedBy), nullableString(hc.UpdatedBy), hc.Visibility,
		hc.Created, hc.Updated,
	)
	if err != nil {
		if strings.Contains(err.Error(), "UNIQUE constraint failed") || strings.Contains(err.Error(), "duplicate key") {
			return store.ErrAlreadyExists
		}
		return err
	}
	return nil
}

func (s *PostgresStore) GetHarnessConfig(ctx context.Context, id string) (*store.HarnessConfig, error) {
	hc := &store.HarnessConfig{}
	var configJSON, filesJSON string
	var displayName, description, contentHash, scopeID sql.NullString
	var storageURI, storageBucket, storagePath sql.NullString
	var createdBy, updatedBy, ownerID, visibility sql.NullString

	err := s.db.QueryRowContext(ctx, `
		SELECT id, name, slug, display_name, description, harness, config,
			content_hash, scope, scope_id,
			storage_uri, storage_bucket, storage_path, files,
			locked, status,
			owner_id, created_by, updated_by, visibility,
			created_at, updated_at
		FROM harness_configs WHERE id = $1
	`, id).Scan(
		&hc.ID, &hc.Name, &hc.Slug, &displayName, &description,
		&hc.Harness, &configJSON,
		&contentHash, &hc.Scope, &scopeID,
		&storageURI, &storageBucket, &storagePath, &filesJSON,
		&hc.Locked, &hc.Status,
		&ownerID, &createdBy, &updatedBy, &visibility,
		&hc.Created, &hc.Updated,
	)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, store.ErrNotFound
		}
		return nil, err
	}

	if displayName.Valid {
		hc.DisplayName = displayName.String
	}
	if description.Valid {
		hc.Description = description.String
	}
	if contentHash.Valid {
		hc.ContentHash = contentHash.String
	}
	if scopeID.Valid {
		hc.ScopeID = scopeID.String
	}
	if storageURI.Valid {
		hc.StorageURI = storageURI.String
	}
	if storageBucket.Valid {
		hc.StorageBucket = storageBucket.String
	}
	if storagePath.Valid {
		hc.StoragePath = storagePath.String
	}
	if ownerID.Valid {
		hc.OwnerID = ownerID.String
	}
	if createdBy.Valid {
		hc.CreatedBy = createdBy.String
	}
	if updatedBy.Valid {
		hc.UpdatedBy = updatedBy.String
	}
	if visibility.Valid {
		hc.Visibility = visibility.String
	}
	unmarshalJSON(configJSON, &hc.Config)
	unmarshalJSON(filesJSON, &hc.Files)

	return hc, nil
}

func (s *PostgresStore) GetHarnessConfigBySlug(ctx context.Context, slug, scope, scopeID string) (*store.HarnessConfig, error) {
	var id string
	var err error

	if scopeID != "" {
		err = s.db.QueryRowContext(ctx, "SELECT id FROM harness_configs WHERE slug = $1 AND scope = $2 AND scope_id = $3", slug, scope, scopeID).Scan(&id)
	} else {
		err = s.db.QueryRowContext(ctx, "SELECT id FROM harness_configs WHERE slug = $1 AND scope = $2", slug, scope).Scan(&id)
	}

	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, store.ErrNotFound
		}
		return nil, err
	}
	return s.GetHarnessConfig(ctx, id)
}

func (s *PostgresStore) UpdateHarnessConfig(ctx context.Context, hc *store.HarnessConfig) error {
	hc.Updated = time.Now()

	result, err := s.db.ExecContext(ctx, `
		UPDATE harness_configs SET
			name = $1, slug = $2, display_name = $3, description = $4,
			harness = $5, config = $6,
			content_hash = $7, scope = $8, scope_id = $9,
			storage_uri = $10, storage_bucket = $11, storage_path = $12, files = $13,
			locked = $14, status = $15,
			owner_id = $16, updated_by = $17, visibility = $18,
			updated_at = $19
		WHERE id = $20
	`,
		hc.Name, hc.Slug, nullableString(hc.DisplayName), nullableString(hc.Description),
		hc.Harness, marshalJSON(hc.Config),
		nullableString(hc.ContentHash), hc.Scope, nullableString(hc.ScopeID),
		nullableString(hc.StorageURI), nullableString(hc.StorageBucket), nullableString(hc.StoragePath), marshalJSON(hc.Files),
		boolToInt(hc.Locked), hc.Status,
		nullableString(hc.OwnerID), nullableString(hc.UpdatedBy), hc.Visibility,
		hc.Updated,
		hc.ID,
	)
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

func (s *PostgresStore) DeleteHarnessConfig(ctx context.Context, id string) error {
	result, err := s.db.ExecContext(ctx, "DELETE FROM harness_configs WHERE id = $1", id)
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

func (s *PostgresStore) DeleteHarnessConfigsByScope(ctx context.Context, scope, scopeID string) (int, error) {
	result, err := s.db.ExecContext(ctx, "DELETE FROM harness_configs WHERE scope = $1 AND scope_id = $2", scope, scopeID)
	if err != nil {
		return 0, err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return 0, err
	}
	return int(n), nil
}

func (s *PostgresStore) ListHarnessConfigs(ctx context.Context, filter store.HarnessConfigFilter, opts store.ListOptions) (*store.ListResult[store.HarnessConfig], error) {
	var conditions []string
	var args []interface{}

	if filter.Name != "" {
		n := len(args) + 1
		conditions = append(conditions, fmt.Sprintf("(name = $%d OR slug = $%d)", n, n+1))
		args = append(args, filter.Name, filter.Name)
	}
	if filter.Scope != "" {
		n := len(args) + 1
		conditions = append(conditions, fmt.Sprintf("scope = $%d", n))
		args = append(args, filter.Scope)
	}
	if filter.ScopeID != "" {
		n := len(args) + 1
		conditions = append(conditions, fmt.Sprintf("scope_id = $%d", n))
		args = append(args, filter.ScopeID)
	} else if filter.ProjectID != "" && filter.Scope == "" {
		// When projectId is set without scope, return global + project-scoped configs for this project
		n := len(args) + 1
		conditions = append(conditions, fmt.Sprintf("(scope = 'global' OR (scope = 'project' AND scope_id = $%d))", n))
		args = append(args, filter.ProjectID)
	} else if (filter.Scope == "project" || filter.Scope == "grove") && filter.ProjectID != "" {
		// projectId combined with an explicit scope filter — narrow to that project.
		n := len(args) + 1
		conditions = append(conditions, fmt.Sprintf("scope_id = $%d", n))
		args = append(args, filter.ProjectID)
	}
	if filter.Harness != "" {
		n := len(args) + 1
		conditions = append(conditions, fmt.Sprintf("harness = $%d", n))
		args = append(args, filter.Harness)
	}
	if filter.OwnerID != "" {
		n := len(args) + 1
		conditions = append(conditions, fmt.Sprintf("owner_id = $%d", n))
		args = append(args, filter.OwnerID)
	}
	if filter.Status != "" {
		n := len(args) + 1
		conditions = append(conditions, fmt.Sprintf("status = $%d", n))
		args = append(args, filter.Status)
	}
	if filter.Search != "" {
		n := len(args) + 1
		conditions = append(conditions, fmt.Sprintf("(name LIKE $%d OR description LIKE $%d)", n, n+1))
		searchPattern := "%" + filter.Search + "%"
		args = append(args, searchPattern, searchPattern)
	}

	whereClause := ""
	if len(conditions) > 0 {
		whereClause = "WHERE " + strings.Join(conditions, " AND ")
	}

	var totalCount int
	countQuery := fmt.Sprintf("SELECT COUNT(*) FROM harness_configs %s", whereClause)
	if err := s.db.QueryRowContext(ctx, countQuery, args...).Scan(&totalCount); err != nil {
		return nil, err
	}

	limit := opts.Limit
	if limit <= 0 {
		limit = 50
	}

	n := len(args) + 1
	query := fmt.Sprintf(`
		SELECT id, name, slug, display_name, description, harness, config,
			content_hash, scope, scope_id,
			storage_uri, storage_bucket, storage_path, files,
			locked, status,
			owner_id, created_by, updated_by, visibility,
			created_at, updated_at
		FROM harness_configs %s ORDER BY created_at DESC LIMIT $%d
	`, whereClause, n)
	args = append(args, limit)

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var harnessConfigs []store.HarnessConfig
	for rows.Next() {
		var hc store.HarnessConfig
		var configJSON, filesJSON string
		var displayName, description, contentHash, scopeID sql.NullString
		var storageURI, storageBucket, storagePath sql.NullString
		var createdBy, updatedBy, ownerID, visibility sql.NullString

		if err := rows.Scan(
			&hc.ID, &hc.Name, &hc.Slug, &displayName, &description,
			&hc.Harness, &configJSON,
			&contentHash, &hc.Scope, &scopeID,
			&storageURI, &storageBucket, &storagePath, &filesJSON,
			&hc.Locked, &hc.Status,
			&ownerID, &createdBy, &updatedBy, &visibility,
			&hc.Created, &hc.Updated,
		); err != nil {
			return nil, err
		}

		if displayName.Valid {
			hc.DisplayName = displayName.String
		}
		if description.Valid {
			hc.Description = description.String
		}
		if contentHash.Valid {
			hc.ContentHash = contentHash.String
		}
		if scopeID.Valid {
			hc.ScopeID = scopeID.String
		}
		if storageURI.Valid {
			hc.StorageURI = storageURI.String
		}
		if storageBucket.Valid {
			hc.StorageBucket = storageBucket.String
		}
		if storagePath.Valid {
			hc.StoragePath = storagePath.String
		}
		if ownerID.Valid {
			hc.OwnerID = ownerID.String
		}
		if createdBy.Valid {
			hc.CreatedBy = createdBy.String
		}
		if updatedBy.Valid {
			hc.UpdatedBy = updatedBy.String
		}
		if visibility.Valid {
			hc.Visibility = visibility.String
		}
		unmarshalJSON(configJSON, &hc.Config)
		unmarshalJSON(filesJSON, &hc.Files)

		harnessConfigs = append(harnessConfigs, hc)
	}

	// When querying by ProjectID without explicit Scope, the query returns both
	// global and project-scoped configs. Deduplicate by slug, preferring the more
	// specific scope (project > global).
	if filter.ProjectID != "" && filter.Scope == "" {
		seen := make(map[string]int, len(harnessConfigs))
		deduped := make([]store.HarnessConfig, 0, len(harnessConfigs))
		for _, hc := range harnessConfigs {
			if idx, exists := seen[hc.Slug]; exists {
				if hc.Scope == "project" && deduped[idx].Scope == "global" {
					deduped[idx] = hc
				}
			} else {
				seen[hc.Slug] = len(deduped)
				deduped = append(deduped, hc)
			}
		}
		harnessConfigs = deduped
		totalCount = len(deduped)
	}

	return &store.ListResult[store.HarnessConfig]{
		Items:      harnessConfigs,
		TotalCount: totalCount,
	}, nil
}
