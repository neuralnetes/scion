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
	"fmt"
	"strings"
	"time"

	"github.com/GoogleCloudPlatform/scion/pkg/store"
)

func (s *PostgresStore) CreateGCPServiceAccount(ctx context.Context, sa *store.GCPServiceAccount) error {
	if sa.CreatedAt.IsZero() {
		sa.CreatedAt = time.Now()
	}

	scopesStr := strings.Join(sa.DefaultScopes, ",")

	_, err := s.db.ExecContext(ctx, `
		INSERT INTO gcp_service_accounts (id, scope, scope_id, email, project_id, display_name, default_scopes, verified, verified_at, created_by, created_at, managed, managed_by)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13)`,
		sa.ID, sa.Scope, sa.ScopeID, sa.Email, sa.ProjectID, sa.DisplayName,
		scopesStr, boolToInt(sa.Verified), nullableTime(sa.VerifiedAt), sa.CreatedBy, sa.CreatedAt,
		boolToInt(sa.Managed), sa.ManagedBy,
	)
	if err != nil {
		if strings.Contains(err.Error(), "unique constraint") || strings.Contains(err.Error(), "duplicate key") {
			return store.ErrAlreadyExists
		}
		return err
	}
	return nil
}

func (s *PostgresStore) GetGCPServiceAccount(ctx context.Context, id string) (*store.GCPServiceAccount, error) {
	var sa store.GCPServiceAccount
	var scopesStr string
	var verifiedAt sql.NullTime

	err := s.db.QueryRowContext(ctx, `
		SELECT id, scope, scope_id, email, project_id, display_name, default_scopes, verified, verified_at, created_by, created_at, managed, managed_by
		FROM gcp_service_accounts WHERE id = $1`, id,
	).Scan(&sa.ID, &sa.Scope, &sa.ScopeID, &sa.Email, &sa.ProjectID, &sa.DisplayName,
		&scopesStr, &sa.Verified, &verifiedAt, &sa.CreatedBy, &sa.CreatedAt,
		&sa.Managed, &sa.ManagedBy,
	)
	if err == sql.ErrNoRows {
		return nil, store.ErrNotFound
	}
	if err != nil {
		return nil, err
	}

	if scopesStr != "" {
		sa.DefaultScopes = strings.Split(scopesStr, ",")
	}
	if verifiedAt.Valid {
		sa.VerifiedAt = verifiedAt.Time
	}

	return &sa, nil
}

func (s *PostgresStore) UpdateGCPServiceAccount(ctx context.Context, sa *store.GCPServiceAccount) error {
	scopesStr := strings.Join(sa.DefaultScopes, ",")

	result, err := s.db.ExecContext(ctx, `
		UPDATE gcp_service_accounts
		SET email = $1, project_id = $2, display_name = $3, default_scopes = $4, verified = $5, verified_at = $6, managed = $7, managed_by = $8
		WHERE id = $9`,
		sa.Email, sa.ProjectID, sa.DisplayName, scopesStr, boolToInt(sa.Verified), nullableTime(sa.VerifiedAt),
		boolToInt(sa.Managed), sa.ManagedBy, sa.ID,
	)
	if err != nil {
		return err
	}
	rows, _ := result.RowsAffected()
	if rows == 0 {
		return store.ErrNotFound
	}
	return nil
}

func (s *PostgresStore) DeleteGCPServiceAccount(ctx context.Context, id string) error {
	result, err := s.db.ExecContext(ctx, `DELETE FROM gcp_service_accounts WHERE id = $1`, id)
	if err != nil {
		return err
	}
	rows, _ := result.RowsAffected()
	if rows == 0 {
		return store.ErrNotFound
	}
	return nil
}

func (s *PostgresStore) ListGCPServiceAccounts(ctx context.Context, filter store.GCPServiceAccountFilter) ([]store.GCPServiceAccount, error) {
	query := `SELECT id, scope, scope_id, email, project_id, display_name, default_scopes, verified, verified_at, created_by, created_at, managed, managed_by FROM gcp_service_accounts WHERE 1=1`
	var args []interface{}
	n := 1

	if filter.Scope != "" {
		query += fmt.Sprintf(` AND scope = $%d`, n)
		args = append(args, filter.Scope)
		n++
	}
	if filter.ScopeID != "" {
		query += fmt.Sprintf(` AND scope_id = $%d`, n)
		args = append(args, filter.ScopeID)
		n++
	}
	if filter.Email != "" {
		query += fmt.Sprintf(` AND email = $%d`, n)
		args = append(args, filter.Email)
		n++
	}
	if filter.Managed != nil {
		query += fmt.Sprintf(` AND managed = $%d`, n)
		args = append(args, boolToInt(*filter.Managed))
		n++
	}

	query += ` ORDER BY created_at DESC`

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var results []store.GCPServiceAccount
	for rows.Next() {
		var sa store.GCPServiceAccount
		var scopesStr string
		var verifiedAt sql.NullTime

		if err := rows.Scan(&sa.ID, &sa.Scope, &sa.ScopeID, &sa.Email, &sa.ProjectID, &sa.DisplayName,
			&scopesStr, &sa.Verified, &verifiedAt, &sa.CreatedBy, &sa.CreatedAt,
			&sa.Managed, &sa.ManagedBy,
		); err != nil {
			return nil, err
		}

		if scopesStr != "" {
			sa.DefaultScopes = strings.Split(scopesStr, ",")
		}
		if verifiedAt.Valid {
			sa.VerifiedAt = verifiedAt.Time
		}

		results = append(results, sa)
	}

	return results, rows.Err()
}

func (s *PostgresStore) CountGCPServiceAccounts(ctx context.Context, filter store.GCPServiceAccountFilter) (int, error) {
	query := `SELECT COUNT(*) FROM gcp_service_accounts WHERE 1=1`
	var args []interface{}
	n := 1

	if filter.Scope != "" {
		query += fmt.Sprintf(` AND scope = $%d`, n)
		args = append(args, filter.Scope)
		n++
	}
	if filter.ScopeID != "" {
		query += fmt.Sprintf(` AND scope_id = $%d`, n)
		args = append(args, filter.ScopeID)
		n++
	}
	if filter.Email != "" {
		query += fmt.Sprintf(` AND email = $%d`, n)
		args = append(args, filter.Email)
		n++
	}
	if filter.Managed != nil {
		query += fmt.Sprintf(` AND managed = $%d`, n)
		args = append(args, boolToInt(*filter.Managed))
		n++
	}

	var count int
	err := s.db.QueryRowContext(ctx, query, args...).Scan(&count)
	return count, err
}
