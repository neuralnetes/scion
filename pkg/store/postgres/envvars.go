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

func (s *PostgresStore) CreateEnvVar(ctx context.Context, envVar *store.EnvVar) error {
	now := time.Now()
	envVar.Created = now
	envVar.Updated = now

	_, err := s.db.ExecContext(ctx, `
		INSERT INTO env_vars (id, key, value, scope, scope_id, description, sensitive, injection_mode, secret, created_at, updated_at, created_by)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12)
	`,
		envVar.ID, envVar.Key, envVar.Value, envVar.Scope, envVar.ScopeID,
		envVar.Description, boolToInt(envVar.Sensitive), envVar.InjectionMode, boolToInt(envVar.Secret),
		envVar.Created, envVar.Updated, envVar.CreatedBy,
	)
	if err != nil {
		if strings.Contains(err.Error(), "UNIQUE constraint failed") || strings.Contains(err.Error(), "duplicate key") {
			return store.ErrAlreadyExists
		}
		return err
	}
	return nil
}

func (s *PostgresStore) GetEnvVar(ctx context.Context, key, scope, scopeID string) (*store.EnvVar, error) {
	envVar := &store.EnvVar{}

	err := s.db.QueryRowContext(ctx, `
		SELECT id, key, value, scope, scope_id, description, sensitive, injection_mode, secret, created_at, updated_at, created_by
		FROM env_vars WHERE key = $1 AND scope = $2 AND scope_id = $3
	`, key, scope, scopeID).Scan(
		&envVar.ID, &envVar.Key, &envVar.Value, &envVar.Scope, &envVar.ScopeID,
		&envVar.Description, &envVar.Sensitive, &envVar.InjectionMode, &envVar.Secret,
		&envVar.Created, &envVar.Updated, &envVar.CreatedBy,
	)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, store.ErrNotFound
		}
		return nil, err
	}

	return envVar, nil
}

func (s *PostgresStore) UpdateEnvVar(ctx context.Context, envVar *store.EnvVar) error {
	envVar.Updated = time.Now()

	result, err := s.db.ExecContext(ctx, `
		UPDATE env_vars SET
			value = $1, description = $2, sensitive = $3, injection_mode = $4, secret = $5, updated_at = $6
		WHERE key = $7 AND scope = $8 AND scope_id = $9
	`,
		envVar.Value, envVar.Description, boolToInt(envVar.Sensitive), envVar.InjectionMode, boolToInt(envVar.Secret), envVar.Updated,
		envVar.Key, envVar.Scope, envVar.ScopeID,
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

func (s *PostgresStore) UpsertEnvVar(ctx context.Context, envVar *store.EnvVar) (bool, error) {
	now := time.Now()
	envVar.Updated = now

	// Check if it already exists
	existing, err := s.GetEnvVar(ctx, envVar.Key, envVar.Scope, envVar.ScopeID)
	if err != nil && err != store.ErrNotFound {
		return false, err
	}

	if existing != nil {
		// Update existing
		envVar.ID = existing.ID
		envVar.Created = existing.Created
		envVar.CreatedBy = existing.CreatedBy
		if err := s.UpdateEnvVar(ctx, envVar); err != nil {
			return false, err
		}
		return false, nil
	}

	// Create new
	envVar.Created = now
	if err := s.CreateEnvVar(ctx, envVar); err != nil {
		return false, err
	}
	return true, nil
}

func (s *PostgresStore) DeleteEnvVar(ctx context.Context, key, scope, scopeID string) error {
	result, err := s.db.ExecContext(ctx, "DELETE FROM env_vars WHERE key = $1 AND scope = $2 AND scope_id = $3", key, scope, scopeID)
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

func (s *PostgresStore) DeleteEnvVarsByScope(ctx context.Context, scope, scopeID string) (int, error) {
	result, err := s.db.ExecContext(ctx, "DELETE FROM env_vars WHERE scope = $1 AND scope_id = $2", scope, scopeID)
	if err != nil {
		return 0, err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return 0, err
	}
	return int(n), nil
}

func (s *PostgresStore) ListEnvVars(ctx context.Context, filter store.EnvVarFilter) ([]store.EnvVar, error) {
	var conditions []string
	var args []interface{}

	if filter.Scope != "" {
		conditions = append(conditions, fmt.Sprintf("scope = $%d", len(args)+1))
		args = append(args, filter.Scope)
	}
	if filter.ScopeID != "" {
		conditions = append(conditions, fmt.Sprintf("scope_id = $%d", len(args)+1))
		args = append(args, filter.ScopeID)
	}
	if filter.Key != "" {
		conditions = append(conditions, fmt.Sprintf("key = $%d", len(args)+1))
		args = append(args, filter.Key)
	}

	whereClause := ""
	if len(conditions) > 0 {
		whereClause = "WHERE " + strings.Join(conditions, " AND ")
	}

	query := fmt.Sprintf(`
		SELECT id, key, value, scope, scope_id, description, sensitive, injection_mode, secret, created_at, updated_at, created_by
		FROM env_vars %s ORDER BY key
	`, whereClause)

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var envVars []store.EnvVar
	for rows.Next() {
		var envVar store.EnvVar
		if err := rows.Scan(
			&envVar.ID, &envVar.Key, &envVar.Value, &envVar.Scope, &envVar.ScopeID,
			&envVar.Description, &envVar.Sensitive, &envVar.InjectionMode, &envVar.Secret,
			&envVar.Created, &envVar.Updated, &envVar.CreatedBy,
		); err != nil {
			return nil, err
		}
		envVars = append(envVars, envVar)
	}

	return envVars, nil
}
