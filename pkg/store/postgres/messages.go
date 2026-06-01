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

// Package postgres provides a Postgres implementation of the Store interface.
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
// Message Operations
// ============================================================================

// CreateMessage persists a new message.
func (s *PostgresStore) CreateMessage(ctx context.Context, msg *store.Message) error {
	if msg.ID == "" || msg.ProjectID == "" || msg.Msg == "" {
		return store.ErrInvalidInput
	}
	if msg.CreatedAt.IsZero() {
		msg.CreatedAt = time.Now()
	}

	_, err := s.db.ExecContext(ctx, `
		INSERT INTO messages (
			id, project_id, sender, sender_id, recipient, recipient_id,
			msg, type, urgent, broadcasted, read, agent_id, group_id, created_at
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14)
	`,
		msg.ID, msg.ProjectID, msg.Sender, msg.SenderID, msg.Recipient, msg.RecipientID,
		msg.Msg, msg.Type,
		boolToInt(msg.Urgent), boolToInt(msg.Broadcasted), boolToInt(msg.Read),
		msg.AgentID, msg.GroupID, msg.CreatedAt,
	)
	if err != nil {
		if strings.Contains(err.Error(), "UNIQUE constraint failed") || strings.Contains(err.Error(), "duplicate key") {
			return store.ErrAlreadyExists
		}
		return err
	}
	return nil
}

// GetMessage returns a single message by ID.
func (s *PostgresStore) GetMessage(ctx context.Context, id string) (*store.Message, error) {
	row := s.db.QueryRowContext(ctx, `
		SELECT id, project_id, sender, sender_id, recipient, recipient_id,
			msg, type, urgent, broadcasted, read, agent_id, group_id, created_at
		FROM messages
		WHERE id = $1
	`, id)

	var msg store.Message
	var urgent, broadcasted, read int
	if err := row.Scan(
		&msg.ID, &msg.ProjectID, &msg.Sender, &msg.SenderID, &msg.Recipient, &msg.RecipientID,
		&msg.Msg, &msg.Type, &urgent, &broadcasted, &read,
		&msg.AgentID, &msg.GroupID, &msg.CreatedAt,
	); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, store.ErrNotFound
		}
		return nil, err
	}
	msg.Urgent = urgent != 0
	msg.Broadcasted = broadcasted != 0
	msg.Read = read != 0
	return &msg, nil
}

// ListMessages returns messages matching the given filter, ordered by created_at DESC.
func (s *PostgresStore) ListMessages(ctx context.Context, filter store.MessageFilter, opts store.ListOptions) (*store.ListResult[store.Message], error) {
	var conditions []string
	var args []interface{}

	if filter.ProjectID != "" {
		args = append(args, filter.ProjectID)
		conditions = append(conditions, fmt.Sprintf("project_id = $%d", len(args)))
	}
	if filter.AgentID != "" {
		args = append(args, filter.AgentID)
		conditions = append(conditions, fmt.Sprintf("agent_id = $%d", len(args)))
	}
	if filter.RecipientID != "" {
		args = append(args, filter.RecipientID)
		conditions = append(conditions, fmt.Sprintf("recipient_id = $%d", len(args)))
	}
	if filter.SenderID != "" {
		args = append(args, filter.SenderID)
		conditions = append(conditions, fmt.Sprintf("sender_id = $%d", len(args)))
	}
	if filter.ParticipantID != "" {
		args = append(args, filter.ParticipantID, filter.ParticipantID)
		conditions = append(conditions, fmt.Sprintf("(recipient_id = $%d OR sender_id = $%d)", len(args)-1, len(args)))
	}
	if filter.OnlyUnread {
		conditions = append(conditions, "read = 0")
	}
	if filter.Type != "" {
		args = append(args, filter.Type)
		conditions = append(conditions, fmt.Sprintf("type = $%d", len(args)))
	}

	whereClause := ""
	if len(conditions) > 0 {
		whereClause = "WHERE " + strings.Join(conditions, " AND ")
	}

	var totalCount int
	countQuery := fmt.Sprintf("SELECT COUNT(*) FROM messages %s", whereClause)
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

	args = append(args, limit+1)
	query := fmt.Sprintf(`
		SELECT id, project_id, sender, sender_id, recipient, recipient_id,
			msg, type, urgent, broadcasted, read, agent_id, group_id, created_at
		FROM messages %s ORDER BY created_at DESC LIMIT $%d
	`, whereClause, len(args))

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var msgs []store.Message
	for rows.Next() {
		var msg store.Message
		var urgent, broadcasted, read int
		if err := rows.Scan(
			&msg.ID, &msg.ProjectID, &msg.Sender, &msg.SenderID, &msg.Recipient, &msg.RecipientID,
			&msg.Msg, &msg.Type, &urgent, &broadcasted, &read,
			&msg.AgentID, &msg.GroupID, &msg.CreatedAt,
		); err != nil {
			return nil, err
		}
		msg.Urgent = urgent != 0
		msg.Broadcasted = broadcasted != 0
		msg.Read = read != 0
		msgs = append(msgs, msg)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	result := &store.ListResult[store.Message]{
		Items:      msgs,
		TotalCount: totalCount,
	}
	if len(msgs) > limit {
		result.Items = msgs[:limit]
		result.NextCursor = msgs[limit-1].ID
	}
	return result, nil
}

// MarkMessageRead marks a message as read.
func (s *PostgresStore) MarkMessageRead(ctx context.Context, id string) error {
	result, err := s.db.ExecContext(ctx, `
		UPDATE messages SET read = 1 WHERE id = $1
	`, id)
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return store.ErrNotFound
	}
	return nil
}

// MarkAllMessagesRead marks all messages for a recipient as read.
func (s *PostgresStore) MarkAllMessagesRead(ctx context.Context, recipientID string) error {
	_, err := s.db.ExecContext(ctx, `
		UPDATE messages SET read = 1 WHERE recipient_id = $1
	`, recipientID)
	return err
}

// PurgeOldMessages removes read messages older than readCutoff and unread messages
// older than unreadCutoff. Returns the number of messages removed.
func (s *PostgresStore) PurgeOldMessages(ctx context.Context, readCutoff time.Time, unreadCutoff time.Time) (int, error) {
	result, err := s.db.ExecContext(ctx, `
		DELETE FROM messages
		WHERE (read = 1 AND created_at < $1) OR (read = 0 AND created_at < $2)
	`, readCutoff, unreadCutoff)
	if err != nil {
		return 0, err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return 0, err
	}
	return int(n), nil
}
