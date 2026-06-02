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

// ============================================================================
// Template Operations
// ============================================================================

func (s *PostgresStore) CreateTemplate(ctx context.Context, template *store.Template) error {
	now := time.Now()
	template.Created = now
	template.Updated = now

	// Set default status if not provided
	if template.Status == "" {
		template.Status = store.TemplateStatusActive
	}

	_, err := s.db.ExecContext(ctx, `
		INSERT INTO templates (
			id, name, slug, display_name, description, harness, default_harness_config, image, config,
			content_hash, scope, scope_id, project_id,
			storage_uri, storage_bucket, storage_path, files,
			base_template, locked, status,
			owner_id, created_by, updated_by, visibility,
			created_at, updated_at
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17, $18, $19, $20, $21, $22, $23, $24, $25, $26)
	`,
		template.ID, template.Name, template.Slug, nullableString(template.DisplayName), nullableString(template.Description),
		template.Harness, nullableString(template.DefaultHarnessConfig), template.Image, marshalJSON(template.Config),
		nullableString(template.ContentHash), template.Scope, nullableString(template.ScopeID), nullableString(template.ProjectID),
		nullableString(template.StorageURI), nullableString(template.StorageBucket), nullableString(template.StoragePath), marshalJSON(template.Files),
		nullableString(template.BaseTemplate), boolToInt(template.Locked), template.Status,
		nullableString(template.OwnerID), nullableString(template.CreatedBy), nullableString(template.UpdatedBy), template.Visibility,
		template.Created, template.Updated,
	)
	if err != nil {
		if strings.Contains(err.Error(), "UNIQUE constraint failed") || strings.Contains(err.Error(), "duplicate key") {
			return store.ErrAlreadyExists
		}
		return err
	}
	return nil
}

func (s *PostgresStore) GetTemplate(ctx context.Context, id string) (*store.Template, error) {
	template := &store.Template{}
	var config, files string
	var displayName, description, contentHash, scopeID, projectID sql.NullString
	var storageURI, storageBucket, storagePath, baseTemplate sql.NullString
	var createdBy, updatedBy, ownerID, visibility sql.NullString
	var defaultHarnessConfig sql.NullString

	err := s.db.QueryRowContext(ctx, `
		SELECT id, name, slug, display_name, description, harness, default_harness_config, image, config,
			content_hash, scope, scope_id, project_id,
			storage_uri, storage_bucket, storage_path, files,
			base_template, locked, status,
			owner_id, created_by, updated_by, visibility,
			created_at, updated_at
		FROM templates WHERE id = $1
	`, id).Scan(
		&template.ID, &template.Name, &template.Slug, &displayName, &description,
		&template.Harness, &defaultHarnessConfig, &template.Image, &config,
		&contentHash, &template.Scope, &scopeID, &projectID,
		&storageURI, &storageBucket, &storagePath, &files,
		&baseTemplate, &template.Locked, &template.Status,
		&ownerID, &createdBy, &updatedBy, &visibility,
		&template.Created, &template.Updated,
	)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, store.ErrNotFound
		}
		return nil, err
	}

	if displayName.Valid {
		template.DisplayName = displayName.String
	}
	if description.Valid {
		template.Description = description.String
	}
	if defaultHarnessConfig.Valid {
		template.DefaultHarnessConfig = defaultHarnessConfig.String
	}
	if contentHash.Valid {
		template.ContentHash = contentHash.String
	}
	if scopeID.Valid {
		template.ScopeID = scopeID.String
	}
	if projectID.Valid {
		template.ProjectID = projectID.String
	}
	if storageURI.Valid {
		template.StorageURI = storageURI.String
	}
	if storageBucket.Valid {
		template.StorageBucket = storageBucket.String
	}
	if storagePath.Valid {
		template.StoragePath = storagePath.String
	}
	if baseTemplate.Valid {
		template.BaseTemplate = baseTemplate.String
	}
	if ownerID.Valid {
		template.OwnerID = ownerID.String
	}
	if createdBy.Valid {
		template.CreatedBy = createdBy.String
	}
	if updatedBy.Valid {
		template.UpdatedBy = updatedBy.String
	}
	if visibility.Valid {
		template.Visibility = visibility.String
	}
	unmarshalJSON(config, &template.Config)
	unmarshalJSON(files, &template.Files)

	return template, nil
}

func (s *PostgresStore) GetTemplateBySlug(ctx context.Context, slug, scope, scopeID string) (*store.Template, error) {
	var id string
	var err error

	if scope == "project" && scopeID != "" {
		// Try scope_id first, then fall back to project_id for backwards compatibility
		err = s.db.QueryRowContext(ctx, "SELECT id FROM templates WHERE slug = $1 AND scope = $2 AND (scope_id = $3 OR project_id = $4)", slug, scope, scopeID, scopeID).Scan(&id)
	} else if scope == "user" && scopeID != "" {
		err = s.db.QueryRowContext(ctx, "SELECT id FROM templates WHERE slug = $1 AND scope = $2 AND scope_id = $3", slug, scope, scopeID).Scan(&id)
	} else {
		err = s.db.QueryRowContext(ctx, "SELECT id FROM templates WHERE slug = $1 AND scope = $2", slug, scope).Scan(&id)
	}

	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, store.ErrNotFound
		}
		return nil, err
	}
	return s.GetTemplate(ctx, id)
}

func (s *PostgresStore) UpdateTemplate(ctx context.Context, template *store.Template) error {
	template.Updated = time.Now()

	result, err := s.db.ExecContext(ctx, `
		UPDATE templates SET
			name = $1, slug = $2, display_name = $3, description = $4,
			harness = $5, default_harness_config = $6, image = $7, config = $8,
			content_hash = $9, scope = $10, scope_id = $11, project_id = $12,
			storage_uri = $13, storage_bucket = $14, storage_path = $15, files = $16,
			base_template = $17, locked = $18, status = $19,
			owner_id = $20, updated_by = $21, visibility = $22,
			updated_at = $23
		WHERE id = $24
	`,
		template.Name, template.Slug, nullableString(template.DisplayName), nullableString(template.Description),
		template.Harness, nullableString(template.DefaultHarnessConfig), template.Image, marshalJSON(template.Config),
		nullableString(template.ContentHash), template.Scope, nullableString(template.ScopeID), nullableString(template.ProjectID),
		nullableString(template.StorageURI), nullableString(template.StorageBucket), nullableString(template.StoragePath), marshalJSON(template.Files),
		nullableString(template.BaseTemplate), boolToInt(template.Locked), template.Status,
		nullableString(template.OwnerID), nullableString(template.UpdatedBy), template.Visibility,
		template.Updated,
		template.ID,
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

func (s *PostgresStore) DeleteTemplate(ctx context.Context, id string) error {
	result, err := s.db.ExecContext(ctx, "DELETE FROM templates WHERE id = $1", id)
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

func (s *PostgresStore) DeleteTemplatesByScope(ctx context.Context, scope, scopeID string) (int, error) {
	result, err := s.db.ExecContext(ctx, "DELETE FROM templates WHERE scope = $1 AND scope_id = $2", scope, scopeID)
	if err != nil {
		return 0, err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return 0, err
	}
	return int(n), nil
}

func (s *PostgresStore) ListTemplates(ctx context.Context, filter store.TemplateFilter, opts store.ListOptions) (*store.ListResult[store.Template], error) {
	var conditions []string
	var args []interface{}

	if filter.Name != "" {
		// Exact match on name or slug
		conditions = append(conditions, fmt.Sprintf("(name = $%d OR slug = $%d)", len(args)+1, len(args)+2))
		args = append(args, filter.Name, filter.Name)
	}
	if filter.Scope != "" {
		conditions = append(conditions, fmt.Sprintf("scope = $%d", len(args)+1))
		args = append(args, filter.Scope)
	}
	if filter.ScopeID != "" {
		conditions = append(conditions, fmt.Sprintf("(scope_id = $%d OR project_id = $%d)", len(args)+1, len(args)+2))
		args = append(args, filter.ScopeID, filter.ScopeID)
	} else if filter.ProjectID != "" && filter.Scope == "" {
		// When projectId is set without scope, return global + project-scoped templates for this project
		conditions = append(conditions, fmt.Sprintf("(scope = 'global' OR (scope = 'project' AND (scope_id = $%d OR project_id = $%d)))", len(args)+1, len(args)+2))
		args = append(args, filter.ProjectID, filter.ProjectID)
	} else if filter.ProjectID != "" {
		// Backwards compatibility: projectId with explicit scope
		conditions = append(conditions, fmt.Sprintf("(scope_id = $%d OR project_id = $%d)", len(args)+1, len(args)+2))
		args = append(args, filter.ProjectID, filter.ProjectID)
	}
	if filter.Harness != "" {
		conditions = append(conditions, fmt.Sprintf("harness = $%d", len(args)+1))
		args = append(args, filter.Harness)
	}
	if filter.OwnerID != "" {
		conditions = append(conditions, fmt.Sprintf("owner_id = $%d", len(args)+1))
		args = append(args, filter.OwnerID)
	}
	if filter.Status != "" {
		conditions = append(conditions, fmt.Sprintf("status = $%d", len(args)+1))
		args = append(args, filter.Status)
	}
	if filter.Search != "" {
		conditions = append(conditions, fmt.Sprintf("(name LIKE $%d OR description LIKE $%d)", len(args)+1, len(args)+2))
		searchPattern := "%" + filter.Search + "%"
		args = append(args, searchPattern, searchPattern)
	}

	whereClause := ""
	if len(conditions) > 0 {
		whereClause = "WHERE " + strings.Join(conditions, " AND ")
	}

	var totalCount int
	countQuery := fmt.Sprintf("SELECT COUNT(*) FROM templates %s", whereClause)
	if err := s.db.QueryRowContext(ctx, countQuery, args...).Scan(&totalCount); err != nil {
		return nil, err
	}

	limit := opts.Limit
	if limit <= 0 {
		limit = 50
	}

	query := fmt.Sprintf(`
		SELECT id, name, slug, display_name, description, harness, default_harness_config, image, config,
			content_hash, scope, scope_id, project_id,
			storage_uri, storage_bucket, storage_path, files,
			base_template, locked, status,
			owner_id, created_by, updated_by, visibility,
			created_at, updated_at
		FROM templates %s ORDER BY created_at DESC LIMIT $%d
	`, whereClause, len(args)+1)
	args = append(args, limit)

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var templates []store.Template
	for rows.Next() {
		var template store.Template
		var config, files string
		var displayName, description, contentHash, scopeID, projectID sql.NullString
		var storageURI, storageBucket, storagePath, baseTemplate sql.NullString
		var createdBy, updatedBy, ownerID, visibility sql.NullString
		var defaultHarnessConfig sql.NullString

		if err := rows.Scan(
			&template.ID, &template.Name, &template.Slug, &displayName, &description,
			&template.Harness, &defaultHarnessConfig, &template.Image, &config,
			&contentHash, &template.Scope, &scopeID, &projectID,
			&storageURI, &storageBucket, &storagePath, &files,
			&baseTemplate, &template.Locked, &template.Status,
			&ownerID, &createdBy, &updatedBy, &visibility,
			&template.Created, &template.Updated,
		); err != nil {
			return nil, err
		}

		if displayName.Valid {
			template.DisplayName = displayName.String
		}
		if description.Valid {
			template.Description = description.String
		}
		if defaultHarnessConfig.Valid {
			template.DefaultHarnessConfig = defaultHarnessConfig.String
		}
		if contentHash.Valid {
			template.ContentHash = contentHash.String
		}
		if scopeID.Valid {
			template.ScopeID = scopeID.String
		}
		if projectID.Valid {
			template.ProjectID = projectID.String
		}
		if storageURI.Valid {
			template.StorageURI = storageURI.String
		}
		if storageBucket.Valid {
			template.StorageBucket = storageBucket.String
		}
		if storagePath.Valid {
			template.StoragePath = storagePath.String
		}
		if baseTemplate.Valid {
			template.BaseTemplate = baseTemplate.String
		}
		if ownerID.Valid {
			template.OwnerID = ownerID.String
		}
		if createdBy.Valid {
			template.CreatedBy = createdBy.String
		}
		if updatedBy.Valid {
			template.UpdatedBy = updatedBy.String
		}
		if visibility.Valid {
			template.Visibility = visibility.String
		}
		unmarshalJSON(config, &template.Config)
		unmarshalJSON(files, &template.Files)

		templates = append(templates, template)
	}

	return &store.ListResult[store.Template]{
		Items:      templates,
		TotalCount: totalCount,
	}, nil
}
