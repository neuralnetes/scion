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
	"strings"
	"time"

	"github.com/GoogleCloudPlatform/scion/pkg/store"
)

// ============================================================================
// User Access Token Operations
// ============================================================================

func (s *PostgresStore) CreateUserAccessToken(ctx context.Context, token *store.UserAccessToken) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO user_access_tokens (
			id, user_id, name, prefix, key_hash, project_id, scopes,
			revoked, expires_at, last_used, created_at
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)
	`,
		token.ID, token.UserID, token.Name, token.Prefix, token.KeyHash,
		token.ProjectID, marshalJSON(token.Scopes),
		boolToInt(token.Revoked), ptrToNullTime(token.ExpiresAt), ptrToNullTime(token.LastUsed), token.Created,
	)
	if err != nil {
		if strings.Contains(err.Error(), "UNIQUE constraint failed") || strings.Contains(err.Error(), "duplicate key") {
			return store.ErrAlreadyExists
		}
		if strings.Contains(err.Error(), "foreign key constraint") || strings.Contains(err.Error(), "FOREIGN KEY constraint failed") {
			return store.ErrInvalidInput
		}
		return err
	}
	return nil
}

func (s *PostgresStore) GetUserAccessToken(ctx context.Context, id string) (*store.UserAccessToken, error) {
	token := &store.UserAccessToken{}
	var scopes string
	var expiresAt, lastUsed sql.NullTime

	err := s.db.QueryRowContext(ctx, `
		SELECT id, user_id, name, prefix, key_hash, project_id, scopes,
			revoked, expires_at, last_used, created_at
		FROM user_access_tokens WHERE id = $1
	`, id).Scan(
		&token.ID, &token.UserID, &token.Name, &token.Prefix, &token.KeyHash,
		&token.ProjectID, &scopes,
		&token.Revoked, &expiresAt, &lastUsed, &token.Created,
	)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, store.ErrNotFound
		}
		return nil, err
	}

	unmarshalJSON(scopes, &token.Scopes)
	if expiresAt.Valid {
		token.ExpiresAt = &expiresAt.Time
	}
	if lastUsed.Valid {
		token.LastUsed = &lastUsed.Time
	}
	return token, nil
}

func (s *PostgresStore) GetUserAccessTokenByHash(ctx context.Context, hash string) (*store.UserAccessToken, error) {
	token := &store.UserAccessToken{}
	var scopes string
	var expiresAt, lastUsed sql.NullTime

	err := s.db.QueryRowContext(ctx, `
		SELECT id, user_id, name, prefix, key_hash, project_id, scopes,
			revoked, expires_at, last_used, created_at
		FROM user_access_tokens WHERE key_hash = $1
	`, hash).Scan(
		&token.ID, &token.UserID, &token.Name, &token.Prefix, &token.KeyHash,
		&token.ProjectID, &scopes,
		&token.Revoked, &expiresAt, &lastUsed, &token.Created,
	)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, store.ErrNotFound
		}
		return nil, err
	}

	unmarshalJSON(scopes, &token.Scopes)
	if expiresAt.Valid {
		token.ExpiresAt = &expiresAt.Time
	}
	if lastUsed.Valid {
		token.LastUsed = &lastUsed.Time
	}
	return token, nil
}

func (s *PostgresStore) UpdateUserAccessTokenLastUsed(ctx context.Context, id string) error {
	_, err := s.db.ExecContext(ctx,
		"UPDATE user_access_tokens SET last_used = $1 WHERE id = $2",
		time.Now(), id,
	)
	return err
}

func (s *PostgresStore) RevokeUserAccessToken(ctx context.Context, id string) error {
	result, err := s.db.ExecContext(ctx,
		"UPDATE user_access_tokens SET revoked = 1 WHERE id = $1", id,
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

func (s *PostgresStore) DeleteUserAccessToken(ctx context.Context, id string) error {
	result, err := s.db.ExecContext(ctx, "DELETE FROM user_access_tokens WHERE id = $1", id)
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

func (s *PostgresStore) ListUserAccessTokens(ctx context.Context, userID string) ([]store.UserAccessToken, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, user_id, name, prefix, project_id, scopes,
			revoked, expires_at, last_used, created_at
		FROM user_access_tokens WHERE user_id = $1
		ORDER BY created_at DESC
	`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var tokens []store.UserAccessToken
	for rows.Next() {
		var token store.UserAccessToken
		var scopes string
		var expiresAt, lastUsed sql.NullTime

		if err := rows.Scan(
			&token.ID, &token.UserID, &token.Name, &token.Prefix,
			&token.ProjectID, &scopes,
			&token.Revoked, &expiresAt, &lastUsed, &token.Created,
		); err != nil {
			return nil, err
		}

		unmarshalJSON(scopes, &token.Scopes)
		if expiresAt.Valid {
			token.ExpiresAt = &expiresAt.Time
		}
		if lastUsed.Valid {
			token.LastUsed = &lastUsed.Time
		}
		tokens = append(tokens, token)
	}
	return tokens, nil
}

func (s *PostgresStore) CountUserAccessTokens(ctx context.Context, userID string) (int, error) {
	var count int
	err := s.db.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM user_access_tokens WHERE user_id = $1 AND revoked = 0",
		userID,
	).Scan(&count)
	return count, err
}
