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

func (s *PostgresStore) CreateRuntimeBroker(ctx context.Context, broker *store.RuntimeBroker) error {
	now := time.Now()
	broker.Created = now
	broker.Updated = now

	_, err := s.db.ExecContext(ctx, `
		INSERT INTO runtime_brokers (
			id, name, slug, type, mode, version,
			status, connection_state, last_heartbeat,
			capabilities, supported_harnesses, resources, runtimes,
			labels, annotations, endpoint,
			created_at, updated_at, created_by, auto_provide
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17, $18, $19, $20)
	`,
		broker.ID, broker.Name, broker.Slug, "", "", broker.Version,
		broker.Status, broker.ConnectionState, broker.LastHeartbeat,
		marshalJSON(broker.Capabilities), "[]",
		"{}", marshalJSON(broker.Profiles),
		marshalJSON(broker.Labels), marshalJSON(broker.Annotations), broker.Endpoint,
		broker.Created, broker.Updated, nullableString(broker.CreatedBy), boolToInt(broker.AutoProvide),
	)
	if err != nil {
		if strings.Contains(err.Error(), "UNIQUE constraint failed") || strings.Contains(err.Error(), "duplicate key") {
			return store.ErrAlreadyExists
		}
		return err
	}
	return nil
}

func (s *PostgresStore) GetRuntimeBroker(ctx context.Context, id string) (*store.RuntimeBroker, error) {
	broker := &store.RuntimeBroker{}
	var capabilities, profiles, labels, annotations string
	var brokerType, brokerMode, harnesses, resources string // unused columns kept for schema compatibility
	var lastHeartbeat sql.NullTime
	var createdBy sql.NullString

	err := s.db.QueryRowContext(ctx, `
		SELECT id, name, slug, type, mode, version,
			status, connection_state, last_heartbeat,
			capabilities, supported_harnesses, resources, runtimes,
			labels, annotations, endpoint,
			created_at, updated_at, created_by, auto_provide
		FROM runtime_brokers WHERE id = $1
	`, id).Scan(
		&broker.ID, &broker.Name, &broker.Slug, &brokerType, &brokerMode, &broker.Version,
		&broker.Status, &broker.ConnectionState, &lastHeartbeat,
		&capabilities, &harnesses, &resources, &profiles,
		&labels, &annotations, &broker.Endpoint,
		&broker.Created, &broker.Updated, &createdBy, &broker.AutoProvide,
	)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, store.ErrNotFound
		}
		return nil, err
	}

	if lastHeartbeat.Valid {
		broker.LastHeartbeat = lastHeartbeat.Time
	}
	if createdBy.Valid {
		broker.CreatedBy = createdBy.String
	}
	unmarshalJSON(capabilities, &broker.Capabilities)
	unmarshalJSON(profiles, &broker.Profiles)
	unmarshalJSON(labels, &broker.Labels)
	unmarshalJSON(annotations, &broker.Annotations)

	return broker, nil
}

func (s *PostgresStore) GetRuntimeBrokerByName(ctx context.Context, name string) (*store.RuntimeBroker, error) {
	var id string
	err := s.db.QueryRowContext(ctx, "SELECT id FROM runtime_brokers WHERE LOWER(name) = LOWER($1)", name).Scan(&id)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, store.ErrNotFound
		}
		return nil, err
	}
	return s.GetRuntimeBroker(ctx, id)
}

func (s *PostgresStore) UpdateRuntimeBroker(ctx context.Context, broker *store.RuntimeBroker) error {
	broker.Updated = time.Now()

	result, err := s.db.ExecContext(ctx, `
		UPDATE runtime_brokers SET
			name = $1, slug = $2, type = $3, version = $4,
			status = $5, connection_state = $6, last_heartbeat = $7,
			capabilities = $8, supported_harnesses = $9, resources = $10, runtimes = $11,
			labels = $12, annotations = $13, endpoint = $14,
			updated_at = $15, auto_provide = $16
		WHERE id = $17
	`,
		broker.Name, broker.Slug, "", broker.Version,
		broker.Status, broker.ConnectionState, broker.LastHeartbeat,
		marshalJSON(broker.Capabilities), "[]",
		"{}", marshalJSON(broker.Profiles),
		marshalJSON(broker.Labels), marshalJSON(broker.Annotations), broker.Endpoint,
		broker.Updated, boolToInt(broker.AutoProvide),
		broker.ID,
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

func (s *PostgresStore) DeleteRuntimeBroker(ctx context.Context, id string) error {
	result, err := s.db.ExecContext(ctx, "DELETE FROM runtime_brokers WHERE id = $1", id)
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

func (s *PostgresStore) ListRuntimeBrokers(ctx context.Context, filter store.RuntimeBrokerFilter, opts store.ListOptions) (*store.ListResult[store.RuntimeBroker], error) {
	var conditions []string
	var args []interface{}

	if filter.Status != "" {
		conditions = append(conditions, fmt.Sprintf("status = $%d", len(args)+1))
		args = append(args, filter.Status)
	}
	if filter.ProjectID != "" {
		conditions = append(conditions, fmt.Sprintf("(id IN (SELECT broker_id FROM project_contributors WHERE project_id = $%d) OR auto_provide = 1)", len(args)+1))
		args = append(args, filter.ProjectID)
	}
	if filter.Name != "" {
		conditions = append(conditions, fmt.Sprintf("LOWER(name) = LOWER($%d)", len(args)+1))
		args = append(args, filter.Name)
	}
	if filter.AutoProvide != nil {
		conditions = append(conditions, fmt.Sprintf("auto_provide = $%d", len(args)+1))
		args = append(args, boolToInt(*filter.AutoProvide))
	}

	whereClause := ""
	if len(conditions) > 0 {
		whereClause = "WHERE " + strings.Join(conditions, " AND ")
	}

	var totalCount int
	countQuery := fmt.Sprintf("SELECT COUNT(*) FROM runtime_brokers %s", whereClause)
	if err := s.db.QueryRowContext(ctx, countQuery, args...).Scan(&totalCount); err != nil {
		return nil, err
	}

	limit := opts.Limit
	if limit <= 0 {
		limit = 50
	}

	query := fmt.Sprintf(`
		SELECT id, name, slug, type, mode, version,
			status, connection_state, last_heartbeat,
			capabilities, supported_harnesses, resources, runtimes,
			labels, annotations, endpoint,
			created_at, updated_at, created_by, auto_provide
		FROM runtime_brokers %s ORDER BY created_at DESC LIMIT $%d
	`, whereClause, len(args)+1)
	args = append(args, limit)

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var hosts []store.RuntimeBroker
	for rows.Next() {
		var broker store.RuntimeBroker
		var capabilities, profiles, labels, annotations string
		var brokerType, brokerMode, harnesses, resources string // unused columns kept for schema compatibility
		var lastHeartbeat sql.NullTime
		var createdBy sql.NullString

		if err := rows.Scan(
			&broker.ID, &broker.Name, &broker.Slug, &brokerType, &brokerMode, &broker.Version,
			&broker.Status, &broker.ConnectionState, &lastHeartbeat,
			&capabilities, &harnesses, &resources, &profiles,
			&labels, &annotations, &broker.Endpoint,
			&broker.Created, &broker.Updated, &createdBy, &broker.AutoProvide,
		); err != nil {
			return nil, err
		}

		if lastHeartbeat.Valid {
			broker.LastHeartbeat = lastHeartbeat.Time
		}
		if createdBy.Valid {
			broker.CreatedBy = createdBy.String
		}
		unmarshalJSON(capabilities, &broker.Capabilities)
		unmarshalJSON(profiles, &broker.Profiles)
		unmarshalJSON(labels, &broker.Labels)
		unmarshalJSON(annotations, &broker.Annotations)

		hosts = append(hosts, broker)
	}

	return &store.ListResult[store.RuntimeBroker]{
		Items:      hosts,
		TotalCount: totalCount,
	}, nil
}

func (s *PostgresStore) UpdateRuntimeBrokerHeartbeat(ctx context.Context, id string, status string) error {
	now := time.Now()

	result, err := s.db.ExecContext(ctx, `
		UPDATE runtime_brokers SET
			status = $1,
			last_heartbeat = $2,
			updated_at = $3
		WHERE id = $4
	`, status, now, now, id)
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
