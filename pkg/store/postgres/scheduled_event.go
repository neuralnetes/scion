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
// Scheduled Event Operations
// ============================================================================

// CreateScheduledEvent creates a new scheduled event.
func (s *PostgresStore) CreateScheduledEvent(ctx context.Context, event *store.ScheduledEvent) error {
	if event.ID == "" || event.ProjectID == "" || event.EventType == "" {
		return store.ErrInvalidInput
	}

	now := time.Now()
	if event.CreatedAt.IsZero() {
		event.CreatedAt = now
	}
	if event.Status == "" {
		event.Status = store.ScheduledEventPending
	}

	_, err := s.db.ExecContext(ctx, `
		INSERT INTO scheduled_events (
			id, project_id, event_type, fire_at, payload, status,
			created_at, created_by, fired_at, error, schedule_id
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)
	`,
		event.ID, event.ProjectID, event.EventType, event.FireAt, event.Payload, event.Status,
		event.CreatedAt, nullableString(event.CreatedBy), nullableTime(timeFromPtr(event.FiredAt)), nullableString(event.Error),
		nullableString(event.ScheduleID),
	)
	if err != nil {
		if strings.Contains(err.Error(), "UNIQUE constraint failed") || strings.Contains(err.Error(), "duplicate key") {
			return store.ErrAlreadyExists
		}
		if strings.Contains(err.Error(), "foreign key constraint") || strings.Contains(err.Error(), "FOREIGN KEY constraint failed") {
			return fmt.Errorf("project %s does not exist: %w", event.ProjectID, store.ErrInvalidInput)
		}
		return err
	}
	return nil
}

// GetScheduledEvent retrieves a scheduled event by ID.
func (s *PostgresStore) GetScheduledEvent(ctx context.Context, id string) (*store.ScheduledEvent, error) {
	event := &store.ScheduledEvent{}
	var createdBy sql.NullString
	var firedAt sql.NullTime
	var errMsg sql.NullString
	var scheduleID sql.NullString

	err := s.db.QueryRowContext(ctx, `
		SELECT id, project_id, event_type, fire_at, payload, status,
			created_at, created_by, fired_at, error, schedule_id
		FROM scheduled_events WHERE id = $1
	`, id).Scan(
		&event.ID, &event.ProjectID, &event.EventType, &event.FireAt, &event.Payload, &event.Status,
		&event.CreatedAt, &createdBy, &firedAt, &errMsg, &scheduleID,
	)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, store.ErrNotFound
		}
		return nil, err
	}

	if createdBy.Valid {
		event.CreatedBy = createdBy.String
	}
	if firedAt.Valid {
		event.FiredAt = &firedAt.Time
	}
	if errMsg.Valid {
		event.Error = errMsg.String
	}
	if scheduleID.Valid {
		event.ScheduleID = scheduleID.String
	}

	return event, nil
}

// ListPendingScheduledEvents returns all events with status "pending",
// ordered by fire_at ASC.
func (s *PostgresStore) ListPendingScheduledEvents(ctx context.Context) ([]store.ScheduledEvent, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, project_id, event_type, fire_at, payload, status,
			created_at, created_by, fired_at, error, schedule_id
		FROM scheduled_events
		WHERE status = $1
		ORDER BY fire_at ASC
	`, store.ScheduledEventPending)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	return scanScheduledEvents(rows)
}

// UpdateScheduledEventStatus updates the status and optional error for an event.
func (s *PostgresStore) UpdateScheduledEventStatus(ctx context.Context, id string, status string, firedAt *time.Time, errMsg string) error {
	_, err := s.db.ExecContext(ctx, `
		UPDATE scheduled_events SET status = $1, fired_at = $2, error = $3
		WHERE id = $4
	`, status, nullableTime(timeFromPtr(firedAt)), nullableString(errMsg), id)
	return err
}

// CancelScheduledEvent marks an event as cancelled.
// Returns ErrNotFound if the event doesn't exist or is not pending.
func (s *PostgresStore) CancelScheduledEvent(ctx context.Context, id string) error {
	result, err := s.db.ExecContext(ctx, `
		UPDATE scheduled_events SET status = $1
		WHERE id = $2 AND status = $3
	`, store.ScheduledEventCancelled, id, store.ScheduledEventPending)
	if err != nil {
		return err
	}

	rows, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if rows == 0 {
		return store.ErrNotFound
	}
	return nil
}

// ListScheduledEvents returns events matching the filter criteria.
func (s *PostgresStore) ListScheduledEvents(ctx context.Context, filter store.ScheduledEventFilter, opts store.ListOptions) (*store.ListResult[store.ScheduledEvent], error) {
	var conditions []string
	var args []interface{}

	if filter.ProjectID != "" {
		conditions = append(conditions, fmt.Sprintf("project_id = $%d", len(args)+1))
		args = append(args, filter.ProjectID)
	}
	if filter.EventType != "" {
		conditions = append(conditions, fmt.Sprintf("event_type = $%d", len(args)+1))
		args = append(args, filter.EventType)
	}
	if filter.Status != "" {
		conditions = append(conditions, fmt.Sprintf("status = $%d", len(args)+1))
		args = append(args, filter.Status)
	}
	if filter.ScheduleID != "" {
		conditions = append(conditions, fmt.Sprintf("schedule_id = $%d", len(args)+1))
		args = append(args, filter.ScheduleID)
	}

	whereClause := ""
	if len(conditions) > 0 {
		whereClause = "WHERE " + strings.Join(conditions, " AND ")
	}

	// Get total count
	var totalCount int
	countQuery := fmt.Sprintf("SELECT COUNT(*) FROM scheduled_events %s", whereClause)
	if err := s.db.QueryRowContext(ctx, countQuery, args...).Scan(&totalCount); err != nil {
		return nil, err
	}

	// Apply pagination
	limit := opts.Limit
	if limit <= 0 {
		limit = 50
	}
	if limit > 200 {
		limit = 200
	}

	query := fmt.Sprintf(`
		SELECT id, project_id, event_type, fire_at, payload, status,
			created_at, created_by, fired_at, error, schedule_id
		FROM scheduled_events %s
		ORDER BY created_at DESC
		LIMIT $%d
	`, whereClause, len(args)+1)

	queryArgs := append(args, limit+1) //nolint:gocritic // intentional append to copy

	if opts.Cursor != "" {
		if whereClause == "" {
			query = fmt.Sprintf(`
				SELECT id, project_id, event_type, fire_at, payload, status,
					created_at, created_by, fired_at, error, schedule_id
				FROM scheduled_events WHERE id < $%d
				ORDER BY created_at DESC
				LIMIT $%d
			`, len(args)+1, len(args)+2)
		} else {
			query = fmt.Sprintf(`
				SELECT id, project_id, event_type, fire_at, payload, status,
					created_at, created_by, fired_at, error, schedule_id
				FROM scheduled_events %s AND id < $%d
				ORDER BY created_at DESC
				LIMIT $%d
			`, whereClause, len(args)+1, len(args)+2)
		}
		queryArgs = append(args, opts.Cursor, limit+1) //nolint:gocritic
	}

	rows, err := s.db.QueryContext(ctx, query, queryArgs...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	events, err := scanScheduledEvents(rows)
	if err != nil {
		return nil, err
	}

	result := &store.ListResult[store.ScheduledEvent]{
		TotalCount: totalCount,
	}

	if len(events) > limit {
		result.Items = events[:limit]
		result.NextCursor = events[limit-1].ID
	} else {
		result.Items = events
	}

	return result, nil
}

// PurgeOldScheduledEvents removes non-pending events older than cutoff.
func (s *PostgresStore) PurgeOldScheduledEvents(ctx context.Context, cutoff time.Time) (int, error) {
	result, err := s.db.ExecContext(ctx,
		"DELETE FROM scheduled_events WHERE status != $1 AND created_at < $2",
		store.ScheduledEventPending, cutoff,
	)
	if err != nil {
		return 0, err
	}
	rowsAffected, err := result.RowsAffected()
	if err != nil {
		return 0, err
	}
	return int(rowsAffected), nil
}

// ============================================================================
// Helpers
// ============================================================================

// timeFromPtr returns the time from a pointer, or zero time if nil.
func timeFromPtr(t *time.Time) time.Time {
	if t == nil {
		return time.Time{}
	}
	return *t
}

// scanScheduledEvents scans rows into ScheduledEvent slices.
func scanScheduledEvents(rows *sql.Rows) ([]store.ScheduledEvent, error) {
	var events []store.ScheduledEvent
	for rows.Next() {
		var event store.ScheduledEvent
		var createdBy sql.NullString
		var firedAt sql.NullTime
		var errMsg sql.NullString
		var scheduleID sql.NullString

		if err := rows.Scan(
			&event.ID, &event.ProjectID, &event.EventType, &event.FireAt, &event.Payload, &event.Status,
			&event.CreatedAt, &createdBy, &firedAt, &errMsg, &scheduleID,
		); err != nil {
			return nil, err
		}

		if createdBy.Valid {
			event.CreatedBy = createdBy.String
		}
		if firedAt.Valid {
			event.FiredAt = &firedAt.Time
		}
		if errMsg.Valid {
			event.Error = errMsg.String
		}
		if scheduleID.Valid {
			event.ScheduleID = scheduleID.String
		}
		events = append(events, event)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return events, nil
}
