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

func (s *PostgresStore) CreateProject(ctx context.Context, project *store.Project) error {
	now := time.Now()
	project.Created = now
	project.Updated = now

	_, err := s.db.ExecContext(ctx, `
		INSERT INTO projects (id, name, slug, git_remote, default_runtime_broker_id, labels, annotations, shared_dirs, created_at, updated_at, created_by, owner_id, visibility, github_installation_id, github_permissions, github_app_status, git_identity)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17)
	`,
		project.ID, project.Name, project.Slug, nullableString(project.GitRemote), nullableString(project.DefaultRuntimeBrokerID),
		marshalJSON(project.Labels), marshalJSON(project.Annotations), marshalJSON(project.SharedDirs),
		project.Created, project.Updated, project.CreatedBy, project.OwnerID, project.Visibility,
		nullableInt64(project.GitHubInstallationID), marshalJSONPtr(project.GitHubPermissions), marshalJSONPtr(project.GitHubAppStatus),
		marshalJSONPtr(project.GitIdentity),
	)
	if err != nil {
		if strings.Contains(err.Error(), "duplicate key value violates unique constraint") {
			return store.ErrAlreadyExists
		}
		return err
	}
	return nil
}

func (s *PostgresStore) GetProject(ctx context.Context, id string) (*store.Project, error) {
	project := &store.Project{}
	var labels, annotations, sharedDirs string
	var gitRemote, defaultRuntimeBrokerID sql.NullString
	var githubInstallationID sql.NullInt64
	var githubPermissions, githubAppStatus, gitIdentity string

	err := s.db.QueryRowContext(ctx, `
		SELECT id, name, slug, git_remote, default_runtime_broker_id, labels, annotations, shared_dirs, created_at, updated_at, created_by, owner_id, visibility, github_installation_id, COALESCE(github_permissions, ''), COALESCE(github_app_status, ''), COALESCE(git_identity, '')
		FROM projects WHERE id = $1
	`, id).Scan(
		&project.ID, &project.Name, &project.Slug, &gitRemote, &defaultRuntimeBrokerID,
		&labels, &annotations, &sharedDirs,
		&project.Created, &project.Updated, &project.CreatedBy, &project.OwnerID, &project.Visibility,
		&githubInstallationID, &githubPermissions, &githubAppStatus, &gitIdentity,
	)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, store.ErrNotFound
		}
		return nil, err
	}

	if gitRemote.Valid {
		project.GitRemote = gitRemote.String
	}
	if defaultRuntimeBrokerID.Valid {
		project.DefaultRuntimeBrokerID = defaultRuntimeBrokerID.String
	}
	if githubInstallationID.Valid {
		id := githubInstallationID.Int64
		project.GitHubInstallationID = &id
	}
	unmarshalJSON(labels, &project.Labels)
	unmarshalJSON(annotations, &project.Annotations)
	unmarshalJSON(sharedDirs, &project.SharedDirs)
	if githubPermissions != "" {
		project.GitHubPermissions = &store.GitHubTokenPermissions{}
		unmarshalJSON(githubPermissions, project.GitHubPermissions)
	}
	if githubAppStatus != "" {
		project.GitHubAppStatus = &store.GitHubAppProjectStatus{}
		unmarshalJSON(githubAppStatus, project.GitHubAppStatus)
	}
	if gitIdentity != "" {
		project.GitIdentity = &store.GitIdentityConfig{}
		unmarshalJSON(gitIdentity, project.GitIdentity)
	}

	// Populate computed fields
	s.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM agents WHERE project_id = $1", id).Scan(&project.AgentCount)
	s.db.QueryRowContext(ctx, `
		SELECT (SELECT COUNT(*) FROM project_contributors WHERE project_id = $1 AND status = 'online')
		     + (SELECT COUNT(*) FROM runtime_brokers WHERE auto_provide = 1 AND status = 'online'
		            AND id NOT IN (SELECT broker_id FROM project_contributors WHERE project_id = $2))
	`, id, id).Scan(&project.ActiveBrokerCount)
	s.populateProjectType(ctx, project)

	return project, nil
}

// populateProjectType sets the computed ProjectType field based on how the project was established.
// Type is "linked" (pre-existing local project linked to Hub) or "hub-managed" (created via Hub).
// Whether a project is git-backed is orthogonal — indicated by the GitRemote field.
func (s *PostgresStore) populateProjectType(ctx context.Context, project *store.Project) {
	// Check if any provider has a local_path not under ~/.scion/projects/ (i.e. broker-linked)
	var linkedCount int
	s.db.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM project_contributors WHERE project_id = $1 AND local_path != '' AND local_path NOT LIKE '%/.scion/projects/%'",
		project.ID).Scan(&linkedCount)
	if linkedCount > 0 {
		project.ProjectType = store.ProjectTypeLinked
		return
	}
	project.ProjectType = store.ProjectTypeHubManaged
}

func (s *PostgresStore) GetProjectBySlug(ctx context.Context, slug string) (*store.Project, error) {
	var id string
	err := s.db.QueryRowContext(ctx, "SELECT id FROM projects WHERE slug = $1", slug).Scan(&id)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, store.ErrNotFound
		}
		return nil, err
	}
	return s.GetProject(ctx, id)
}

func (s *PostgresStore) GetProjectBySlugCaseInsensitive(ctx context.Context, slug string) (*store.Project, error) {
	var id string
	err := s.db.QueryRowContext(ctx, "SELECT id FROM projects WHERE LOWER(slug) = LOWER($1)", slug).Scan(&id)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, store.ErrNotFound
		}
		return nil, err
	}
	return s.GetProject(ctx, id)
}

func (s *PostgresStore) GetProjectsByGitRemote(ctx context.Context, gitRemote string) ([]*store.Project, error) {
	rows, err := s.db.QueryContext(ctx, "SELECT id FROM projects WHERE git_remote = $1 ORDER BY created_at ASC", gitRemote)
	if err != nil {
		return nil, err
	}

	// Collect all IDs first, then close the cursor before calling GetProject
	// (SQLite single-connection can't serve a new query while rows are open).
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return nil, err
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	rows.Close()

	projects := make([]*store.Project, 0, len(ids))
	for _, id := range ids {
		project, err := s.GetProject(ctx, id)
		if err != nil {
			return nil, err
		}
		projects = append(projects, project)
	}
	return projects, nil
}

func (s *PostgresStore) NextAvailableSlug(ctx context.Context, baseSlug string) (string, error) {
	// Check if the base slug is available
	var count int
	if err := s.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM projects WHERE slug = $1", baseSlug).Scan(&count); err != nil {
		return "", err
	}
	if count == 0 {
		return baseSlug, nil
	}

	// Find the next available serial suffix
	for i := 1; ; i++ {
		candidate := fmt.Sprintf("%s-%d", baseSlug, i)
		if err := s.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM projects WHERE slug = $1", candidate).Scan(&count); err != nil {
			return "", err
		}
		if count == 0 {
			return candidate, nil
		}
	}
}

func (s *PostgresStore) UpdateProject(ctx context.Context, project *store.Project) error {
	project.Updated = time.Now()

	result, err := s.db.ExecContext(ctx, `
		UPDATE projects SET
			name = $1, slug = $2, git_remote = $3, default_runtime_broker_id = $4,
			labels = $5, annotations = $6, shared_dirs = $7,
			updated_at = $8, owner_id = $9, visibility = $10,
			github_installation_id = $11, github_permissions = $12, github_app_status = $13,
			git_identity = $14
		WHERE id = $15
	`,
		project.Name, project.Slug, nullableString(project.GitRemote), nullableString(project.DefaultRuntimeBrokerID),
		marshalJSON(project.Labels), marshalJSON(project.Annotations), marshalJSON(project.SharedDirs),
		project.Updated, project.OwnerID, project.Visibility,
		nullableInt64(project.GitHubInstallationID), marshalJSONPtr(project.GitHubPermissions), marshalJSONPtr(project.GitHubAppStatus),
		marshalJSONPtr(project.GitIdentity),
		project.ID,
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

func (s *PostgresStore) DeleteProject(ctx context.Context, id string) error {
	result, err := s.db.ExecContext(ctx, "DELETE FROM projects WHERE id = $1", id)
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

func (s *PostgresStore) ListProjects(ctx context.Context, filter store.ProjectFilter, opts store.ListOptions) (*store.ListResult[store.Project], error) {
	var conditions []string
	var args []interface{}
	n := 1

	if len(filter.MemberOrOwnerIDs) > 0 {
		// Combine owner_id match with project ID membership using OR
		placeholders := make([]string, len(filter.MemberOrOwnerIDs))
		for i, id := range filter.MemberOrOwnerIDs {
			placeholders[i] = fmt.Sprintf("$%d", n)
			n++
			args = append(args, id)
		}
		orParts := []string{"id IN (" + strings.Join(placeholders, ",") + ")"}
		if filter.OwnerID != "" {
			orParts = append(orParts, fmt.Sprintf("owner_id = $%d", n))
			n++
			args = append(args, filter.OwnerID)
		}
		conditions = append(conditions, "("+strings.Join(orParts, " OR ")+")")
	} else if len(filter.MemberProjectIDs) > 0 {
		// Strict project ID membership (no owner OR)
		placeholders := make([]string, len(filter.MemberProjectIDs))
		for i, id := range filter.MemberProjectIDs {
			placeholders[i] = fmt.Sprintf("$%d", n)
			n++
			args = append(args, id)
		}
		conditions = append(conditions, "id IN ("+strings.Join(placeholders, ",")+")")
	} else if filter.OwnerID != "" {
		conditions = append(conditions, fmt.Sprintf("owner_id = $%d", n))
		n++
		args = append(args, filter.OwnerID)
	}
	if filter.ExcludeOwnerID != "" {
		conditions = append(conditions, fmt.Sprintf("owner_id != $%d", n))
		n++
		args = append(args, filter.ExcludeOwnerID)
	}
	if filter.Visibility != "" {
		conditions = append(conditions, fmt.Sprintf("visibility = $%d", n))
		n++
		args = append(args, filter.Visibility)
	}
	if filter.GitRemote != "" {
		conditions = append(conditions, fmt.Sprintf("git_remote = $%d", n))
		n++
		args = append(args, filter.GitRemote)
	} else if filter.GitRemotePrefix != "" {
		conditions = append(conditions, fmt.Sprintf("git_remote LIKE $%d", n))
		n++
		args = append(args, filter.GitRemotePrefix+"%")
	}
	if filter.BrokerID != "" {
		conditions = append(conditions, fmt.Sprintf("id IN (SELECT project_id FROM project_contributors WHERE broker_id = $%d)", n))
		n++
		args = append(args, filter.BrokerID)
	}
	if filter.Name != "" {
		conditions = append(conditions, fmt.Sprintf("LOWER(name) = LOWER($%d)", n))
		n++
		args = append(args, filter.Name)
	}
	if filter.Slug != "" {
		conditions = append(conditions, fmt.Sprintf("LOWER(slug) = LOWER($%d)", n))
		n++
		args = append(args, filter.Slug)
	}

	whereClause := ""
	if len(conditions) > 0 {
		whereClause = "WHERE " + strings.Join(conditions, " AND ")
	}

	var totalCount int
	countQuery := fmt.Sprintf("SELECT COUNT(*) FROM projects %s", whereClause)
	if err := s.db.QueryRowContext(ctx, countQuery, args...).Scan(&totalCount); err != nil {
		return nil, err
	}

	limit := opts.Limit
	if limit <= 0 {
		limit = 50
	}

	query := fmt.Sprintf(`
		SELECT id, name, slug, git_remote, default_runtime_broker_id, labels, annotations, shared_dirs, created_at, updated_at, created_by, owner_id, visibility,
		       github_installation_id, COALESCE(github_permissions, ''), COALESCE(github_app_status, ''), COALESCE(git_identity, '')
		FROM projects %s ORDER BY created_at DESC LIMIT $%d
	`, whereClause, n)
	args = append(args, limit)

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var projects []store.Project
	type projectRow struct {
		project              store.Project
		labels               string
		annotations          string
		sharedDirs           string
		gitRemote            sql.NullString
		brokerID             sql.NullString
		githubInstallationID sql.NullInt64
		githubPermissions    string
		githubAppStatus      string
		gitIdentity          string
	}
	var rowData []projectRow

	for rows.Next() {
		var r projectRow
		if err := rows.Scan(
			&r.project.ID, &r.project.Name, &r.project.Slug, &r.gitRemote, &r.brokerID,
			&r.labels, &r.annotations, &r.sharedDirs,
			&r.project.Created, &r.project.Updated, &r.project.CreatedBy, &r.project.OwnerID, &r.project.Visibility,
			&r.githubInstallationID, &r.githubPermissions, &r.githubAppStatus, &r.gitIdentity,
		); err != nil {
			return nil, err
		}
		rowData = append(rowData, r)
	}
	rows.Close() // Close early to release connection for nested queries

	for _, r := range rowData {
		project := r.project
		if r.gitRemote.Valid {
			project.GitRemote = r.gitRemote.String
		}
		if r.brokerID.Valid {
			project.DefaultRuntimeBrokerID = r.brokerID.String
		}
		if r.githubInstallationID.Valid {
			id := r.githubInstallationID.Int64
			project.GitHubInstallationID = &id
		}
		unmarshalJSON(r.labels, &project.Labels)
		unmarshalJSON(r.annotations, &project.Annotations)
		unmarshalJSON(r.sharedDirs, &project.SharedDirs)
		if r.githubPermissions != "" {
			project.GitHubPermissions = &store.GitHubTokenPermissions{}
			unmarshalJSON(r.githubPermissions, project.GitHubPermissions)
		}
		if r.githubAppStatus != "" {
			project.GitHubAppStatus = &store.GitHubAppProjectStatus{}
			unmarshalJSON(r.githubAppStatus, project.GitHubAppStatus)
		}
		if r.gitIdentity != "" {
			project.GitIdentity = &store.GitIdentityConfig{}
			unmarshalJSON(r.gitIdentity, project.GitIdentity)
		}

		// Populate computed fields - these now have a connection available
		s.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM agents WHERE project_id = $1", project.ID).Scan(&project.AgentCount)
		s.db.QueryRowContext(ctx, `
			SELECT (SELECT COUNT(*) FROM project_contributors WHERE project_id = $1 AND status = 'online')
			     + (SELECT COUNT(*) FROM runtime_brokers WHERE auto_provide = 1 AND status = 'online'
			            AND id NOT IN (SELECT broker_id FROM project_contributors WHERE project_id = $2))
		`, project.ID, project.ID).Scan(&project.ActiveBrokerCount)
		s.populateProjectType(ctx, &project)

		projects = append(projects, project)
	}

	return &store.ListResult[store.Project]{
		Items:      projects,
		TotalCount: totalCount,
	}, nil
}
