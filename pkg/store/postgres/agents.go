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

// Package postgres provides a PostgreSQL implementation of the Store interface.
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

func (s *PostgresStore) CreateAgent(ctx context.Context, agent *store.Agent) error {
	now := time.Now()
	agent.Created = now
	agent.Updated = now
	agent.StateVersion = 1

	_, err := s.db.ExecContext(ctx, `
		INSERT INTO agents (
			id, agent_id, name, template, project_id,
			labels, annotations,
			phase, activity, tool_name,
			connection_state, container_status, runtime_state,
			stalled_from_activity,
			image, detached, runtime, runtime_broker_id, web_pty_enabled, task_summary, message,
			applied_config,
			created_at, updated_at, last_seen, last_activity_event, deleted_at,
			created_by, owner_id, visibility, state_version, ancestry
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17, $18, $19, $20, $21, $22, $23, $24, $25, $26, $27, $28, $29, $30, $31, $32)
	`,
		agent.ID, agent.Slug, agent.Name, agent.Template, agent.ProjectID,
		marshalJSON(agent.Labels), marshalJSON(agent.Annotations),
		agent.Phase, agent.Activity, agent.ToolName,
		agent.ConnectionState, agent.ContainerStatus, agent.RuntimeState,
		agent.StalledFromActivity,
		agent.Image, boolToInt(agent.Detached), agent.Runtime, nullableString(agent.RuntimeBrokerID), boolToInt(agent.WebPTYEnabled), agent.TaskSummary, agent.Message,
		marshalJSON(agent.AppliedConfig),
		agent.Created, agent.Updated, nullableTime(agent.LastSeen), nullableTime(agent.LastActivityEvent), nullableTime(agent.DeletedAt),
		agent.CreatedBy, agent.OwnerID, agent.Visibility, agent.StateVersion, marshalJSON(agent.Ancestry),
	)
	if err != nil {
		if strings.Contains(err.Error(), "UNIQUE constraint failed") || strings.Contains(err.Error(), "duplicate key") {
			return store.ErrAlreadyExists
		}
		return err
	}
	return nil
}

func (s *PostgresStore) GetAgent(ctx context.Context, id string) (*store.Agent, error) {
	agent := &store.Agent{}
	var labels, annotations, appliedConfig string
	var lastSeen, lastActivityEvent, deletedAt, startedAt sql.NullTime
	var runtimeBrokerID, message, toolName, ancestry sql.NullString

	err := s.db.QueryRowContext(ctx, `
		SELECT id, agent_id, name, template, project_id,
			labels, annotations,
			phase, activity, tool_name,
			connection_state, container_status, runtime_state,
			stalled_from_activity,
			current_turns, current_model_calls,
			image, detached, runtime, runtime_broker_id, web_pty_enabled, task_summary, message,
			applied_config,
			created_at, updated_at, last_seen, last_activity_event, deleted_at, started_at,
			created_by, owner_id, visibility, state_version, ancestry
		FROM agents WHERE id = $1
	`, id).Scan(
		&agent.ID, &agent.Slug, &agent.Name, &agent.Template, &agent.ProjectID,
		&labels, &annotations,
		&agent.Phase, &agent.Activity, &toolName,
		&agent.ConnectionState, &agent.ContainerStatus, &agent.RuntimeState,
		&agent.StalledFromActivity,
		&agent.CurrentTurns, &agent.CurrentModelCalls,
		&agent.Image, &agent.Detached, &agent.Runtime, &runtimeBrokerID, &agent.WebPTYEnabled, &agent.TaskSummary, &message,
		&appliedConfig,
		&agent.Created, &agent.Updated, &lastSeen, &lastActivityEvent, &deletedAt, &startedAt,
		&agent.CreatedBy, &agent.OwnerID, &agent.Visibility, &agent.StateVersion, &ancestry,
	)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, store.ErrNotFound
		}
		return nil, err
	}

	unmarshalJSON(labels, &agent.Labels)
	unmarshalJSON(annotations, &agent.Annotations)
	unmarshalJSON(appliedConfig, &agent.AppliedConfig)
	unmarshalJSON(ancestry.String, &agent.Ancestry)
	if lastSeen.Valid {
		agent.LastSeen = lastSeen.Time
	}
	if lastActivityEvent.Valid {
		agent.LastActivityEvent = lastActivityEvent.Time
	}
	if deletedAt.Valid {
		agent.DeletedAt = deletedAt.Time
	}
	if startedAt.Valid {
		agent.StartedAt = startedAt.Time
	}
	if runtimeBrokerID.Valid {
		agent.RuntimeBrokerID = runtimeBrokerID.String
	}
	if message.Valid {
		agent.Message = message.String
	}
	if toolName.Valid {
		agent.ToolName = toolName.String
	}

	return agent, nil
}

func (s *PostgresStore) GetAgentBySlug(ctx context.Context, projectID, slug string) (*store.Agent, error) {
	agent := &store.Agent{}
	var labels, annotations, appliedConfig string
	var lastSeen, lastActivityEvent, deletedAt, startedAt sql.NullTime
	var runtimeBrokerID, message, toolName, ancestry sql.NullString

	err := s.db.QueryRowContext(ctx, `
		SELECT id, agent_id, name, template, project_id,
			labels, annotations,
			phase, activity, tool_name,
			connection_state, container_status, runtime_state,
			stalled_from_activity,
			current_turns, current_model_calls,
			image, detached, runtime, runtime_broker_id, web_pty_enabled, task_summary, message,
			applied_config,
			created_at, updated_at, last_seen, last_activity_event, deleted_at, started_at,
			created_by, owner_id, visibility, state_version, ancestry
		FROM agents WHERE project_id = $1 AND agent_id = $2
	`, projectID, slug).Scan(
		&agent.ID, &agent.Slug, &agent.Name, &agent.Template, &agent.ProjectID,
		&labels, &annotations,
		&agent.Phase, &agent.Activity, &toolName,
		&agent.ConnectionState, &agent.ContainerStatus, &agent.RuntimeState,
		&agent.StalledFromActivity,
		&agent.CurrentTurns, &agent.CurrentModelCalls,
		&agent.Image, &agent.Detached, &agent.Runtime, &runtimeBrokerID, &agent.WebPTYEnabled, &agent.TaskSummary, &message,
		&appliedConfig,
		&agent.Created, &agent.Updated, &lastSeen, &lastActivityEvent, &deletedAt, &startedAt,
		&agent.CreatedBy, &agent.OwnerID, &agent.Visibility, &agent.StateVersion, &ancestry,
	)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, store.ErrNotFound
		}
		return nil, err
	}

	unmarshalJSON(labels, &agent.Labels)
	unmarshalJSON(annotations, &agent.Annotations)
	unmarshalJSON(appliedConfig, &agent.AppliedConfig)
	unmarshalJSON(ancestry.String, &agent.Ancestry)
	if lastSeen.Valid {
		agent.LastSeen = lastSeen.Time
	}
	if lastActivityEvent.Valid {
		agent.LastActivityEvent = lastActivityEvent.Time
	}
	if deletedAt.Valid {
		agent.DeletedAt = deletedAt.Time
	}
	if startedAt.Valid {
		agent.StartedAt = startedAt.Time
	}
	if runtimeBrokerID.Valid {
		agent.RuntimeBrokerID = runtimeBrokerID.String
	}
	if message.Valid {
		agent.Message = message.String
	}
	if toolName.Valid {
		agent.ToolName = toolName.String
	}

	return agent, nil
}

func (s *PostgresStore) UpdateAgent(ctx context.Context, agent *store.Agent) error {
	agent.Updated = time.Now()
	newVersion := agent.StateVersion + 1

	result, err := s.db.ExecContext(ctx, `
		UPDATE agents SET
			agent_id = $1, name = $2, template = $3,
			labels = $4, annotations = $5,
			phase = $6, activity = $7, tool_name = $8,
			connection_state = $9, container_status = $10, runtime_state = $11,
			stalled_from_activity = $12,
			image = $13, detached = $14, runtime = $15, runtime_broker_id = $16, web_pty_enabled = $17, task_summary = $18, message = $19,
			applied_config = $20,
			updated_at = $21, last_seen = $22, last_activity_event = $23, deleted_at = $24,
			owner_id = $25, visibility = $26, state_version = $27
		WHERE id = $28 AND state_version = $29
	`,
		agent.Slug, agent.Name, agent.Template,
		marshalJSON(agent.Labels), marshalJSON(agent.Annotations),
		agent.Phase, agent.Activity, agent.ToolName,
		agent.ConnectionState, agent.ContainerStatus, agent.RuntimeState,
		agent.StalledFromActivity,
		agent.Image, boolToInt(agent.Detached), agent.Runtime, nullableString(agent.RuntimeBrokerID), boolToInt(agent.WebPTYEnabled), agent.TaskSummary, agent.Message,
		marshalJSON(agent.AppliedConfig),
		agent.Updated, nullableTime(agent.LastSeen), nullableTime(agent.LastActivityEvent), nullableTime(agent.DeletedAt),
		agent.OwnerID, agent.Visibility, newVersion,
		agent.ID, agent.StateVersion,
	)
	if err != nil {
		return err
	}

	rowsAffected, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if rowsAffected == 0 {
		// Check if agent exists
		var exists bool
		s.db.QueryRowContext(ctx, "SELECT 1 FROM agents WHERE id = $1", agent.ID).Scan(&exists)
		if !exists {
			return store.ErrNotFound
		}
		return store.ErrVersionConflict
	}

	agent.StateVersion = newVersion
	return nil
}

func (s *PostgresStore) DeleteAgent(ctx context.Context, id string) error {
	result, err := s.db.ExecContext(ctx, "DELETE FROM agents WHERE id = $1", id)
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

func (s *PostgresStore) ListAgents(ctx context.Context, filter store.AgentFilter, opts store.ListOptions) (*store.ListResult[store.Agent], error) {
	var conditions []string
	var args []interface{}

	if len(filter.MemberOrOwnerProjectIDs) > 0 {
		// Combine project_id membership with owner_id match using OR
		placeholders := make([]string, len(filter.MemberOrOwnerProjectIDs))
		for i, id := range filter.MemberOrOwnerProjectIDs {
			placeholders[i] = fmt.Sprintf("$%d", len(args)+1)
			args = append(args, id)
		}
		orParts := []string{"project_id IN (" + strings.Join(placeholders, ",") + ")"}
		if filter.OwnerID != "" {
			orParts = append(orParts, fmt.Sprintf("owner_id = $%d", len(args)+1))
			args = append(args, filter.OwnerID)
		}
		conditions = append(conditions, "("+strings.Join(orParts, " OR ")+")")
	} else if len(filter.MemberProjectIDs) > 0 {
		placeholders := make([]string, len(filter.MemberProjectIDs))
		for i, id := range filter.MemberProjectIDs {
			placeholders[i] = fmt.Sprintf("$%d", len(args)+1)
			args = append(args, id)
		}
		conditions = append(conditions, "project_id IN ("+strings.Join(placeholders, ",")+")")
	} else if filter.OwnerID != "" {
		conditions = append(conditions, fmt.Sprintf("owner_id = $%d", len(args)+1))
		args = append(args, filter.OwnerID)
	}
	if filter.ExcludeOwnerID != "" {
		conditions = append(conditions, fmt.Sprintf("owner_id != $%d", len(args)+1))
		args = append(args, filter.ExcludeOwnerID)
	}
	if filter.ProjectID != "" {
		conditions = append(conditions, fmt.Sprintf("project_id = $%d", len(args)+1))
		args = append(args, filter.ProjectID)
	}
	if filter.RuntimeBrokerID != "" {
		conditions = append(conditions, fmt.Sprintf("runtime_broker_id = $%d", len(args)+1))
		args = append(args, filter.RuntimeBrokerID)
	}
	if filter.Phase != "" {
		conditions = append(conditions, fmt.Sprintf("phase = $%d", len(args)+1))
		args = append(args, filter.Phase)
	}
	if filter.AncestorID != "" {
		conditions = append(conditions, fmt.Sprintf("EXISTS (SELECT 1 FROM json_array_elements_text(ancestry::json) AS e(value) WHERE e.value = $%d)", len(args)+1))
		args = append(args, filter.AncestorID)
	}

	// Exclude soft-deleted agents unless explicitly requested
	if !filter.IncludeDeleted {
		conditions = append(conditions, "deleted_at IS NULL")
	}

	whereClause := ""
	if len(conditions) > 0 {
		whereClause = "WHERE " + strings.Join(conditions, " AND ")
	}

	// Get total count
	var totalCount int
	countQuery := fmt.Sprintf("SELECT COUNT(*) FROM agents %s", whereClause)
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
		SELECT id, agent_id, name, template, project_id,
			labels, annotations,
			phase, activity, tool_name,
			connection_state, container_status, runtime_state,
			stalled_from_activity,
			current_turns, current_model_calls,
			image, detached, runtime, runtime_broker_id, web_pty_enabled, task_summary, message,
			applied_config,
			created_at, updated_at, last_seen, last_activity_event, deleted_at, started_at,
			created_by, owner_id, visibility, state_version, ancestry
		FROM agents %s ORDER BY created_at DESC LIMIT $%d
	`, whereClause, len(args)+1)
	args = append(args, limit+1) // Fetch one extra to determine if there's a next page

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var agents []store.Agent
	for rows.Next() {
		var agent store.Agent
		var labels, annotations, appliedConfig string
		var lastSeen, lastActivityEvent, deletedAt, startedAt sql.NullTime
		var runtimeBrokerID, message, toolName, ancestry sql.NullString

		if err := rows.Scan(
			&agent.ID, &agent.Slug, &agent.Name, &agent.Template, &agent.ProjectID,
			&labels, &annotations,
			&agent.Phase, &agent.Activity, &toolName,
			&agent.ConnectionState, &agent.ContainerStatus, &agent.RuntimeState,
			&agent.StalledFromActivity,
			&agent.CurrentTurns, &agent.CurrentModelCalls,
			&agent.Image, &agent.Detached, &agent.Runtime, &runtimeBrokerID, &agent.WebPTYEnabled, &agent.TaskSummary, &message,
			&appliedConfig,
			&agent.Created, &agent.Updated, &lastSeen, &lastActivityEvent, &deletedAt, &startedAt,
			&agent.CreatedBy, &agent.OwnerID, &agent.Visibility, &agent.StateVersion, &ancestry,
		); err != nil {
			return nil, err
		}

		unmarshalJSON(labels, &agent.Labels)
		unmarshalJSON(annotations, &agent.Annotations)
		unmarshalJSON(appliedConfig, &agent.AppliedConfig)
		unmarshalJSON(ancestry.String, &agent.Ancestry)
		if lastSeen.Valid {
			agent.LastSeen = lastSeen.Time
		}
		if lastActivityEvent.Valid {
			agent.LastActivityEvent = lastActivityEvent.Time
		}
		if deletedAt.Valid {
			agent.DeletedAt = deletedAt.Time
		}
		if startedAt.Valid {
			agent.StartedAt = startedAt.Time
		}
		if runtimeBrokerID.Valid {
			agent.RuntimeBrokerID = runtimeBrokerID.String
		}
		if message.Valid {
			agent.Message = message.String
		}
		if toolName.Valid {
			agent.ToolName = toolName.String
		}

		agents = append(agents, agent)
	}

	result := &store.ListResult[store.Agent]{
		Items:      agents,
		TotalCount: totalCount,
	}

	// Handle pagination
	if len(agents) > limit {
		result.Items = agents[:limit]
		result.NextCursor = agents[limit-1].ID
	}

	return result, nil
}

func (s *PostgresStore) UpdateAgentStatus(ctx context.Context, id string, su store.AgentStatusUpdate) error {
	now := time.Now()

	// When activity is being updated to something other than "executing",
	// clear tool_name (it's only meaningful during execution).
	// We signal this by setting the activity-provided flag.
	activityProvided := su.Activity != ""

	// Prepare nullable values for limits tracking fields
	var currentTurnsProvided bool
	var currentTurnsVal int
	if su.CurrentTurns != nil {
		currentTurnsProvided = true
		currentTurnsVal = *su.CurrentTurns
	}
	var currentModelCallsProvided bool
	var currentModelCallsVal int
	if su.CurrentModelCalls != nil {
		currentModelCallsProvided = true
		currentModelCallsVal = *su.CurrentModelCalls
	}

	result, err := s.db.ExecContext(ctx, `
		UPDATE agents SET
			phase = COALESCE(NULLIF($1, ''), phase),
			activity = CASE WHEN $2 != '' THEN
				CASE WHEN phase = 'stopped'
					AND activity IN ('crashed', 'limits_exceeded')
					AND $3 NOT IN ('crashed', 'limits_exceeded')
					THEN activity ELSE $4 END
				ELSE activity END,
			tool_name = CASE WHEN $5 THEN $6 ELSE tool_name END,
			message = COALESCE(NULLIF($7, ''), message),
			connection_state = COALESCE(NULLIF($8, ''), connection_state),
			container_status = COALESCE(NULLIF($9, ''), container_status),
			runtime_state = COALESCE(NULLIF($10, ''), runtime_state),
			task_summary = COALESCE(NULLIF($11, ''), task_summary),
			stalled_from_activity = CASE WHEN $12 != '' THEN '' ELSE stalled_from_activity END,
			last_activity_event = CASE WHEN $13 != '' THEN $14 ELSE last_activity_event END,
			current_turns = CASE WHEN $15 THEN $16 ELSE current_turns END,
			current_model_calls = CASE WHEN $17 THEN $18 ELSE current_model_calls END,
			started_at = COALESCE(NULLIF($19, ''), started_at),
			updated_at = $20,
			last_seen = $21
		WHERE id = $22
	`,
		su.Phase,
		su.Activity, su.Activity, su.Activity,
		activityProvided, su.ToolName,
		su.Message, su.ConnectionState, su.ContainerStatus,
		su.RuntimeState, su.TaskSummary,
		su.Activity,
		su.Activity, now,
		currentTurnsProvided, currentTurnsVal,
		currentModelCallsProvided, currentModelCallsVal,
		su.StartedAt,
		now, now, id,
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

func (s *PostgresStore) PurgeDeletedAgents(ctx context.Context, cutoff time.Time) (int, error) {
	result, err := s.db.ExecContext(ctx,
		"DELETE FROM agents WHERE deleted_at IS NOT NULL AND deleted_at < $1",
		cutoff,
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

func (s *PostgresStore) MarkStaleAgentsOffline(ctx context.Context, threshold time.Time) ([]store.Agent, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	now := time.Now()

	// Update stale agents to offline activity.
	// Only affects agents that:
	// - Have reported at least one heartbeat (last_seen IS NOT NULL)
	// - Are in the running phase
	// - Are not already in a terminal/sticky activity (completed, limits_exceeded, offline)
	_, err = tx.ExecContext(ctx, `
		UPDATE agents SET
			activity = 'offline',
			updated_at = $1
		WHERE last_seen < $2
		  AND last_seen IS NOT NULL
		  AND phase = 'running'
		  AND activity NOT IN ('completed', 'limits_exceeded', 'blocked', 'offline')
	`, now, threshold)
	if err != nil {
		return nil, err
	}

	// Fetch the agents that were just updated.
	rows, err := tx.QueryContext(ctx, `
		SELECT id, agent_id, name, template, project_id,
			labels, annotations,
			phase, activity, tool_name,
			connection_state, container_status, runtime_state,
			stalled_from_activity,
			current_turns, current_model_calls,
			image, detached, runtime, runtime_broker_id, web_pty_enabled, task_summary, message,
			applied_config,
			created_at, updated_at, last_seen, last_activity_event, deleted_at, started_at,
			created_by, owner_id, visibility, state_version, ancestry
		FROM agents
		WHERE activity = 'offline' AND updated_at = $1
		  AND last_seen < $2
		  AND last_seen IS NOT NULL
		  AND phase = 'running'
	`, now, threshold)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var agents []store.Agent
	for rows.Next() {
		var agent store.Agent
		var labels, annotations, appliedConfig string
		var lastSeen, lastActivityEvent, deletedAt, startedAt sql.NullTime
		var runtimeBrokerID, message, toolName, ancestry sql.NullString

		if err := rows.Scan(
			&agent.ID, &agent.Slug, &agent.Name, &agent.Template, &agent.ProjectID,
			&labels, &annotations,
			&agent.Phase, &agent.Activity, &toolName,
			&agent.ConnectionState, &agent.ContainerStatus, &agent.RuntimeState,
			&agent.StalledFromActivity,
			&agent.CurrentTurns, &agent.CurrentModelCalls,
			&agent.Image, &agent.Detached, &agent.Runtime, &runtimeBrokerID, &agent.WebPTYEnabled, &agent.TaskSummary, &message,
			&appliedConfig,
			&agent.Created, &agent.Updated, &lastSeen, &lastActivityEvent, &deletedAt, &startedAt,
			&agent.CreatedBy, &agent.OwnerID, &agent.Visibility, &agent.StateVersion, &ancestry,
		); err != nil {
			return nil, err
		}

		unmarshalJSON(labels, &agent.Labels)
		unmarshalJSON(annotations, &agent.Annotations)
		unmarshalJSON(appliedConfig, &agent.AppliedConfig)
		unmarshalJSON(ancestry.String, &agent.Ancestry)
		if lastSeen.Valid {
			agent.LastSeen = lastSeen.Time
		}
		if lastActivityEvent.Valid {
			agent.LastActivityEvent = lastActivityEvent.Time
		}
		if deletedAt.Valid {
			agent.DeletedAt = deletedAt.Time
		}
		if startedAt.Valid {
			agent.StartedAt = startedAt.Time
		}
		if runtimeBrokerID.Valid {
			agent.RuntimeBrokerID = runtimeBrokerID.String
		}
		if message.Valid {
			agent.Message = message.String
		}
		if toolName.Valid {
			agent.ToolName = toolName.String
		}

		agents = append(agents, agent)
	}

	if err := tx.Commit(); err != nil {
		return nil, err
	}

	return agents, nil
}

func (s *PostgresStore) MarkStalledAgents(ctx context.Context, activityThreshold, heartbeatRecency time.Time) ([]store.Agent, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	now := time.Now()

	// Update agents to stalled activity.
	// Only affects agents that:
	// - Have a stale last_activity_event (older than activityThreshold)
	// - Have a recent heartbeat (last_seen >= heartbeatRecency) — process is alive
	// - Are in the running phase
	// - Are not already in a terminal/sticky/waiting activity or already stalled/offline
	_, err = tx.ExecContext(ctx, `
		UPDATE agents SET
			stalled_from_activity = activity,
			activity = 'stalled',
			updated_at = $1
		WHERE last_activity_event < $2
		  AND last_activity_event IS NOT NULL
		  AND last_seen >= $3
		  AND last_seen IS NOT NULL
		  AND phase = 'running'
		  AND activity NOT IN ('completed', 'limits_exceeded', 'blocked', 'stalled', 'offline', 'waiting_for_input')
	`, now, activityThreshold, heartbeatRecency)
	if err != nil {
		return nil, err
	}

	// Fetch the agents that were just updated.
	rows, err := tx.QueryContext(ctx, `
		SELECT id, agent_id, name, template, project_id,
			labels, annotations,
			phase, activity, tool_name,
			connection_state, container_status, runtime_state,
			stalled_from_activity,
			current_turns, current_model_calls,
			image, detached, runtime, runtime_broker_id, web_pty_enabled, task_summary, message,
			applied_config,
			created_at, updated_at, last_seen, last_activity_event, deleted_at, started_at,
			created_by, owner_id, visibility, state_version, ancestry
		FROM agents
		WHERE activity = 'stalled' AND updated_at = $1
		  AND last_activity_event < $2
		  AND last_activity_event IS NOT NULL
		  AND last_seen >= $3
		  AND last_seen IS NOT NULL
		  AND phase = 'running'
	`, now, activityThreshold, heartbeatRecency)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var agents []store.Agent
	for rows.Next() {
		var agent store.Agent
		var labels, annotations, appliedConfig string
		var lastSeen, lastActivityEvent, deletedAt, startedAt sql.NullTime
		var runtimeBrokerID, message, toolName, ancestry sql.NullString

		if err := rows.Scan(
			&agent.ID, &agent.Slug, &agent.Name, &agent.Template, &agent.ProjectID,
			&labels, &annotations,
			&agent.Phase, &agent.Activity, &toolName,
			&agent.ConnectionState, &agent.ContainerStatus, &agent.RuntimeState,
			&agent.StalledFromActivity,
			&agent.CurrentTurns, &agent.CurrentModelCalls,
			&agent.Image, &agent.Detached, &agent.Runtime, &runtimeBrokerID, &agent.WebPTYEnabled, &agent.TaskSummary, &message,
			&appliedConfig,
			&agent.Created, &agent.Updated, &lastSeen, &lastActivityEvent, &deletedAt, &startedAt,
			&agent.CreatedBy, &agent.OwnerID, &agent.Visibility, &agent.StateVersion, &ancestry,
		); err != nil {
			return nil, err
		}

		unmarshalJSON(labels, &agent.Labels)
		unmarshalJSON(annotations, &agent.Annotations)
		unmarshalJSON(appliedConfig, &agent.AppliedConfig)
		unmarshalJSON(ancestry.String, &agent.Ancestry)
		if lastSeen.Valid {
			agent.LastSeen = lastSeen.Time
		}
		if lastActivityEvent.Valid {
			agent.LastActivityEvent = lastActivityEvent.Time
		}
		if deletedAt.Valid {
			agent.DeletedAt = deletedAt.Time
		}
		if startedAt.Valid {
			agent.StartedAt = startedAt.Time
		}
		if runtimeBrokerID.Valid {
			agent.RuntimeBrokerID = runtimeBrokerID.String
		}
		if message.Valid {
			agent.Message = message.String
		}
		if toolName.Valid {
			agent.ToolName = toolName.String
		}

		agents = append(agents, agent)
	}

	if err := tx.Commit(); err != nil {
		return nil, err
	}

	return agents, nil
}
