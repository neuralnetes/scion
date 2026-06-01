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
	"strconv"
	"strings"
	"time"

	"github.com/GoogleCloudPlatform/scion/pkg/store"
)

func (s *PostgresStore) CreateUser(ctx context.Context, user *store.User) error {
	now := time.Now()
	user.Created = now

	_, err := s.db.ExecContext(ctx, `
		INSERT INTO users (id, email, display_name, avatar_url, role, status, preferences, created_at, last_login)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
	`,
		user.ID, user.Email, user.DisplayName, user.AvatarURL, user.Role, user.Status,
		marshalJSON(user.Preferences), user.Created, user.LastLogin,
	)
	if err != nil {
		if strings.Contains(err.Error(), "unique constraint") || strings.Contains(err.Error(), "duplicate key") {
			return store.ErrAlreadyExists
		}
		return err
	}
	return nil
}

func (s *PostgresStore) GetUser(ctx context.Context, id string) (*store.User, error) {
	user := &store.User{}
	var preferences string
	var lastLogin, lastSeen sql.NullTime

	err := s.db.QueryRowContext(ctx, `
		SELECT id, email, display_name, avatar_url, role, status, preferences, created_at, last_login, last_seen
		FROM users WHERE id = $1
	`, id).Scan(
		&user.ID, &user.Email, &user.DisplayName, &user.AvatarURL, &user.Role, &user.Status,
		&preferences, &user.Created, &lastLogin, &lastSeen,
	)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, store.ErrNotFound
		}
		return nil, err
	}

	if lastLogin.Valid {
		user.LastLogin = lastLogin.Time
	}
	if lastSeen.Valid {
		user.LastSeen = lastSeen.Time
	}
	unmarshalJSON(preferences, &user.Preferences)

	return user, nil
}

func (s *PostgresStore) GetUserByEmail(ctx context.Context, email string) (*store.User, error) {
	var id string
	err := s.db.QueryRowContext(ctx, "SELECT id FROM users WHERE email = $1", email).Scan(&id)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, store.ErrNotFound
		}
		return nil, err
	}
	return s.GetUser(ctx, id)
}

func (s *PostgresStore) UpdateUser(ctx context.Context, user *store.User) error {
	result, err := s.db.ExecContext(ctx, `
		UPDATE users SET
			email = $1, display_name = $2, avatar_url = $3,
			role = $4, status = $5, preferences = $6, last_login = $7, last_seen = $8
		WHERE id = $9
	`,
		user.Email, user.DisplayName, user.AvatarURL,
		user.Role, user.Status, marshalJSON(user.Preferences), user.LastLogin, user.LastSeen,
		user.ID,
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

func (s *PostgresStore) UpdateUserLastSeen(ctx context.Context, id string, t time.Time) error {
	_, err := s.db.ExecContext(ctx, `UPDATE users SET last_seen = $1 WHERE id = $2`, t, id)
	return err
}

func (s *PostgresStore) DeleteUser(ctx context.Context, id string) error {
	result, err := s.db.ExecContext(ctx, "DELETE FROM users WHERE id = $1", id)
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

func (s *PostgresStore) ListUsers(ctx context.Context, filter store.UserFilter, opts store.ListOptions) (*store.ListResult[store.User], error) {
	var conditions []string
	var args []interface{}

	if filter.Role != "" {
		args = append(args, filter.Role)
		conditions = append(conditions, fmt.Sprintf("role = $%d", len(args)))
	}
	if filter.Status != "" {
		args = append(args, filter.Status)
		conditions = append(conditions, fmt.Sprintf("status = $%d", len(args)))
	}
	if filter.Search != "" {
		pattern := "%" + filter.Search + "%"
		args = append(args, pattern, pattern)
		conditions = append(conditions, fmt.Sprintf("(email LIKE $%d OR display_name LIKE $%d)", len(args)-1, len(args)))
	}

	whereClause := ""
	if len(conditions) > 0 {
		whereClause = "WHERE " + strings.Join(conditions, " AND ")
	}

	var totalCount int
	countQuery := fmt.Sprintf("SELECT COUNT(*) FROM users %s", whereClause)
	if err := s.db.QueryRowContext(ctx, countQuery, args...).Scan(&totalCount); err != nil {
		return nil, err
	}

	limit := opts.Limit
	if limit <= 0 {
		limit = 50
	}

	if limit > 200 {
		limit = 200
	}

	offset := 0
	if opts.Cursor != "" {
		if parsed, err := strconv.Atoi(opts.Cursor); err == nil && parsed > 0 {
			offset = parsed
		}
	}

	// Map sort field to column name (whitelist to prevent SQL injection)
	orderColumn := "created_at"
	orderDir := "DESC"
	switch opts.SortBy {
	case "name":
		orderColumn = "display_name"
		orderDir = "ASC" // default ascending for name
	case "lastSeen":
		orderColumn = "last_seen"
	case "created":
		orderColumn = "created_at"
	}
	if opts.SortDir == "asc" {
		orderDir = "ASC"
	} else if opts.SortDir == "desc" {
		orderDir = "DESC"
	}

	args = append(args, limit+1, offset)
	query := fmt.Sprintf(`
		SELECT id, email, display_name, avatar_url, role, status, preferences, created_at, last_login, last_seen
		FROM users %s ORDER BY %s %s LIMIT $%d OFFSET $%d
	`, whereClause, orderColumn, orderDir, len(args)-1, len(args))

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var users []store.User
	for rows.Next() {
		var user store.User
		var preferences string
		var lastLogin, lastSeen sql.NullTime

		if err := rows.Scan(
			&user.ID, &user.Email, &user.DisplayName, &user.AvatarURL, &user.Role, &user.Status,
			&preferences, &user.Created, &lastLogin, &lastSeen,
		); err != nil {
			return nil, err
		}

		if lastLogin.Valid {
			user.LastLogin = lastLogin.Time
		}
		if lastSeen.Valid {
			user.LastSeen = lastSeen.Time
		}
		unmarshalJSON(preferences, &user.Preferences)

		users = append(users, user)
	}

	result := &store.ListResult[store.User]{
		Items:      users,
		TotalCount: totalCount,
	}

	// Handle pagination: if we got more than limit, there's a next page
	if len(users) > limit {
		result.Items = users[:limit]
		result.NextCursor = strconv.Itoa(offset + limit)
	}

	return result, nil
}
