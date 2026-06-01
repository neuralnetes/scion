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
	"os"
	"testing"
	"time"

	"github.com/GoogleCloudPlatform/scion/pkg/api"
	"github.com/GoogleCloudPlatform/scion/pkg/store"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const envVarDSN = "SCION_TEST_POSTGRES_URL"

// resetSchema drops and recreates the public schema, giving each test a clean slate.
func resetSchema(t *testing.T, s *PostgresStore) {
	t.Helper()
	ctx := context.Background()
	_, err := s.db.ExecContext(ctx, `DROP SCHEMA public CASCADE; CREATE SCHEMA public;`)
	require.NoError(t, err, "resetSchema")
}

// newTestStore opens a PostgresStore against the live DB and applies all
// migrations. It skips the test when SCION_TEST_POSTGRES_URL is not set.
func newTestStore(t *testing.T) *PostgresStore {
	t.Helper()
	dsn := os.Getenv(envVarDSN)
	if dsn == "" {
		t.Skipf("set %s to run Postgres tests", envVarDSN)
	}

	s, err := New(dsn)
	require.NoError(t, err)

	resetSchema(t, s)

	ctx := context.Background()
	err = s.Migrate(ctx)
	require.NoError(t, err, "Migrate")

	t.Cleanup(func() { s.Close() })
	return s
}

// ============================================================================
// Migration Tests
// ============================================================================

func TestMigrate(t *testing.T) {
	dsn := os.Getenv(envVarDSN)
	if dsn == "" {
		t.Skipf("set %s to run Postgres tests", envVarDSN)
	}

	s, err := New(dsn)
	require.NoError(t, err)
	defer s.Close()

	resetSchema(t, s)

	ctx := context.Background()

	// First run: all 53 migrations must apply cleanly.
	err = s.Migrate(ctx)
	require.NoError(t, err, "first Migrate must succeed")

	// Verify final version in schema_migrations.
	var version int
	err = s.db.QueryRowContext(ctx, "SELECT MAX(version) FROM schema_migrations").Scan(&version)
	require.NoError(t, err)
	assert.Equal(t, 53, version, "all 53 migrations should be applied")

	// Second run: must be idempotent (no error, no re-apply).
	err = s.Migrate(ctx)
	require.NoError(t, err, "second Migrate must be idempotent")

	var version2 int
	err = s.db.QueryRowContext(ctx, "SELECT MAX(version) FROM schema_migrations").Scan(&version2)
	require.NoError(t, err)
	assert.Equal(t, 53, version2, "idempotent run must not change version")
}

// ============================================================================
// User Tests
// ============================================================================

func TestPGUserCRUD(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	user := &store.User{
		ID:          api.NewUUID(),
		Email:       "alice@example.com",
		DisplayName: "Alice",
		Role:        store.UserRoleMember,
		Status:      "active",
		Preferences: &store.UserPreferences{Theme: "dark"},
	}

	require.NoError(t, s.CreateUser(ctx, user))
	assert.NotZero(t, user.Created)

	// Get by ID
	got, err := s.GetUser(ctx, user.ID)
	require.NoError(t, err)
	assert.Equal(t, user.Email, got.Email)
	assert.Equal(t, "dark", got.Preferences.Theme)

	// Get by email
	got2, err := s.GetUserByEmail(ctx, "alice@example.com")
	require.NoError(t, err)
	assert.Equal(t, user.ID, got2.ID)

	// Duplicate email returns ErrAlreadyExists
	dup := &store.User{
		ID: api.NewUUID(), Email: "alice@example.com",
		DisplayName: "Alice2", Role: store.UserRoleMember, Status: "active",
	}
	err = s.CreateUser(ctx, dup)
	assert.ErrorIs(t, err, store.ErrAlreadyExists)

	// Update
	got.DisplayName = "Alice Updated"
	got.LastLogin = time.Now()
	require.NoError(t, s.UpdateUser(ctx, got))

	got3, err := s.GetUser(ctx, user.ID)
	require.NoError(t, err)
	assert.Equal(t, "Alice Updated", got3.DisplayName)
	assert.NotZero(t, got3.LastLogin)

	// Delete
	require.NoError(t, s.DeleteUser(ctx, user.ID))
	_, err = s.GetUser(ctx, user.ID)
	assert.ErrorIs(t, err, store.ErrNotFound)
}

func TestPGUserList(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	for i := 0; i < 3; i++ {
		u := &store.User{
			ID:          api.NewUUID(),
			Email:       "user" + string(rune('a'+i)) + "@example.com",
			DisplayName: "User " + string(rune('A'+i)),
			Role:        store.UserRoleMember,
			Status:      "active",
		}
		if i == 0 {
			u.Role = store.UserRoleAdmin
		}
		require.NoError(t, s.CreateUser(ctx, u))
	}

	result, err := s.ListUsers(ctx, store.UserFilter{}, store.ListOptions{})
	require.NoError(t, err)
	assert.Equal(t, 3, result.TotalCount)

	result, err = s.ListUsers(ctx, store.UserFilter{Role: store.UserRoleAdmin}, store.ListOptions{})
	require.NoError(t, err)
	assert.Equal(t, 1, result.TotalCount)
}

// ============================================================================
// Project Tests
// ============================================================================

func TestPGProjectCRUD(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	project := &store.Project{
		ID:         api.NewUUID(),
		Name:       "My Project",
		Slug:       "my-project",
		GitRemote:  "github.com/org/repo",
		Visibility: store.VisibilityPrivate,
		Labels:     map[string]string{"team": "platform"},
	}

	require.NoError(t, s.CreateProject(ctx, project))
	assert.NotZero(t, project.Created)

	got, err := s.GetProject(ctx, project.ID)
	require.NoError(t, err)
	assert.Equal(t, project.Name, got.Name)
	assert.Equal(t, "platform", got.Labels["team"])

	// Slug uniqueness
	dup := &store.Project{
		ID: api.NewUUID(), Name: "Dup", Slug: "my-project", Visibility: store.VisibilityPrivate,
	}
	err = s.CreateProject(ctx, dup)
	assert.ErrorIs(t, err, store.ErrAlreadyExists)

	// Update
	got.Name = "Updated Project"
	require.NoError(t, s.UpdateProject(ctx, got))

	got2, err := s.GetProject(ctx, project.ID)
	require.NoError(t, err)
	assert.Equal(t, "Updated Project", got2.Name)

	// Delete
	require.NoError(t, s.DeleteProject(ctx, project.ID))
	_, err = s.GetProject(ctx, project.ID)
	assert.ErrorIs(t, err, store.ErrNotFound)
}

// ============================================================================
// Agent Tests
// ============================================================================

func TestPGAgentCRUD(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	project := &store.Project{
		ID:         api.NewUUID(),
		Name:       "Test Project",
		Slug:       "test-project",
		Visibility: store.VisibilityPrivate,
	}
	require.NoError(t, s.CreateProject(ctx, project))

	agent := &store.Agent{
		ID:         api.NewUUID(),
		Slug:       "test-agent",
		Name:       "Test Agent",
		Template:   "claude",
		ProjectID:  project.ID,
		Phase:      "created",
		Visibility: store.VisibilityPrivate,
		Labels:     map[string]string{"env": "test"},
	}

	require.NoError(t, s.CreateAgent(ctx, agent))
	assert.NotZero(t, agent.Created)
	assert.Equal(t, int64(1), agent.StateVersion)

	got, err := s.GetAgent(ctx, agent.ID)
	require.NoError(t, err)
	assert.Equal(t, agent.Slug, got.Slug)
	assert.Equal(t, "test", got.Labels["env"])

	got, err = s.GetAgentBySlug(ctx, project.ID, "test-agent")
	require.NoError(t, err)
	assert.Equal(t, agent.ID, got.ID)

	got.Name = "Updated Agent"
	got.Phase = "running"
	require.NoError(t, s.UpdateAgent(ctx, got))
	assert.Equal(t, int64(2), got.StateVersion)

	got2, err := s.GetAgent(ctx, agent.ID)
	require.NoError(t, err)
	assert.Equal(t, "Updated Agent", got2.Name)

	// Version conflict
	got2.StateVersion = 1
	err = s.UpdateAgent(ctx, got2)
	assert.ErrorIs(t, err, store.ErrVersionConflict)

	require.NoError(t, s.DeleteAgent(ctx, agent.ID))
	_, err = s.GetAgent(ctx, agent.ID)
	assert.ErrorIs(t, err, store.ErrNotFound)
}

func TestPGAgentList(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	project := &store.Project{
		ID:         api.NewUUID(),
		Name:       "Test Project",
		Slug:       "test-project",
		Visibility: store.VisibilityPrivate,
	}
	require.NoError(t, s.CreateProject(ctx, project))

	for i := 0; i < 5; i++ {
		agent := &store.Agent{
			ID:         api.NewUUID(),
			Slug:       "agent-" + string(rune('a'+i)),
			Name:       "Agent " + string(rune('A'+i)),
			Template:   "claude",
			ProjectID:  project.ID,
			Phase:      "running",
			Visibility: store.VisibilityPrivate,
		}
		if i%2 == 0 {
			agent.Phase = "stopped"
		}
		require.NoError(t, s.CreateAgent(ctx, agent))
	}

	result, err := s.ListAgents(ctx, store.AgentFilter{}, store.ListOptions{})
	require.NoError(t, err)
	assert.Equal(t, 5, result.TotalCount)

	result, err = s.ListAgents(ctx, store.AgentFilter{Phase: "running"}, store.ListOptions{})
	require.NoError(t, err)
	assert.Equal(t, 2, result.TotalCount)

	result, err = s.ListAgents(ctx, store.AgentFilter{ProjectID: project.ID}, store.ListOptions{})
	require.NoError(t, err)
	assert.Equal(t, 5, result.TotalCount)

	result, err = s.ListAgents(ctx, store.AgentFilter{}, store.ListOptions{Limit: 2})
	require.NoError(t, err)
	assert.Len(t, result.Items, 2)
}

// ============================================================================
// Secret Tests (incl. Upsert)
// ============================================================================

func TestPGSecretCRUD(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	secret := &store.Secret{
		ID:             api.NewUUID(),
		Key:            "MY_SECRET",
		EncryptedValue: "enc_val_1",
		Scope:          store.ScopeHub,
		ScopeID:        "hub-1",
		SecretType:     store.SecretTypeEnvironment,
		CreatedBy:      "user-1",
		UpdatedBy:      "user-1",
	}

	require.NoError(t, s.CreateSecret(ctx, secret))
	assert.NotZero(t, secret.Created)
	assert.Equal(t, 1, secret.Version)

	got, err := s.GetSecret(ctx, "MY_SECRET", store.ScopeHub, "hub-1")
	require.NoError(t, err)
	assert.Equal(t, secret.ID, got.ID)
	assert.Equal(t, "MY_SECRET", got.Key)

	got.EncryptedValue = "enc_val_2"
	require.NoError(t, s.UpdateSecret(ctx, got))
	assert.Equal(t, 2, got.Version)

	got2, err := s.GetSecretValue(ctx, "MY_SECRET", store.ScopeHub, "hub-1")
	require.NoError(t, err)
	assert.Equal(t, "enc_val_2", got2)

	require.NoError(t, s.DeleteSecret(ctx, "MY_SECRET", store.ScopeHub, "hub-1"))
	_, err = s.GetSecret(ctx, "MY_SECRET", store.ScopeHub, "hub-1")
	assert.ErrorIs(t, err, store.ErrNotFound)
}

func TestPGSecretUpsert(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	secret := &store.Secret{
		ID:             api.NewUUID(),
		Key:            "UPSERT_KEY",
		EncryptedValue: "first_value",
		Scope:          store.ScopeUser,
		ScopeID:        "u1",
		CreatedBy:      "user-1",
		UpdatedBy:      "user-1",
	}

	created, err := s.UpsertSecret(ctx, secret)
	require.NoError(t, err)
	assert.True(t, created, "first upsert should create")

	// Second upsert with same key/scope should update
	secret2 := &store.Secret{
		Key:            "UPSERT_KEY",
		EncryptedValue: "second_value",
		Scope:          store.ScopeUser,
		ScopeID:        "u1",
		UpdatedBy:      "user-1",
	}
	created2, err := s.UpsertSecret(ctx, secret2)
	require.NoError(t, err)
	assert.False(t, created2, "second upsert should update, not create")

	got, err := s.GetSecretValue(ctx, "UPSERT_KEY", store.ScopeUser, "u1")
	require.NoError(t, err)
	assert.Equal(t, "second_value", got)
}

func TestPGListSecrets(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	for i := 0; i < 3; i++ {
		require.NoError(t, s.CreateSecret(ctx, &store.Secret{
			ID:             api.NewUUID(),
			Key:            "KEY_" + string(rune('A'+i)),
			EncryptedValue: "val",
			Scope:          store.ScopeProject,
			ScopeID:        "proj-1",
		}))
	}
	// One in a different scope
	require.NoError(t, s.CreateSecret(ctx, &store.Secret{
		ID:             api.NewUUID(),
		Key:            "HUB_KEY",
		EncryptedValue: "val",
		Scope:          store.ScopeHub,
		ScopeID:        "hub-1",
	}))

	secrets, err := s.ListSecrets(ctx, store.SecretFilter{Scope: store.ScopeProject, ScopeID: "proj-1"})
	require.NoError(t, err)
	assert.Len(t, secrets, 3)

	n, err := s.DeleteSecretsByScope(ctx, store.ScopeProject, "proj-1")
	require.NoError(t, err)
	assert.Equal(t, 3, n)
}

// ============================================================================
// Group Tests (+membership)
// ============================================================================

func TestPGGroupCRUD(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	group := &store.Group{
		ID:          api.NewUUID(),
		Name:        "Engineering",
		Slug:        "engineering",
		Description: "Eng team",
		Created:     time.Now(),
		Updated:     time.Now(),
	}
	require.NoError(t, s.CreateGroup(ctx, group))

	got, err := s.GetGroup(ctx, group.ID)
	require.NoError(t, err)
	assert.Equal(t, "Engineering", got.Name)

	got.Description = "Eng team updated"
	require.NoError(t, s.UpdateGroup(ctx, got))

	got2, err := s.GetGroup(ctx, group.ID)
	require.NoError(t, err)
	assert.Equal(t, "Eng team updated", got2.Description)

	require.NoError(t, s.DeleteGroup(ctx, group.ID))
	_, err = s.GetGroup(ctx, group.ID)
	assert.ErrorIs(t, err, store.ErrNotFound)
}

func TestPGGroupMembership(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	group := &store.Group{
		ID:      api.NewUUID(),
		Name:    "Alpha",
		Slug:    "alpha",
		Created: time.Now(),
		Updated: time.Now(),
	}
	require.NoError(t, s.CreateGroup(ctx, group))

	member := &store.GroupMember{
		GroupID:    group.ID,
		MemberType: "user",
		MemberID:   "user-42",
		Role:       "member",
		AddedAt:    time.Now(),
	}
	require.NoError(t, s.AddGroupMember(ctx, member))

	members, err := s.GetGroupMembers(ctx, group.ID)
	require.NoError(t, err)
	assert.Len(t, members, 1)
	assert.Equal(t, "user-42", members[0].MemberID)

	groups, err := s.GetEffectiveGroups(ctx, "user-42")
	require.NoError(t, err)
	assert.Contains(t, groups, group.ID)

	require.NoError(t, s.RemoveGroupMember(ctx, group.ID, "user", "user-42"))
	members, err = s.GetGroupMembers(ctx, group.ID)
	require.NoError(t, err)
	assert.Len(t, members, 0)
}

// ============================================================================
// Policy Tests
// ============================================================================

func TestPGPolicyCRUD(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	policy := &store.Policy{
		ID:           api.NewUUID(),
		Name:         "ReadPolicy",
		ScopeType:    store.PolicyScopeHub,
		ScopeID:      "hub-1",
		ResourceType: "*",
		Actions:      []string{"read"},
		Effect:       "allow",
		Priority:     10,
		Created:      time.Now(),
		Updated:      time.Now(),
	}
	require.NoError(t, s.CreatePolicy(ctx, policy))

	got, err := s.GetPolicy(ctx, policy.ID)
	require.NoError(t, err)
	assert.Equal(t, "ReadPolicy", got.Name)
	assert.Equal(t, []string{"read"}, got.Actions)

	result, err := s.ListPolicies(ctx, store.PolicyFilter{ScopeType: store.PolicyScopeHub}, store.ListOptions{})
	require.NoError(t, err)
	assert.Equal(t, 1, result.TotalCount)

	require.NoError(t, s.DeletePolicy(ctx, policy.ID))
	_, err = s.GetPolicy(ctx, policy.ID)
	assert.ErrorIs(t, err, store.ErrNotFound)
}

// ============================================================================
// InviteCode Tests
// ============================================================================

func TestPGInviteCode(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	invite := &store.InviteCode{
		ID:         api.NewUUID(),
		CodeHash:   "hash-abc",
		CodePrefix: "scion_inv_",
		MaxUses:    5,
		ExpiresAt:  time.Now().Add(24 * time.Hour),
		CreatedBy:  "admin",
		Note:       "test invite",
		Created:    time.Now(),
	}
	require.NoError(t, s.CreateInviteCode(ctx, invite))

	got, err := s.GetInviteCodeByHash(ctx, "hash-abc")
	require.NoError(t, err)
	assert.Equal(t, invite.ID, got.ID)
	assert.Equal(t, "test invite", got.Note)

	require.NoError(t, s.IncrementInviteUseCount(ctx, invite.ID))

	got2, err := s.GetInviteCodeByHash(ctx, "hash-abc")
	require.NoError(t, err)
	assert.Equal(t, 1, got2.UseCount)

	require.NoError(t, s.RevokeInviteCode(ctx, invite.ID))

	got3, err := s.GetInviteCodeByHash(ctx, "hash-abc")
	require.NoError(t, err)
	assert.True(t, got3.Revoked)
}

// ============================================================================
// EnvVar Tests
// ============================================================================

func TestPGEnvVarCRUD(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	ev := &store.EnvVar{
		ID:      api.NewUUID(),
		Key:     "DATABASE_URL",
		Value:   "postgres://localhost/mydb",
		Scope:   store.ScopeProject,
		ScopeID: "proj-1",
	}
	require.NoError(t, s.CreateEnvVar(ctx, ev))
	assert.NotZero(t, ev.Created)

	got, err := s.GetEnvVar(ctx, "DATABASE_URL", store.ScopeProject, "proj-1")
	require.NoError(t, err)
	assert.Equal(t, ev.Value, got.Value)

	got.Value = "postgres://localhost/newdb"
	require.NoError(t, s.UpdateEnvVar(ctx, got))

	got2, err := s.GetEnvVar(ctx, "DATABASE_URL", store.ScopeProject, "proj-1")
	require.NoError(t, err)
	assert.Equal(t, "postgres://localhost/newdb", got2.Value)

	require.NoError(t, s.DeleteEnvVar(ctx, "DATABASE_URL", store.ScopeProject, "proj-1"))
	_, err = s.GetEnvVar(ctx, "DATABASE_URL", store.ScopeProject, "proj-1")
	assert.ErrorIs(t, err, store.ErrNotFound)
}

func TestPGEnvVarList(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	for i := 0; i < 3; i++ {
		require.NoError(t, s.CreateEnvVar(ctx, &store.EnvVar{
			ID:      api.NewUUID(),
			Key:     "VAR_" + string(rune('A'+i)),
			Value:   "val",
			Scope:   store.ScopeProject,
			ScopeID: "proj-evlist",
		}))
	}
	require.NoError(t, s.CreateEnvVar(ctx, &store.EnvVar{
		ID:      api.NewUUID(),
		Key:     "HUB_VAR",
		Value:   "hub",
		Scope:   store.ScopeHub,
		ScopeID: "hub-1",
	}))

	vars, err := s.ListEnvVars(ctx, store.EnvVarFilter{Scope: store.ScopeProject, ScopeID: "proj-evlist"})
	require.NoError(t, err)
	assert.Len(t, vars, 3)

	n, err := s.DeleteEnvVarsByScope(ctx, store.ScopeProject, "proj-evlist")
	require.NoError(t, err)
	assert.Equal(t, 3, n)

	vars, err = s.ListEnvVars(ctx, store.EnvVarFilter{Scope: store.ScopeProject, ScopeID: "proj-evlist"})
	require.NoError(t, err)
	assert.Empty(t, vars)
}

// ============================================================================
// Ping
// ============================================================================

func TestPGPing(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	require.NoError(t, s.Ping(ctx))
}
