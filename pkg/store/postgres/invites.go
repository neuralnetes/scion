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

func (s *PostgresStore) CreateInviteCode(ctx context.Context, invite *store.InviteCode) error {
	if invite.Created.IsZero() {
		invite.Created = time.Now()
	}

	_, err := s.db.ExecContext(ctx, `
		INSERT INTO invite_codes (id, code_hash, code_prefix, max_uses, use_count, expires_at, revoked, created_by, note, created)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
	`, invite.ID, invite.CodeHash, invite.CodePrefix, invite.MaxUses, invite.UseCount,
		invite.ExpiresAt, boolToInt(invite.Revoked), invite.CreatedBy, invite.Note, invite.Created)
	if err != nil {
		if strings.Contains(err.Error(), "UNIQUE constraint failed") || strings.Contains(err.Error(), "duplicate key") {
			return store.ErrAlreadyExists
		}
		return err
	}
	return nil
}

func (s *PostgresStore) GetInviteCodeByHash(ctx context.Context, codeHash string) (*store.InviteCode, error) {
	invite := &store.InviteCode{}
	var revoked int
	err := s.db.QueryRowContext(ctx, `
		SELECT id, code_hash, code_prefix, max_uses, use_count, expires_at, revoked, created_by, note, created
		FROM invite_codes WHERE code_hash = $1
	`, codeHash).Scan(
		&invite.ID, &invite.CodeHash, &invite.CodePrefix, &invite.MaxUses, &invite.UseCount,
		&invite.ExpiresAt, &revoked, &invite.CreatedBy, &invite.Note, &invite.Created,
	)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, store.ErrNotFound
		}
		return nil, err
	}
	invite.Revoked = revoked != 0
	return invite, nil
}

func (s *PostgresStore) GetInviteCode(ctx context.Context, id string) (*store.InviteCode, error) {
	invite := &store.InviteCode{}
	var revoked int
	err := s.db.QueryRowContext(ctx, `
		SELECT id, code_hash, code_prefix, max_uses, use_count, expires_at, revoked, created_by, note, created
		FROM invite_codes WHERE id = $1
	`, id).Scan(
		&invite.ID, &invite.CodeHash, &invite.CodePrefix, &invite.MaxUses, &invite.UseCount,
		&invite.ExpiresAt, &revoked, &invite.CreatedBy, &invite.Note, &invite.Created,
	)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, store.ErrNotFound
		}
		return nil, err
	}
	invite.Revoked = revoked != 0
	return invite, nil
}

func (s *PostgresStore) ListInviteCodes(ctx context.Context, opts store.ListOptions) (*store.ListResult[store.InviteCode], error) {
	var totalCount int
	if err := s.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM invite_codes").Scan(&totalCount); err != nil {
		return nil, err
	}

	limit := opts.Limit
	if limit <= 0 {
		limit = 50
	}

	var conditions []string
	var args []interface{}

	if opts.Cursor != "" {
		conditions = append(conditions, `(created < (SELECT created FROM invite_codes WHERE id = $1)
			OR (created = (SELECT created FROM invite_codes WHERE id = $2) AND id < $3))`)
		args = append(args, opts.Cursor, opts.Cursor, opts.Cursor)
	}

	whereClause := ""
	if len(conditions) > 0 {
		whereClause = "WHERE " + strings.Join(conditions, " AND ")
	}

	query := fmt.Sprintf(`
		SELECT id, code_prefix, max_uses, use_count, expires_at, revoked, created_by, note, created
		FROM invite_codes %s ORDER BY created DESC, id DESC LIMIT $%d
	`, whereClause, len(args)+1)
	args = append(args, limit+1)

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var invites []store.InviteCode
	for rows.Next() {
		var invite store.InviteCode
		var revoked int
		if err := rows.Scan(
			&invite.ID, &invite.CodePrefix, &invite.MaxUses, &invite.UseCount,
			&invite.ExpiresAt, &revoked, &invite.CreatedBy, &invite.Note, &invite.Created,
		); err != nil {
			return nil, err
		}
		invite.Revoked = revoked != 0
		invites = append(invites, invite)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if invites == nil {
		invites = []store.InviteCode{}
	}

	var nextCursor string
	if len(invites) > limit {
		nextCursor = invites[limit-1].ID
		invites = invites[:limit]
	}

	return &store.ListResult[store.InviteCode]{
		Items:      invites,
		TotalCount: totalCount,
		NextCursor: nextCursor,
	}, nil
}

func (s *PostgresStore) IncrementInviteUseCount(ctx context.Context, id string) error {
	result, err := s.db.ExecContext(ctx, `
		UPDATE invite_codes SET use_count = use_count + 1
		WHERE id = $1 AND revoked = 0 AND expires_at > NOW()
		  AND (max_uses = 0 OR use_count < max_uses)
	`, id)
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

func (s *PostgresStore) RevokeInviteCode(ctx context.Context, id string) error {
	result, err := s.db.ExecContext(ctx, "UPDATE invite_codes SET revoked = 1 WHERE id = $1", id)
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

func (s *PostgresStore) DeleteInviteCode(ctx context.Context, id string) error {
	result, err := s.db.ExecContext(ctx, "DELETE FROM invite_codes WHERE id = $1", id)
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

func (s *PostgresStore) GetInviteStats(ctx context.Context) (*store.InviteStats, error) {
	stats := &store.InviteStats{}

	// Count pending (active, not expired, not exhausted) invites
	err := s.db.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM invite_codes
		WHERE revoked = 0
		  AND expires_at > NOW()
		  AND (max_uses = 0 OR use_count < max_uses)
	`).Scan(&stats.PendingInvites)
	if err != nil {
		return nil, err
	}

	// Total redemptions across all invites
	err = s.db.QueryRowContext(ctx, `
		SELECT COALESCE(SUM(use_count), 0) FROM invite_codes
	`).Scan(&stats.TotalRedemptions)
	if err != nil {
		return nil, err
	}

	// Allow list count
	err = s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM allow_list`).Scan(&stats.AllowListCount)
	if err != nil {
		return nil, err
	}

	// Recent invites that have been redeemed (use_count > 0), ordered by most recently created
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, code_prefix, use_count, max_uses, expires_at, note, created
		FROM invite_codes
		WHERE use_count > 0
		ORDER BY created DESC
		LIMIT 10
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	for rows.Next() {
		var info store.InviteCodeInfo
		if err := rows.Scan(&info.ID, &info.CodePrefix, &info.UseCount, &info.MaxUses, &info.ExpiresAt, &info.Note, &info.Created); err != nil {
			return nil, err
		}
		stats.RecentRedemptions = append(stats.RecentRedemptions, info)
	}
	if stats.RecentRedemptions == nil {
		stats.RecentRedemptions = []store.InviteCodeInfo{}
	}

	return stats, rows.Err()
}
