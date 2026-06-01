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
// Schedule Operations (Recurring Schedules)
// ============================================================================

// CreateSchedule creates a new recurring schedule.
func (s *PostgresStore) CreateSchedule(ctx context.Context, schedule *store.Schedule) error {
	if schedule.ID == "" || schedule.ProjectID == "" || schedule.Name == "" || schedule.CronExpr == "" {
		return store.ErrInvalidInput
	}

	now := time.Now()
	if schedule.CreatedAt.IsZero() {
		schedule.CreatedAt = now
	}
	if schedule.UpdatedAt.IsZero() {
		schedule.UpdatedAt = now
	}
	if schedule.Status == "" {
		schedule.Status = store.ScheduleStatusActive
	}

	_, err := s.db.ExecContext(ctx, `
		INSERT INTO schedules (
			id, project_id, name, cron_expr, event_type, payload, status,
			next_run_at, last_run_at, last_run_status, last_run_error,
			run_count, error_count, created_at, created_by, updated_at
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16)
	`,
		schedule.ID, schedule.ProjectID, schedule.Name, schedule.CronExpr,
		schedule.EventType, schedule.Payload, schedule.Status,
		nullableTime(timeFromNullablePtr(schedule.NextRunAt)),
		nullableTime(timeFromNullablePtr(schedule.LastRunAt)),
		nullableString(schedule.LastRunStatus), nullableString(schedule.LastRunError),
		schedule.RunCount, schedule.ErrorCount,
		schedule.CreatedAt, nullableString(schedule.CreatedBy), schedule.UpdatedAt,
	)
	if err != nil {
		if strings.Contains(err.Error(), "UNIQUE constraint failed") || strings.Contains(err.Error(), "duplicate key") {
			return store.ErrAlreadyExists
		}
		if strings.Contains(err.Error(), "FOREIGN KEY constraint failed") {
			return fmt.Errorf("project %s does not exist: %w", schedule.ProjectID, store.ErrInvalidInput)
		}
		return err
	}
	return nil
}

// GetSchedule retrieves a schedule by ID.
func (s *PostgresStore) GetSchedule(ctx context.Context, id string) (*store.Schedule, error) {
	schedule := &store.Schedule{}
	var nextRunAt, lastRunAt sql.NullTime
	var lastRunStatus, lastRunError, createdBy sql.NullString

	err := s.db.QueryRowContext(ctx, `
		SELECT id, project_id, name, cron_expr, event_type, payload, status,
			next_run_at, last_run_at, last_run_status, last_run_error,
			run_count, error_count, created_at, created_by, updated_at
		FROM schedules WHERE id = $1
	`, id).Scan(
		&schedule.ID, &schedule.ProjectID, &schedule.Name, &schedule.CronExpr,
		&schedule.EventType, &schedule.Payload, &schedule.Status,
		&nextRunAt, &lastRunAt, &lastRunStatus, &lastRunError,
		&schedule.RunCount, &schedule.ErrorCount,
		&schedule.CreatedAt, &createdBy, &schedule.UpdatedAt,
	)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, store.ErrNotFound
		}
		return nil, err
	}

	if nextRunAt.Valid {
		schedule.NextRunAt = &nextRunAt.Time
	}
	if lastRunAt.Valid {
		schedule.LastRunAt = &lastRunAt.Time
	}
	if lastRunStatus.Valid {
		schedule.LastRunStatus = lastRunStatus.String
	}
	if lastRunError.Valid {
		schedule.LastRunError = lastRunError.String
	}
	if createdBy.Valid {
		schedule.CreatedBy = createdBy.String
	}

	return schedule, nil
}

// ListSchedules returns schedules matching the filter criteria.
func (s *PostgresStore) ListSchedules(ctx context.Context, filter store.ScheduleFilter, opts store.ListOptions) (*store.ListResult[store.Schedule], error) {
	var conditions []string
	var args []interface{}

	if filter.ProjectID != "" {
		args = append(args, filter.ProjectID)
		conditions = append(conditions, fmt.Sprintf("project_id = $%d", len(args)))
	}
	if filter.Status != "" {
		args = append(args, filter.Status)
		conditions = append(conditions, fmt.Sprintf("status = $%d", len(args)))
	} else {
		// By default, exclude deleted schedules
		args = append(args, store.ScheduleStatusDeleted)
		conditions = append(conditions, fmt.Sprintf("status != $%d", len(args)))
	}
	if filter.Name != "" {
		args = append(args, filter.Name)
		conditions = append(conditions, fmt.Sprintf("name = $%d", len(args)))
	}

	whereClause := ""
	if len(conditions) > 0 {
		whereClause = "WHERE " + strings.Join(conditions, " AND ")
	}

	// Get total count
	var totalCount int
	countQuery := fmt.Sprintf("SELECT COUNT(*) FROM schedules %s", whereClause)
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

	query := fmt.Sprintf(`
		SELECT id, project_id, name, cron_expr, event_type, payload, status,
			next_run_at, last_run_at, last_run_status, last_run_error,
			run_count, error_count, created_at, created_by, updated_at
		FROM schedules %s
		ORDER BY created_at DESC
		LIMIT $%d
	`, whereClause, len(args)+1)

	queryArgs := append(args, limit+1) //nolint:gocritic

	rows, err := s.db.QueryContext(ctx, query, queryArgs...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	schedules, err := scanSchedules(rows)
	if err != nil {
		return nil, err
	}

	result := &store.ListResult[store.Schedule]{
		TotalCount: totalCount,
	}

	if len(schedules) > limit {
		result.Items = schedules[:limit]
		result.NextCursor = schedules[limit-1].ID
	} else {
		result.Items = schedules
	}

	return result, nil
}

// UpdateSchedule updates an existing schedule.
func (s *PostgresStore) UpdateSchedule(ctx context.Context, schedule *store.Schedule) error {
	schedule.UpdatedAt = time.Now()

	result, err := s.db.ExecContext(ctx, `
		UPDATE schedules SET
			name = $1, cron_expr = $2, event_type = $3, payload = $4,
			status = $5, next_run_at = $6, updated_at = $7
		WHERE id = $8
	`,
		schedule.Name, schedule.CronExpr, schedule.EventType, schedule.Payload,
		schedule.Status, nullableTime(timeFromNullablePtr(schedule.NextRunAt)),
		schedule.UpdatedAt, schedule.ID,
	)
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

// UpdateScheduleStatus updates only the status of a schedule.
func (s *PostgresStore) UpdateScheduleStatus(ctx context.Context, id string, status string) error {
	result, err := s.db.ExecContext(ctx, `
		UPDATE schedules SET status = $1, updated_at = $2 WHERE id = $3
	`, status, time.Now(), id)
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

// UpdateScheduleAfterRun updates a schedule after a run completes.
func (s *PostgresStore) UpdateScheduleAfterRun(ctx context.Context, id string, ranAt time.Time, nextRunAt time.Time, errMsg string) error {
	var query string
	var args []interface{}

	if errMsg != "" {
		query = `
			UPDATE schedules SET
				last_run_at = $1, next_run_at = $2, last_run_status = $3, last_run_error = $4,
				run_count = run_count + 1, error_count = error_count + 1, updated_at = $5
			WHERE id = $6
		`
		args = []interface{}{ranAt, nextRunAt, store.ScheduleRunError, errMsg, time.Now(), id}
	} else {
		query = `
			UPDATE schedules SET
				last_run_at = $1, next_run_at = $2, last_run_status = $3, last_run_error = NULL,
				run_count = run_count + 1, updated_at = $4
			WHERE id = $5
		`
		args = []interface{}{ranAt, nextRunAt, store.ScheduleRunSuccess, time.Now(), id}
	}

	result, err := s.db.ExecContext(ctx, query, args...)
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

// DeleteSchedule removes a schedule by ID (hard delete).
func (s *PostgresStore) DeleteSchedule(ctx context.Context, id string) error {
	result, err := s.db.ExecContext(ctx, "DELETE FROM schedules WHERE id = $1", id)
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

// ListDueSchedules returns active schedules whose next_run_at has passed.
func (s *PostgresStore) ListDueSchedules(ctx context.Context, now time.Time) ([]store.Schedule, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, project_id, name, cron_expr, event_type, payload, status,
			next_run_at, last_run_at, last_run_status, last_run_error,
			run_count, error_count, created_at, created_by, updated_at
		FROM schedules
		WHERE status = $1 AND next_run_at IS NOT NULL AND next_run_at <= $2
		ORDER BY next_run_at ASC
	`, store.ScheduleStatusActive, now)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	return scanSchedules(rows)
}

// ============================================================================
// Helpers
// ============================================================================

// timeFromNullablePtr returns the time from a pointer, or zero time if nil.
func timeFromNullablePtr(t *time.Time) time.Time {
	if t == nil {
		return time.Time{}
	}
	return *t
}

// scanSchedules scans rows into Schedule slices.
func scanSchedules(rows *sql.Rows) ([]store.Schedule, error) {
	var schedules []store.Schedule
	for rows.Next() {
		var schedule store.Schedule
		var nextRunAt, lastRunAt sql.NullTime
		var lastRunStatus, lastRunError, createdBy sql.NullString

		if err := rows.Scan(
			&schedule.ID, &schedule.ProjectID, &schedule.Name, &schedule.CronExpr,
			&schedule.EventType, &schedule.Payload, &schedule.Status,
			&nextRunAt, &lastRunAt, &lastRunStatus, &lastRunError,
			&schedule.RunCount, &schedule.ErrorCount,
			&schedule.CreatedAt, &createdBy, &schedule.UpdatedAt,
		); err != nil {
			return nil, err
		}

		if nextRunAt.Valid {
			schedule.NextRunAt = &nextRunAt.Time
		}
		if lastRunAt.Valid {
			schedule.LastRunAt = &lastRunAt.Time
		}
		if lastRunStatus.Valid {
			schedule.LastRunStatus = lastRunStatus.String
		}
		if lastRunError.Valid {
			schedule.LastRunError = lastRunError.String
		}
		if createdBy.Valid {
			schedule.CreatedBy = createdBy.String
		}
		schedules = append(schedules, schedule)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return schedules, nil
}
