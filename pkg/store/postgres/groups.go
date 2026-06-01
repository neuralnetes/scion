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

func (s *PostgresStore) CreateGroup(ctx context.Context, group *store.Group) error {
	now := time.Now()
	group.Created = now
	group.Updated = now
	if group.GroupType == "" {
		group.GroupType = store.GroupTypeExplicit
	}

	_, err := s.db.ExecContext(ctx, `
		INSERT INTO groups (id, name, slug, description, group_type, project_id, parent_id, labels, annotations, created_at, updated_at, created_by, owner_id)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13)
	`,
		group.ID, group.Name, group.Slug, group.Description,
		group.GroupType, nullableString(group.ProjectID),
		nullableString(group.ParentID),
		marshalJSON(group.Labels), marshalJSON(group.Annotations),
		group.Created, group.Updated, group.CreatedBy, group.OwnerID,
	)
	if err != nil {
		if strings.Contains(err.Error(), "UNIQUE constraint failed") || strings.Contains(err.Error(), "duplicate key") {
			return store.ErrAlreadyExists
		}
		return err
	}
	return nil
}

func (s *PostgresStore) GetGroup(ctx context.Context, id string) (*store.Group, error) {
	group := &store.Group{}
	var labels, annotations string
	var parentID, projectID sql.NullString

	err := s.db.QueryRowContext(ctx, `
		SELECT id, name, slug, description, group_type, project_id, parent_id, labels, annotations, created_at, updated_at, created_by, owner_id
		FROM groups WHERE id = $1
	`, id).Scan(
		&group.ID, &group.Name, &group.Slug, &group.Description,
		&group.GroupType, &projectID,
		&parentID,
		&labels, &annotations,
		&group.Created, &group.Updated, &group.CreatedBy, &group.OwnerID,
	)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, store.ErrNotFound
		}
		return nil, err
	}

	if parentID.Valid {
		group.ParentID = parentID.String
	}
	if projectID.Valid {
		group.ProjectID = projectID.String
	}
	unmarshalJSON(labels, &group.Labels)
	unmarshalJSON(annotations, &group.Annotations)
	if group.GroupType == "" {
		group.GroupType = store.GroupTypeExplicit
	}

	return group, nil
}

func (s *PostgresStore) GetGroupBySlug(ctx context.Context, slug string) (*store.Group, error) {
	var id string
	err := s.db.QueryRowContext(ctx, "SELECT id FROM groups WHERE slug = $1", slug).Scan(&id)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, store.ErrNotFound
		}
		return nil, err
	}
	return s.GetGroup(ctx, id)
}

func (s *PostgresStore) UpdateGroup(ctx context.Context, group *store.Group) error {
	group.Updated = time.Now()

	result, err := s.db.ExecContext(ctx, `
		UPDATE groups SET
			name = $1, slug = $2, description = $3, group_type = $4, project_id = $5,
			parent_id = $6, labels = $7, annotations = $8,
			updated_at = $9, owner_id = $10
		WHERE id = $11
	`,
		group.Name, group.Slug, group.Description,
		group.GroupType, nullableString(group.ProjectID),
		nullableString(group.ParentID),
		marshalJSON(group.Labels), marshalJSON(group.Annotations),
		group.Updated, group.OwnerID,
		group.ID,
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

func (s *PostgresStore) DeleteGroup(ctx context.Context, id string) error {
	result, err := s.db.ExecContext(ctx, "DELETE FROM groups WHERE id = $1", id)
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

func (s *PostgresStore) ListGroups(ctx context.Context, filter store.GroupFilter, opts store.ListOptions) (*store.ListResult[store.Group], error) {
	var conditions []string
	var args []interface{}

	if filter.OwnerID != "" {
		conditions = append(conditions, fmt.Sprintf("owner_id = $%d", len(args)+1))
		args = append(args, filter.OwnerID)
	}
	if filter.ParentID != "" {
		conditions = append(conditions, fmt.Sprintf("parent_id = $%d", len(args)+1))
		args = append(args, filter.ParentID)
	}
	if filter.GroupType != "" {
		conditions = append(conditions, fmt.Sprintf("group_type = $%d", len(args)+1))
		args = append(args, filter.GroupType)
	}
	if filter.ProjectID != "" {
		conditions = append(conditions, fmt.Sprintf("project_id = $%d", len(args)+1))
		args = append(args, filter.ProjectID)
	}

	whereClause := ""
	if len(conditions) > 0 {
		whereClause = "WHERE " + strings.Join(conditions, " AND ")
	}

	var totalCount int
	countQuery := fmt.Sprintf("SELECT COUNT(*) FROM groups %s", whereClause)
	if err := s.db.QueryRowContext(ctx, countQuery, args...).Scan(&totalCount); err != nil {
		return nil, err
	}

	limit := opts.Limit
	if limit <= 0 {
		limit = 50
	}

	query := fmt.Sprintf(`
		SELECT id, name, slug, description, group_type, project_id, parent_id, labels, annotations, created_at, updated_at, created_by, owner_id
		FROM groups %s ORDER BY created_at DESC LIMIT $%d
	`, whereClause, len(args)+1)
	args = append(args, limit)

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var groups []store.Group
	for rows.Next() {
		var group store.Group
		var labels, annotations string
		var parentID, projectID sql.NullString

		if err := rows.Scan(
			&group.ID, &group.Name, &group.Slug, &group.Description,
			&group.GroupType, &projectID,
			&parentID,
			&labels, &annotations,
			&group.Created, &group.Updated, &group.CreatedBy, &group.OwnerID,
		); err != nil {
			return nil, err
		}

		if parentID.Valid {
			group.ParentID = parentID.String
		}
		if projectID.Valid {
			group.ProjectID = projectID.String
		}
		unmarshalJSON(labels, &group.Labels)
		unmarshalJSON(annotations, &group.Annotations)
		if group.GroupType == "" {
			group.GroupType = store.GroupTypeExplicit
		}

		groups = append(groups, group)
	}

	return &store.ListResult[store.Group]{
		Items:      groups,
		TotalCount: totalCount,
	}, nil
}

func (s *PostgresStore) AddGroupMember(ctx context.Context, member *store.GroupMember) error {
	if member.AddedAt.IsZero() {
		member.AddedAt = time.Now()
	}

	_, err := s.db.ExecContext(ctx, `
		INSERT INTO group_members (group_id, member_type, member_id, role, added_at, added_by)
		VALUES ($1, $2, $3, $4, $5, $6)
	`,
		member.GroupID, member.MemberType, member.MemberID, member.Role, member.AddedAt, member.AddedBy,
	)
	if err != nil {
		if strings.Contains(err.Error(), "UNIQUE constraint failed") || strings.Contains(err.Error(), "duplicate key") || strings.Contains(err.Error(), "PRIMARY KEY constraint failed") {
			return store.ErrAlreadyExists
		}
		return err
	}
	return nil
}

func (s *PostgresStore) UpdateGroupMemberRole(ctx context.Context, groupID, memberType, memberID, newRole string) error {
	result, err := s.db.ExecContext(ctx,
		`UPDATE group_members SET role = $1 WHERE group_id = $2 AND member_type = $3 AND member_id = $4`,
		newRole, groupID, memberType, memberID,
	)
	if err != nil {
		return err
	}
	rows, _ := result.RowsAffected()
	if rows == 0 {
		return store.ErrNotFound
	}
	return nil
}

func (s *PostgresStore) RemoveGroupMember(ctx context.Context, groupID, memberType, memberID string) error {
	result, err := s.db.ExecContext(ctx,
		"DELETE FROM group_members WHERE group_id = $1 AND member_type = $2 AND member_id = $3",
		groupID, memberType, memberID,
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

func (s *PostgresStore) GetGroupMembers(ctx context.Context, groupID string) ([]store.GroupMember, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT group_id, member_type, member_id, role, added_at, added_by
		FROM group_members WHERE group_id = $1
	`, groupID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var members []store.GroupMember
	for rows.Next() {
		var member store.GroupMember
		if err := rows.Scan(
			&member.GroupID, &member.MemberType, &member.MemberID, &member.Role, &member.AddedAt, &member.AddedBy,
		); err != nil {
			return nil, err
		}
		members = append(members, member)
	}

	return members, nil
}

func (s *PostgresStore) GetUserGroups(ctx context.Context, userID string) ([]store.GroupMember, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT group_id, member_type, member_id, role, added_at, added_by
		FROM group_members WHERE member_type = 'user' AND member_id = $1
	`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var memberships []store.GroupMember
	for rows.Next() {
		var member store.GroupMember
		if err := rows.Scan(
			&member.GroupID, &member.MemberType, &member.MemberID, &member.Role, &member.AddedAt, &member.AddedBy,
		); err != nil {
			return nil, err
		}
		memberships = append(memberships, member)
	}

	return memberships, nil
}

func (s *PostgresStore) GetGroupMembership(ctx context.Context, groupID, memberType, memberID string) (*store.GroupMember, error) {
	member := &store.GroupMember{}

	err := s.db.QueryRowContext(ctx, `
		SELECT group_id, member_type, member_id, role, added_at, added_by
		FROM group_members WHERE group_id = $1 AND member_type = $2 AND member_id = $3
	`, groupID, memberType, memberID).Scan(
		&member.GroupID, &member.MemberType, &member.MemberID, &member.Role, &member.AddedAt, &member.AddedBy,
	)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, store.ErrNotFound
		}
		return nil, err
	}

	return member, nil
}

// WouldCreateCycle checks if adding memberGroupID as a member of groupID would create a cycle.
// A cycle exists if groupID is reachable from memberGroupID by following the containment relationship.
// Example: if A contains B, and we try to add A as member of B, we'd have A->B->A (cycle).
func (s *PostgresStore) WouldCreateCycle(ctx context.Context, groupID, memberGroupID string) (bool, error) {
	// If they're the same, it's a direct cycle
	if groupID == memberGroupID {
		return true, nil
	}

	// Check if groupID is reachable from memberGroupID by traversing DOWN the containment graph
	// (i.e., checking what groups memberGroupID contains, and what those contain, etc.)
	visited := make(map[string]bool)
	return s.hasPathDown(ctx, memberGroupID, groupID, visited)
}

// hasPathDown checks if 'target' is reachable from 'current' by following containment.
// It looks at what groups 'current' contains as members.
func (s *PostgresStore) hasPathDown(ctx context.Context, current, target string, visited map[string]bool) (bool, error) {
	if current == target {
		return true, nil
	}
	if visited[current] {
		return false, nil
	}
	visited[current] = true

	// Get all groups that 'current' contains (groups where current is the group_id)
	rows, err := s.db.QueryContext(ctx,
		"SELECT member_id FROM group_members WHERE member_type = 'group' AND group_id = $1", current)
	if err != nil {
		return false, err
	}
	defer rows.Close()

	for rows.Next() {
		var childGroupID string
		if err := rows.Scan(&childGroupID); err != nil {
			return false, err
		}
		found, err := s.hasPathDown(ctx, childGroupID, target, visited)
		if err != nil {
			return false, err
		}
		if found {
			return true, nil
		}
	}

	return false, nil
}

// GetEffectiveGroups returns all groups a user belongs to, including transitive memberships.
func (s *PostgresStore) GetEffectiveGroups(ctx context.Context, userID string) ([]string, error) {
	// Start with direct group memberships
	directMemberships, err := s.GetUserGroups(ctx, userID)
	if err != nil {
		return nil, err
	}

	effectiveGroups := make(map[string]bool)
	for _, m := range directMemberships {
		effectiveGroups[m.GroupID] = true
		// Add transitive group memberships
		if err := s.addTransitiveGroups(ctx, m.GroupID, effectiveGroups); err != nil {
			return nil, err
		}
	}

	result := make([]string, 0, len(effectiveGroups))
	for groupID := range effectiveGroups {
		result = append(result, groupID)
	}

	return result, nil
}

// addTransitiveGroups recursively adds all groups that contain the given group.
func (s *PostgresStore) addTransitiveGroups(ctx context.Context, groupID string, visited map[string]bool) error {
	// Find all groups where this group is a member
	rows, err := s.db.QueryContext(ctx,
		"SELECT group_id FROM group_members WHERE member_type = 'group' AND member_id = $1", groupID)
	if err != nil {
		return err
	}

	// Collect all parent group IDs first, then close rows before recursing
	// This avoids issues with SQLite connections during recursive queries
	var parentGroupIDs []string
	for rows.Next() {
		var parentGroupID string
		if err := rows.Scan(&parentGroupID); err != nil {
			rows.Close()
			return err
		}
		parentGroupIDs = append(parentGroupIDs, parentGroupID)
	}
	rows.Close()

	// Now recurse after rows are closed
	for _, parentGroupID := range parentGroupIDs {
		if !visited[parentGroupID] {
			visited[parentGroupID] = true
			if err := s.addTransitiveGroups(ctx, parentGroupID, visited); err != nil {
				return err
			}
		}
	}

	return nil
}

// GetGroupByProjectID retrieves the project_agents group associated with a project.
func (s *PostgresStore) GetGroupByProjectID(ctx context.Context, projectID string) (*store.Group, error) {
	var id string
	err := s.db.QueryRowContext(ctx, "SELECT id FROM groups WHERE project_id = $1 AND group_type = $2 LIMIT 1",
		projectID, store.GroupTypeProjectAgents).Scan(&id)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, store.ErrNotFound
		}
		return nil, err
	}
	return s.GetGroup(ctx, id)
}

// GetEffectiveGroupsForAgent returns all groups an agent belongs to.
func (s *PostgresStore) GetEffectiveGroupsForAgent(ctx context.Context, agentID string) ([]string, error) {
	return nil, nil
}

// CheckDelegatedAccess is a stub for the SQLite store. Delegation resolution
// is implemented in the Ent adapter.
func (s *PostgresStore) CheckDelegatedAccess(ctx context.Context, agentID string, conditions *store.PolicyConditions) (bool, error) {
	return false, nil
}

// GetGroupsByIDs is a stub for the SQLite store. Group retrieval by IDs
// is implemented in the Ent adapter.
func (s *PostgresStore) GetGroupsByIDs(ctx context.Context, ids []string) ([]store.Group, error) {
	if len(ids) == 0 {
		return nil, nil
	}

	placeholders := make([]string, len(ids))
	args := make([]interface{}, len(ids))
	for i, id := range ids {
		placeholders[i] = fmt.Sprintf("$%d", i+1)
		args[i] = id
	}

	rows, err := s.db.QueryContext(ctx,
		`SELECT id, name, slug, description, group_type, project_id, parent_id, labels, annotations, created_at, updated_at, created_by, owner_id
		FROM groups WHERE id IN (`+strings.Join(placeholders, ",")+`)`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var groups []store.Group
	for rows.Next() {
		var g store.Group
		var labels, annotations string
		var parentID, projectID sql.NullString
		if err := rows.Scan(
			&g.ID, &g.Name, &g.Slug, &g.Description,
			&g.GroupType, &projectID,
			&parentID,
			&labels, &annotations,
			&g.Created, &g.Updated, &g.CreatedBy, &g.OwnerID,
		); err != nil {
			return nil, err
		}
		if parentID.Valid {
			g.ParentID = parentID.String
		}
		if projectID.Valid {
			g.ProjectID = projectID.String
		}
		unmarshalJSON(labels, &g.Labels)
		unmarshalJSON(annotations, &g.Annotations)
		if g.GroupType == "" {
			g.GroupType = store.GroupTypeExplicit
		}
		groups = append(groups, g)
	}

	return groups, rows.Err()
}

func (s *PostgresStore) CountGroupMembersByRole(ctx context.Context, groupID, role string) (int, error) {
	var count int
	err := s.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM group_members WHERE group_id = $1 AND role = $2`,
		groupID, role,
	).Scan(&count)
	if err != nil {
		return 0, err
	}
	return count, nil
}
