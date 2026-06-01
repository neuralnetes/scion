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

func (s *PostgresStore) AddAllowListEntry(ctx context.Context, entry *store.AllowListEntry) error {
	if entry.Created.IsZero() {
		entry.Created = time.Now()
	}
	entry.Email = strings.ToLower(entry.Email)

	_, err := s.db.ExecContext(ctx, `
		INSERT INTO allow_list (id, email, note, added_by, invite_id, created)
		VALUES ($1, $2, $3, $4, $5, $6)
	`, entry.ID, entry.Email, entry.Note, entry.AddedBy, entry.InviteID, entry.Created)
	if err != nil {
		if strings.Contains(err.Error(), "UNIQUE constraint failed") || strings.Contains(err.Error(), "duplicate key") {
			return store.ErrAlreadyExists
		}
		return err
	}
	return nil
}

func (s *PostgresStore) RemoveAllowListEntry(ctx context.Context, email string) error {
	result, err := s.db.ExecContext(ctx, "DELETE FROM allow_list WHERE email = $1", strings.ToLower(email))
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

func (s *PostgresStore) GetAllowListEntry(ctx context.Context, email string) (*store.AllowListEntry, error) {
	entry := &store.AllowListEntry{}
	err := s.db.QueryRowContext(ctx, `
		SELECT id, email, note, added_by, invite_id, created
		FROM allow_list WHERE email = $1
	`, strings.ToLower(email)).Scan(
		&entry.ID, &entry.Email, &entry.Note, &entry.AddedBy, &entry.InviteID, &entry.Created,
	)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, store.ErrNotFound
		}
		return nil, err
	}
	return entry, nil
}

func (s *PostgresStore) ListAllowListEntries(ctx context.Context, opts store.ListOptions) (*store.ListResult[store.AllowListEntry], error) {
	var totalCount int
	if err := s.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM allow_list").Scan(&totalCount); err != nil {
		return nil, err
	}

	limit := opts.Limit
	if limit <= 0 {
		limit = 50
	}

	var conditions []string
	var args []interface{}

	if opts.Cursor != "" {
		var cursorCreated time.Time
		if err := s.db.QueryRowContext(ctx, "SELECT created FROM allow_list WHERE id = $1", opts.Cursor).Scan(&cursorCreated); err != nil {
			return nil, fmt.Errorf("invalid cursor: %w", err)
		}
		conditions = append(conditions, `(created < $1 OR (created = $2 AND id < $3))`)
		args = append(args, cursorCreated, cursorCreated, opts.Cursor)
	}

	whereClause := ""
	if len(conditions) > 0 {
		whereClause = "WHERE " + strings.Join(conditions, " AND ")
	}

	query := fmt.Sprintf(`
		SELECT id, email, note, added_by, invite_id, created
		FROM allow_list %s ORDER BY created DESC, id DESC LIMIT $%d
	`, whereClause, len(args)+1)
	args = append(args, limit+1)

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var entries []store.AllowListEntry
	for rows.Next() {
		var entry store.AllowListEntry
		if err := rows.Scan(&entry.ID, &entry.Email, &entry.Note, &entry.AddedBy, &entry.InviteID, &entry.Created); err != nil {
			return nil, err
		}
		entries = append(entries, entry)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if entries == nil {
		entries = []store.AllowListEntry{}
	}

	var nextCursor string
	if len(entries) > limit {
		nextCursor = entries[limit-1].ID
		entries = entries[:limit]
	}

	return &store.ListResult[store.AllowListEntry]{
		Items:      entries,
		TotalCount: totalCount,
		NextCursor: nextCursor,
	}, nil
}

func (s *PostgresStore) IsEmailAllowListed(ctx context.Context, email string) (bool, error) {
	var count int
	err := s.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM allow_list WHERE email = $1", strings.ToLower(email)).Scan(&count)
	if err != nil {
		return false, err
	}
	return count > 0, nil
}

func (s *PostgresStore) UpdateAllowListEntryInviteID(ctx context.Context, email string, inviteID string) error {
	result, err := s.db.ExecContext(ctx,
		"UPDATE allow_list SET invite_id = $1 WHERE email = $2",
		inviteID, strings.ToLower(email))
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

func (s *PostgresStore) ListAllowListEntriesWithInvites(ctx context.Context, opts store.ListOptions) (*store.ListResult[store.AllowListEntryWithInvite], error) {
	var totalCount int
	if err := s.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM allow_list").Scan(&totalCount); err != nil {
		return nil, err
	}

	limit := opts.Limit
	if limit <= 0 {
		limit = 50
	}

	var conditions []string
	var args []interface{}

	if opts.Cursor != "" {
		var cursorCreated time.Time
		if err := s.db.QueryRowContext(ctx, "SELECT created FROM allow_list WHERE id = $1", opts.Cursor).Scan(&cursorCreated); err != nil {
			return nil, fmt.Errorf("invalid cursor: %w", err)
		}
		conditions = append(conditions, `(a.created < $1 OR (a.created = $2 AND a.id < $3))`)
		args = append(args, cursorCreated, cursorCreated, opts.Cursor)
	}

	whereClause := ""
	if len(conditions) > 0 {
		whereClause = "WHERE " + strings.Join(conditions, " AND ")
	}

	query := fmt.Sprintf(`
		SELECT a.id, a.email, a.note, a.added_by, a.invite_id, a.created,
		       i.code_prefix, i.max_uses, i.use_count, i.expires_at, i.revoked
		FROM allow_list a
		LEFT JOIN invite_codes i ON a.invite_id = i.id AND a.invite_id != ''
		%s ORDER BY a.created DESC, a.id DESC LIMIT $%d
	`, whereClause, len(args)+1)
	args = append(args, limit+1)

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var entries []store.AllowListEntryWithInvite
	for rows.Next() {
		var entry store.AllowListEntryWithInvite
		var codePrefix sql.NullString
		var maxUses, useCount, revoked sql.NullInt64
		var expiresAt sql.NullTime
		if err := rows.Scan(
			&entry.ID, &entry.Email, &entry.Note, &entry.AddedBy, &entry.InviteID, &entry.Created,
			&codePrefix, &maxUses, &useCount, &expiresAt, &revoked,
		); err != nil {
			return nil, err
		}
		if codePrefix.Valid {
			entry.InviteCodePrefix = codePrefix.String
		}
		if maxUses.Valid {
			entry.InviteMaxUses = int(maxUses.Int64)
		}
		if useCount.Valid {
			entry.InviteUseCount = int(useCount.Int64)
		}
		if expiresAt.Valid {
			entry.InviteExpiresAt = expiresAt.Time
		}
		if revoked.Valid {
			entry.InviteRevoked = revoked.Int64 != 0
		}
		entries = append(entries, entry)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if entries == nil {
		entries = []store.AllowListEntryWithInvite{}
	}

	var nextCursor string
	if len(entries) > limit {
		nextCursor = entries[limit-1].ID
		entries = entries[:limit]
	}

	return &store.ListResult[store.AllowListEntryWithInvite]{
		Items:      entries,
		TotalCount: totalCount,
		NextCursor: nextCursor,
	}, nil
}

func (s *PostgresStore) BulkAddAllowListEntries(ctx context.Context, entries []*store.AllowListEntry) (int, int, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, 0, err
	}
	defer tx.Rollback()

	stmt, err := tx.PrepareContext(ctx, `
		INSERT INTO allow_list (id, email, note, added_by, invite_id, created)
		VALUES ($1, $2, $3, $4, $5, $6)
		ON CONFLICT (email) DO NOTHING
	`)
	if err != nil {
		return 0, 0, err
	}
	defer stmt.Close()

	added := 0
	skipped := 0
	now := time.Now()

	for _, entry := range entries {
		entry.Email = strings.ToLower(entry.Email)
		if entry.Created.IsZero() {
			entry.Created = now
		}
		result, err := stmt.ExecContext(ctx, entry.ID, entry.Email, entry.Note, entry.AddedBy, entry.InviteID, entry.Created)
		if err != nil {
			return added, skipped, err
		}
		rows, _ := result.RowsAffected()
		if rows > 0 {
			added++
		} else {
			skipped++
		}
	}

	if err := tx.Commit(); err != nil {
		return 0, 0, err
	}
	return added, skipped, nil
}

func (s *PostgresStore) ListEmailDomains(ctx context.Context) ([]string, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT DISTINCT split_part(email, '@', 2) AS domain
		FROM users
		WHERE email LIKE '%@%'
		ORDER BY domain
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var domains []string
	for rows.Next() {
		var domain string
		if err := rows.Scan(&domain); err != nil {
			return nil, err
		}
		domains = append(domains, domain)
	}
	return domains, rows.Err()
}
