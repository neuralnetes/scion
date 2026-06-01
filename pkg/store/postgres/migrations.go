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
)

// Migration V1: Initial schema
const migrationV1 = `
-- Projects table
CREATE TABLE IF NOT EXISTS groves (
	id TEXT PRIMARY KEY,
	name TEXT NOT NULL,
	slug TEXT NOT NULL,
	git_remote TEXT UNIQUE,
	labels TEXT,
	annotations TEXT,
	created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
	updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
	created_by TEXT,
	owner_id TEXT,
	visibility TEXT NOT NULL DEFAULT 'private'
);
CREATE INDEX IF NOT EXISTS idx_groves_slug ON groves(slug);
CREATE INDEX IF NOT EXISTS idx_groves_git_remote ON groves(git_remote);
CREATE INDEX IF NOT EXISTS idx_groves_owner ON groves(owner_id);

-- Runtime brokers table
CREATE TABLE IF NOT EXISTS runtime_brokers (
	id TEXT PRIMARY KEY,
	name TEXT NOT NULL,
	slug TEXT NOT NULL,
	type TEXT NOT NULL,
	mode TEXT NOT NULL DEFAULT 'connected',
	version TEXT,
	status TEXT NOT NULL DEFAULT 'offline',
	connection_state TEXT DEFAULT 'disconnected',
	last_heartbeat TIMESTAMP,
	capabilities TEXT,
	supported_harnesses TEXT,
	resources TEXT,
	runtimes TEXT,
	labels TEXT,
	annotations TEXT,
	endpoint TEXT,
	created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
	updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE INDEX IF NOT EXISTS idx_runtime_brokers_slug ON runtime_brokers(slug);
CREATE INDEX IF NOT EXISTS idx_runtime_brokers_status ON runtime_brokers(status);

-- Project contributors (many-to-many relationship)
CREATE TABLE IF NOT EXISTS grove_contributors (
	grove_id TEXT NOT NULL,
	broker_id TEXT NOT NULL,
	broker_name TEXT NOT NULL,
	mode TEXT NOT NULL DEFAULT 'connected',
	status TEXT NOT NULL DEFAULT 'offline',
	profiles TEXT,
	last_seen TIMESTAMP,
	PRIMARY KEY (grove_id, broker_id),
	FOREIGN KEY (grove_id) REFERENCES groves(id) ON DELETE CASCADE,
	FOREIGN KEY (broker_id) REFERENCES runtime_brokers(id) ON DELETE CASCADE
);

-- Agents table
CREATE TABLE IF NOT EXISTS agents (
	id TEXT PRIMARY KEY,
	agent_id TEXT NOT NULL,
	name TEXT NOT NULL,
	template TEXT NOT NULL,
	grove_id TEXT NOT NULL,
	labels TEXT,
	annotations TEXT,
	status TEXT NOT NULL DEFAULT 'pending',
	connection_state TEXT DEFAULT 'unknown',
	container_status TEXT,
	session_status TEXT,
	runtime_state TEXT,
	image TEXT,
	detached INTEGER NOT NULL DEFAULT 1,
	runtime TEXT,
	runtime_broker_id TEXT,
	web_pty_enabled INTEGER NOT NULL DEFAULT 0,
	task_summary TEXT,
	applied_config TEXT,
	created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
	updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
	last_seen TIMESTAMP,
	created_by TEXT,
	owner_id TEXT,
	visibility TEXT NOT NULL DEFAULT 'private',
	state_version INTEGER NOT NULL DEFAULT 1,
	FOREIGN KEY (grove_id) REFERENCES groves(id) ON DELETE CASCADE,
	FOREIGN KEY (runtime_broker_id) REFERENCES runtime_brokers(id) ON DELETE SET NULL
);
-- Use (agent_id, grove_id) order to match Ent schema's (slug, project_id)
CREATE UNIQUE INDEX IF NOT EXISTS idx_agents_grove_slug ON agents(agent_id, grove_id);
CREATE INDEX IF NOT EXISTS idx_agents_grove ON agents(grove_id);
CREATE INDEX IF NOT EXISTS idx_agents_status ON agents(status);
CREATE INDEX IF NOT EXISTS idx_agents_runtime_broker ON agents(runtime_broker_id);

-- Templates table
CREATE TABLE IF NOT EXISTS templates (
	id TEXT PRIMARY KEY,
	name TEXT NOT NULL,
	slug TEXT NOT NULL,
	harness TEXT NOT NULL,
	image TEXT,
	config TEXT,
	scope TEXT NOT NULL DEFAULT 'global',
	grove_id TEXT,
	storage_uri TEXT,
	owner_id TEXT,
	visibility TEXT NOT NULL DEFAULT 'private',
	created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
	updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
	FOREIGN KEY (grove_id) REFERENCES groves(id) ON DELETE CASCADE
);
CREATE INDEX IF NOT EXISTS idx_templates_slug_scope ON templates(slug, scope);
CREATE INDEX IF NOT EXISTS idx_templates_harness ON templates(harness);

-- Users table
CREATE TABLE IF NOT EXISTS users (
	id TEXT PRIMARY KEY,
	email TEXT UNIQUE NOT NULL,
	display_name TEXT NOT NULL,
	avatar_url TEXT,
	role TEXT NOT NULL DEFAULT 'member',
	status TEXT NOT NULL DEFAULT 'active',
	preferences TEXT,
	created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
	last_login TIMESTAMP
);
CREATE INDEX IF NOT EXISTS idx_users_email ON users(email);
`

// Migration V2: Add default_runtime_broker_id to groves
const migrationV2 = `
-- Add default runtime broker to groves
ALTER TABLE groves ADD COLUMN default_runtime_broker_id TEXT REFERENCES runtime_brokers(id) ON DELETE SET NULL;
CREATE INDEX IF NOT EXISTS idx_groves_default_runtime_broker ON groves(default_runtime_broker_id);
`

// Migration V3: Add local_path to grove_contributors
const migrationV3 = `
-- Add local_path column to grove_contributors for tracking filesystem paths per broker
ALTER TABLE grove_contributors ADD COLUMN local_path TEXT;
`

// Migration V4: Add environment variables and secrets tables
const migrationV4 = `
-- Environment variables table
CREATE TABLE IF NOT EXISTS env_vars (
	id TEXT PRIMARY KEY,
	key TEXT NOT NULL,
	value TEXT NOT NULL,
	scope TEXT NOT NULL,
	scope_id TEXT NOT NULL,
	description TEXT,
	sensitive INTEGER NOT NULL DEFAULT 0,
	created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
	updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
	created_by TEXT
);
CREATE UNIQUE INDEX IF NOT EXISTS idx_env_vars_key_scope ON env_vars(key, scope, scope_id);
CREATE INDEX IF NOT EXISTS idx_env_vars_scope ON env_vars(scope, scope_id);

-- Secrets table
CREATE TABLE IF NOT EXISTS secrets (
	id TEXT PRIMARY KEY,
	key TEXT NOT NULL,
	encrypted_value TEXT NOT NULL,
	scope TEXT NOT NULL,
	scope_id TEXT NOT NULL,
	description TEXT,
	version INTEGER NOT NULL DEFAULT 1,
	created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
	updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
	created_by TEXT,
	updated_by TEXT
);
CREATE UNIQUE INDEX IF NOT EXISTS idx_secrets_key_scope ON secrets(key, scope, scope_id);
CREATE INDEX IF NOT EXISTS idx_secrets_scope ON secrets(scope, scope_id);
`

// Migration V5: Groups and Policies (Hub Permissions System)
const migrationV5 = `
-- Groups table
CREATE TABLE IF NOT EXISTS groups (
	id TEXT PRIMARY KEY,
	name TEXT NOT NULL,
	slug TEXT UNIQUE NOT NULL,
	description TEXT,
	parent_id TEXT REFERENCES groups(id) ON DELETE SET NULL,
	labels TEXT,
	annotations TEXT,
	created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
	updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
	created_by TEXT,
	owner_id TEXT
);
CREATE INDEX IF NOT EXISTS idx_groups_slug ON groups(slug);
CREATE INDEX IF NOT EXISTS idx_groups_parent ON groups(parent_id);
CREATE INDEX IF NOT EXISTS idx_groups_owner ON groups(owner_id);

-- Group members table (users and nested groups)
CREATE TABLE IF NOT EXISTS group_members (
	group_id TEXT NOT NULL,
	member_type TEXT NOT NULL,  -- 'user' or 'group'
	member_id TEXT NOT NULL,
	role TEXT NOT NULL DEFAULT 'member',
	added_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
	added_by TEXT,
	PRIMARY KEY (group_id, member_type, member_id),
	FOREIGN KEY (group_id) REFERENCES groups(id) ON DELETE CASCADE
);
CREATE INDEX IF NOT EXISTS idx_group_members_member ON group_members(member_type, member_id);

-- Policies table
CREATE TABLE IF NOT EXISTS policies (
	id TEXT PRIMARY KEY,
	name TEXT NOT NULL,
	description TEXT,
	scope_type TEXT NOT NULL,
	scope_id TEXT,
	resource_type TEXT NOT NULL DEFAULT '*',
	resource_id TEXT,
	actions TEXT NOT NULL,  -- JSON array
	effect TEXT NOT NULL,
	conditions TEXT,        -- JSON object
	priority INTEGER NOT NULL DEFAULT 0,
	labels TEXT,
	annotations TEXT,
	created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
	updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
	created_by TEXT
);
CREATE INDEX IF NOT EXISTS idx_policies_scope ON policies(scope_type, scope_id);
CREATE INDEX IF NOT EXISTS idx_policies_effect ON policies(effect);
CREATE INDEX IF NOT EXISTS idx_policies_priority ON policies(priority DESC);

-- Policy bindings table
CREATE TABLE IF NOT EXISTS policy_bindings (
	policy_id TEXT NOT NULL,
	principal_type TEXT NOT NULL,  -- 'user' or 'group'
	principal_id TEXT NOT NULL,
	PRIMARY KEY (policy_id, principal_type, principal_id),
	FOREIGN KEY (policy_id) REFERENCES policies(id) ON DELETE CASCADE
);
CREATE INDEX IF NOT EXISTS idx_policy_bindings_principal ON policy_bindings(principal_type, principal_id);
`

// Migration V6: Extend templates table for hosted template management
const migrationV6 = `
-- Add new columns to templates table
ALTER TABLE templates ADD COLUMN display_name TEXT;
ALTER TABLE templates ADD COLUMN description TEXT;
ALTER TABLE templates ADD COLUMN content_hash TEXT;
ALTER TABLE templates ADD COLUMN scope_id TEXT;
ALTER TABLE templates ADD COLUMN storage_bucket TEXT;
ALTER TABLE templates ADD COLUMN storage_path TEXT;
ALTER TABLE templates ADD COLUMN files TEXT;
ALTER TABLE templates ADD COLUMN base_template TEXT;
ALTER TABLE templates ADD COLUMN locked INTEGER NOT NULL DEFAULT 0;
ALTER TABLE templates ADD COLUMN status TEXT NOT NULL DEFAULT 'active';
ALTER TABLE templates ADD COLUMN created_by TEXT;
ALTER TABLE templates ADD COLUMN updated_by TEXT;

-- Add indexes for new columns
CREATE INDEX IF NOT EXISTS idx_templates_status ON templates(status);
CREATE INDEX IF NOT EXISTS idx_templates_content_hash ON templates(content_hash);
CREATE INDEX IF NOT EXISTS idx_templates_scope_id ON templates(scope, scope_id);
`

const migrationV7 = `
-- Add API keys table
CREATE TABLE IF NOT EXISTS api_keys (
    id TEXT PRIMARY KEY,
    user_id TEXT NOT NULL,
    name TEXT NOT NULL,
    prefix TEXT NOT NULL,
    key_hash TEXT NOT NULL UNIQUE,
    scopes TEXT,
    revoked INTEGER NOT NULL DEFAULT 0,
    expires_at TIMESTAMP,
    last_used TIMESTAMP,
    created_at TIMESTAMP NOT NULL,
    FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE CASCADE
);

-- Add indexes for API keys
CREATE INDEX IF NOT EXISTS idx_api_keys_user_id ON api_keys(user_id);
CREATE INDEX IF NOT EXISTS idx_api_keys_key_hash ON api_keys(key_hash);
CREATE INDEX IF NOT EXISTS idx_api_keys_prefix ON api_keys(prefix);
`

const migrationV8 = `
-- Add message column to agents table
ALTER TABLE agents ADD COLUMN message TEXT;
`

// Migration V9: Broker secrets and join tokens for Runtime Broker authentication
const migrationV9 = `
-- Broker secrets table for HMAC-based authentication
CREATE TABLE IF NOT EXISTS broker_secrets (
    broker_id TEXT PRIMARY KEY,
    secret_key BYTEA NOT NULL,
    algorithm TEXT NOT NULL DEFAULT 'hmac-sha256',
    created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    rotated_at TIMESTAMP,
    expires_at TIMESTAMP,
    status TEXT NOT NULL DEFAULT 'active',
    FOREIGN KEY (broker_id) REFERENCES runtime_brokers(id) ON DELETE CASCADE
);

-- Broker join tokens table for registration bootstrap
CREATE TABLE IF NOT EXISTS broker_join_tokens (
    broker_id TEXT PRIMARY KEY,
    token_hash TEXT NOT NULL UNIQUE,
    expires_at TIMESTAMP NOT NULL,
    created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    created_by TEXT NOT NULL,
    FOREIGN KEY (broker_id) REFERENCES runtime_brokers(id) ON DELETE CASCADE
);
CREATE INDEX IF NOT EXISTS idx_broker_join_tokens_hash ON broker_join_tokens(token_hash);
CREATE INDEX IF NOT EXISTS idx_broker_join_tokens_expires ON broker_join_tokens(expires_at);
`

// Migration V10: Add user tracking to grove_contributors and runtime_brokers
const migrationV10 = `
-- Add linked_by and linked_at columns to grove_contributors for tracking who linked a broker
ALTER TABLE grove_contributors ADD COLUMN linked_by TEXT;
ALTER TABLE grove_contributors ADD COLUMN linked_at TIMESTAMP;

-- Add created_by column to runtime_brokers for tracking who registered the broker
ALTER TABLE runtime_brokers ADD COLUMN created_by TEXT;
`

// Migration V11: Add auto_provide column to runtime_brokers
const migrationV11 = `
-- Add auto_provide column to runtime_brokers for automatic project provider registration
ALTER TABLE runtime_brokers ADD COLUMN auto_provide INTEGER NOT NULL DEFAULT 0;
`

// Migration V12: Add injection_mode and secret columns to env_vars
const migrationV12 = `
ALTER TABLE env_vars ADD COLUMN injection_mode TEXT NOT NULL DEFAULT 'as_needed';
ALTER TABLE env_vars ADD COLUMN secret INTEGER NOT NULL DEFAULT 0;
`

const migrationV13 = `
ALTER TABLE secrets ADD COLUMN secret_type TEXT NOT NULL DEFAULT 'environment';
ALTER TABLE secrets ADD COLUMN target TEXT;
`

const migrationV14 = `
ALTER TABLE secrets ADD COLUMN secret_ref TEXT;
`

const migrationV15 = `
UPDATE agents SET status = session_status WHERE session_status IS NOT NULL AND session_status != '';
ALTER TABLE agents DROP COLUMN session_status;
`

// Migration V16: Add harness_configs table
const migrationV16 = `
CREATE TABLE IF NOT EXISTS harness_configs (
	id TEXT PRIMARY KEY,
	name TEXT NOT NULL,
	slug TEXT NOT NULL,
	display_name TEXT,
	description TEXT,
	harness TEXT NOT NULL,
	config TEXT,
	content_hash TEXT,
	scope TEXT NOT NULL DEFAULT 'global',
	scope_id TEXT,
	storage_uri TEXT,
	storage_bucket TEXT,
	storage_path TEXT,
	files TEXT,
	locked INTEGER NOT NULL DEFAULT 0,
	status TEXT NOT NULL DEFAULT 'active',
	owner_id TEXT,
	created_by TEXT,
	updated_by TEXT,
	visibility TEXT NOT NULL DEFAULT 'private',
	created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
	updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE INDEX IF NOT EXISTS idx_harness_configs_slug_scope ON harness_configs(slug, scope);
CREATE INDEX IF NOT EXISTS idx_harness_configs_harness ON harness_configs(harness);
CREATE INDEX IF NOT EXISTS idx_harness_configs_status ON harness_configs(status);
CREATE INDEX IF NOT EXISTS idx_harness_configs_content_hash ON harness_configs(content_hash);
CREATE INDEX IF NOT EXISTS idx_harness_configs_scope_id ON harness_configs(scope, scope_id);
`

// Migration V17: Add deleted_at column to agents for soft-delete support
const migrationV17 = `
ALTER TABLE agents ADD COLUMN deleted_at TIMESTAMP;
CREATE INDEX IF NOT EXISTS idx_agents_deleted ON agents(status, deleted_at) WHERE status = 'deleted';
`

// Migration V18: Notification subscriptions and notifications tables
const migrationV18 = `
CREATE TABLE IF NOT EXISTS notification_subscriptions (
	id TEXT PRIMARY KEY,
	agent_id TEXT NOT NULL,
	subscriber_type TEXT NOT NULL DEFAULT 'agent',
	subscriber_id TEXT NOT NULL,
	grove_id TEXT NOT NULL,
	trigger_statuses TEXT NOT NULL,
	created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
	created_by TEXT NOT NULL,
	FOREIGN KEY (agent_id) REFERENCES agents(id) ON DELETE CASCADE
);
CREATE INDEX IF NOT EXISTS idx_notification_subs_agent ON notification_subscriptions(agent_id);
CREATE INDEX IF NOT EXISTS idx_notification_subs_project ON notification_subscriptions(grove_id);

CREATE TABLE IF NOT EXISTS notifications (
	id TEXT PRIMARY KEY,
	subscription_id TEXT NOT NULL,
	agent_id TEXT NOT NULL,
	grove_id TEXT NOT NULL,
	subscriber_type TEXT NOT NULL,
	subscriber_id TEXT NOT NULL,
	status TEXT NOT NULL,
	message TEXT NOT NULL,
	dispatched INTEGER NOT NULL DEFAULT 0,
	acknowledged INTEGER NOT NULL DEFAULT 0,
	created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
	FOREIGN KEY (subscription_id) REFERENCES notification_subscriptions(id) ON DELETE CASCADE
);
CREATE INDEX IF NOT EXISTS idx_notifications_subscriber ON notifications(subscriber_type, subscriber_id);
CREATE INDEX IF NOT EXISTS idx_notifications_project ON notifications(grove_id);
`

const migrationV19 = `
CREATE TABLE IF NOT EXISTS scheduled_events (
	id TEXT PRIMARY KEY,
	grove_id TEXT NOT NULL,
	event_type TEXT NOT NULL,
	fire_at TIMESTAMP NOT NULL,
	payload TEXT NOT NULL,
	status TEXT NOT NULL DEFAULT 'pending',
	created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
	created_by TEXT,
	fired_at TIMESTAMP,
	error TEXT,

	FOREIGN KEY (grove_id) REFERENCES groves(id) ON DELETE CASCADE
);
CREATE INDEX IF NOT EXISTS idx_scheduled_events_status ON scheduled_events(status);
CREATE INDEX IF NOT EXISTS idx_scheduled_events_fire_at ON scheduled_events(fire_at) WHERE status = 'pending';
CREATE INDEX IF NOT EXISTS idx_scheduled_events_project ON scheduled_events(grove_id);
`

const migrationV20 = `
ALTER TABLE agents ADD COLUMN phase TEXT NOT NULL DEFAULT 'created';
ALTER TABLE agents ADD COLUMN activity TEXT DEFAULT '';
ALTER TABLE agents ADD COLUMN tool_name TEXT DEFAULT '';

-- Backfill phase/activity from existing status values
UPDATE agents SET phase = 'created' WHERE status IN ('created', 'pending');
UPDATE agents SET phase = 'provisioning' WHERE status = 'provisioning';
UPDATE agents SET phase = 'cloning' WHERE status = 'cloning';
UPDATE agents SET phase = 'running', activity = 'idle' WHERE status = 'running';
UPDATE agents SET phase = 'stopped' WHERE status = 'stopped';
UPDATE agents SET phase = 'error' WHERE status = 'error';
UPDATE agents SET phase = 'running', activity = 'thinking' WHERE status = 'busy';
UPDATE agents SET phase = 'running', activity = 'idle' WHERE status = 'idle';
UPDATE agents SET phase = 'running', activity = 'waiting_for_input' WHERE status = 'waiting_for_input';
UPDATE agents SET phase = 'running', activity = 'completed' WHERE status = 'completed';
UPDATE agents SET phase = 'running', activity = 'limits_exceeded' WHERE status = 'limits_exceeded';
UPDATE agents SET phase = 'stopped' WHERE status IN ('deleted', 'restored');
UPDATE agents SET phase = 'running', activity = 'offline' WHERE status = 'undetermined';

CREATE INDEX IF NOT EXISTS idx_agents_phase ON agents(phase);
`

// Migration V21: Remove legacy status column from agents table.
// Phase 6 of the agent state refactor — the status column is superseded by
// the phase/activity columns added in V20.
const migrationV21 = `
-- Backfill any remaining agents where phase was not set
UPDATE agents SET phase = status WHERE (phase = '' OR phase IS NULL) AND status IN ('created','provisioning','cloning','starting','running','stopping','stopped','error');
UPDATE agents SET phase = 'created' WHERE (phase = '' OR phase IS NULL) AND status = 'pending';
UPDATE agents SET phase = 'stopped' WHERE (phase = '' OR phase IS NULL) AND status = 'deleted';

-- Backfill activity from status for running agents
UPDATE agents SET activity = status WHERE phase = 'running' AND (activity = '' OR activity IS NULL) AND status IN ('idle','waiting_for_input','completed','limits_exceeded','offline');
UPDATE agents SET activity = 'thinking' WHERE phase = 'running' AND (activity = '' OR activity IS NULL) AND status = 'busy';

-- Update soft-delete index: rely on deleted_at instead of status
DROP INDEX IF EXISTS idx_agents_deleted;
CREATE INDEX IF NOT EXISTS idx_agents_deleted ON agents(deleted_at) WHERE deleted_at IS NOT NULL;

-- Drop the status index before dropping the column
DROP INDEX IF EXISTS idx_agents_status;

-- Drop the status column (SQLite supports this from 3.35.0+)
ALTER TABLE agents DROP COLUMN status;
`

// Migration V22: Rename trigger_statuses to trigger_activities in notification_subscriptions.
const migrationV22 = `
ALTER TABLE notification_subscriptions RENAME COLUMN trigger_statuses TO trigger_activities;
`

// Migration V23: Add injection_mode column to secrets
const migrationV23 = `
ALTER TABLE secrets ADD COLUMN injection_mode TEXT NOT NULL DEFAULT 'as_needed';
`

// Migration V24: Add last_activity_event column to agents for stalled detection.
// Backfills existing agents to prevent false positives on upgrade.
const migrationV24 = `
ALTER TABLE agents ADD COLUMN last_activity_event TIMESTAMP;
UPDATE agents SET last_activity_event = COALESCE(last_seen, updated_at, created_at);
`

// Migration V25: Add stalled_from_activity column for stalled detection.
// Records the activity that was active when the agent was marked stalled,
// so heartbeats can distinguish "still stuck" from "genuinely recovered".
const migrationV25 = `
ALTER TABLE agents ADD COLUMN stalled_from_activity TEXT DEFAULT '';
`

// Migration V26: Add limits tracking columns to agents table.
// These fields are updated by sciontool status reports from inside the container.
const migrationV26 = `
ALTER TABLE agents ADD COLUMN current_turns INTEGER DEFAULT 0;
ALTER TABLE agents ADD COLUMN current_model_calls INTEGER DEFAULT 0;
ALTER TABLE agents ADD COLUMN started_at TIMESTAMP;
`

const migrationV27 = `
ALTER TABLE users ADD COLUMN last_seen TIMESTAMP;
`

// Migration V28: Add shared_dirs column to groves table.
// Stores project-level shared directory configuration as JSON.
const migrationV28 = `
ALTER TABLE groves ADD COLUMN shared_dirs TEXT DEFAULT '';
`

// Migration V29: Add group_type and grove_id columns to groups table.
// These enable filtering groups by type and project association.
const migrationV29 = `
ALTER TABLE groups ADD COLUMN group_type TEXT NOT NULL DEFAULT 'explicit';
ALTER TABLE groups ADD COLUMN grove_id TEXT DEFAULT '';
CREATE INDEX IF NOT EXISTS idx_groups_project ON groups(grove_id);
`

// Migration V30: Create gcp_service_accounts table for GCP identity management.
const migrationV30 = `
CREATE TABLE IF NOT EXISTS gcp_service_accounts (
	id TEXT PRIMARY KEY,
	scope TEXT NOT NULL,
	scope_id TEXT NOT NULL,
	email TEXT NOT NULL,
	grove_id TEXT NOT NULL,
	display_name TEXT NOT NULL DEFAULT '',
	default_scopes TEXT NOT NULL DEFAULT '',
	verified INTEGER NOT NULL DEFAULT 0,
	verified_at TIMESTAMP,
	created_by TEXT NOT NULL DEFAULT '',
	created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
	UNIQUE(email, scope, scope_id)
);
CREATE INDEX IF NOT EXISTS idx_gcp_sa_scope ON gcp_service_accounts(scope, scope_id);
`

// Migration V31: Add scope column to notification_subscriptions and make agent_id nullable.
// Enables project-scoped subscriptions (watch all agents in a project) in addition to
// agent-scoped subscriptions. Adds unique constraint for deduplication.
const migrationV31 = `
-- Postgres supports ALTER TABLE directly, so we recreate the table as in SQLite source.
CREATE TABLE notification_subscriptions_new (
	id TEXT PRIMARY KEY,
	scope TEXT NOT NULL DEFAULT 'agent',
	agent_id TEXT,
	subscriber_type TEXT NOT NULL DEFAULT 'agent',
	subscriber_id TEXT NOT NULL,
	grove_id TEXT NOT NULL,
	trigger_activities TEXT NOT NULL,
	created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
	created_by TEXT NOT NULL,
	FOREIGN KEY (agent_id) REFERENCES agents(id) ON DELETE CASCADE
);

-- Copy existing data (all existing subscriptions are agent-scoped)
INSERT INTO notification_subscriptions_new
	(id, scope, agent_id, subscriber_type, subscriber_id, grove_id, trigger_activities, created_at, created_by)
SELECT id, 'agent', agent_id, subscriber_type, subscriber_id, grove_id, trigger_activities, created_at, created_by
FROM notification_subscriptions;

DROP TABLE notification_subscriptions CASCADE;
ALTER TABLE notification_subscriptions_new RENAME TO notification_subscriptions;

-- Recreate indexes
CREATE INDEX IF NOT EXISTS idx_notification_subs_agent ON notification_subscriptions(agent_id);
CREATE INDEX IF NOT EXISTS idx_notification_subs_project ON notification_subscriptions(grove_id);
CREATE INDEX IF NOT EXISTS idx_notification_subs_subscriber ON notification_subscriptions(subscriber_type, subscriber_id);

-- Unique constraint: one subscription per (scope, target, subscriber, project)
CREATE UNIQUE INDEX IF NOT EXISTS idx_notification_subs_unique
	ON notification_subscriptions(scope, COALESCE(agent_id, ''), subscriber_type, subscriber_id, grove_id);
`

// Migration V32: Recurring schedules table and schedule_id FK on scheduled_events.
const migrationV32 = `
CREATE TABLE IF NOT EXISTS schedules (
	id TEXT PRIMARY KEY,
	grove_id TEXT NOT NULL,
	name TEXT NOT NULL,
	cron_expr TEXT NOT NULL,
	event_type TEXT NOT NULL,
	payload TEXT NOT NULL DEFAULT '{}',
	status TEXT NOT NULL DEFAULT 'active',
	next_run_at TIMESTAMP,
	last_run_at TIMESTAMP,
	last_run_status TEXT,
	last_run_error TEXT,
	run_count INTEGER NOT NULL DEFAULT 0,
	error_count INTEGER NOT NULL DEFAULT 0,
	created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
	created_by TEXT,
	updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
	FOREIGN KEY (grove_id) REFERENCES groves(id) ON DELETE CASCADE,
	UNIQUE(grove_id, name)
);
CREATE INDEX IF NOT EXISTS idx_schedules_project ON schedules(grove_id);
CREATE INDEX IF NOT EXISTS idx_schedules_next_run ON schedules(next_run_at) WHERE status = 'active';

ALTER TABLE scheduled_events ADD COLUMN schedule_id TEXT DEFAULT '';
`

// Migration V33: Subscription templates table.
const migrationV33 = `
CREATE TABLE IF NOT EXISTS subscription_templates (
	id TEXT PRIMARY KEY,
	name TEXT NOT NULL,
	scope TEXT NOT NULL DEFAULT 'project',
	trigger_activities TEXT NOT NULL,
	grove_id TEXT NOT NULL DEFAULT '',
	created_by TEXT NOT NULL,
	UNIQUE(grove_id, name)
);
CREATE INDEX IF NOT EXISTS idx_sub_templates_project ON subscription_templates(grove_id);
`

// Migration V34: User access tokens table (replaces api_keys).
const migrationV34 = `
CREATE TABLE IF NOT EXISTS user_access_tokens (
	id TEXT PRIMARY KEY,
	user_id TEXT NOT NULL,
	name TEXT NOT NULL,
	prefix TEXT NOT NULL,
	key_hash TEXT NOT NULL UNIQUE,
	grove_id TEXT NOT NULL,
	scopes TEXT NOT NULL,
	revoked INTEGER NOT NULL DEFAULT 0,
	expires_at TIMESTAMP,
	last_used TIMESTAMP,
	created_at TIMESTAMP NOT NULL,
	FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE CASCADE,
	FOREIGN KEY (grove_id) REFERENCES groves(id) ON DELETE CASCADE
);
CREATE INDEX IF NOT EXISTS idx_uat_user_id ON user_access_tokens(user_id);
CREATE INDEX IF NOT EXISTS idx_uat_key_hash ON user_access_tokens(key_hash);
`

// Migration V35: GitHub App installations and project GitHub App fields.
const migrationV35 = `
CREATE TABLE IF NOT EXISTS github_installations (
	installation_id BIGINT PRIMARY KEY,
	account_login TEXT NOT NULL,
	account_type TEXT NOT NULL DEFAULT 'Organization',
	app_id INTEGER NOT NULL,
	repositories TEXT NOT NULL DEFAULT '[]',
	status TEXT NOT NULL DEFAULT 'active',
	created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
	updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE INDEX IF NOT EXISTS idx_github_installations_account ON github_installations(account_login);
CREATE INDEX IF NOT EXISTS idx_github_installations_status ON github_installations(status);

ALTER TABLE groves ADD COLUMN github_installation_id INTEGER;
ALTER TABLE groves ADD COLUMN github_permissions TEXT;
ALTER TABLE groves ADD COLUMN github_app_status TEXT;
`

// Migration V36: Git identity configuration for commit attribution.
const migrationV36 = `
ALTER TABLE groves ADD COLUMN git_identity TEXT;
`

// Migration V37: Add ancestry column for transitive access control.
const migrationV37 = `
ALTER TABLE agents ADD COLUMN ancestry TEXT;
`

// Migration V38: Backfill ancestry for existing agents from created_by.
const migrationV38 = `
UPDATE agents SET ancestry = json_build_array(created_by)::text
WHERE created_by IS NOT NULL AND created_by != '' AND ancestry IS NULL;
`

// Migration V39: Messages table for bidirectional human-agent messaging.
const migrationV39 = `
CREATE TABLE IF NOT EXISTS messages (
	id TEXT PRIMARY KEY,
	grove_id TEXT NOT NULL,
	sender TEXT NOT NULL,
	sender_id TEXT NOT NULL DEFAULT '',
	recipient TEXT NOT NULL,
	recipient_id TEXT NOT NULL DEFAULT '',
	msg TEXT NOT NULL,
	type TEXT NOT NULL DEFAULT 'instruction',
	urgent INTEGER NOT NULL DEFAULT 0,
	broadcasted INTEGER NOT NULL DEFAULT 0,
	read INTEGER NOT NULL DEFAULT 0,
	agent_id TEXT NOT NULL DEFAULT '',
	created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX IF NOT EXISTS idx_messages_project ON messages(grove_id);
CREATE INDEX IF NOT EXISTS idx_messages_recipient ON messages(recipient_id, read);
CREATE INDEX IF NOT EXISTS idx_messages_agent ON messages(agent_id);
CREATE INDEX IF NOT EXISTS idx_messages_sender ON messages(sender_id);
CREATE INDEX IF NOT EXISTS idx_messages_created ON messages(created_at DESC);
`

// Migration V40: Allow multiple groves per git remote (drop UNIQUE on git_remote),
// and enforce slug uniqueness (add UNIQUE on slug). Requires table recreation
// because SQLite does not support ALTER TABLE DROP CONSTRAINT.
//
// IMPORTANT: This migration requires foreign_keys=OFF around the DROP TABLE.
// SQLite ignores PRAGMA changes inside transactions, so the migration runner
// handles this via the foreignKeysOffMigrations set. The PRAGMA statements are
// intentionally NOT included in the SQL string.
const migrationV40 = `
CREATE TABLE IF NOT EXISTS groves_new (
	id TEXT PRIMARY KEY,
	name TEXT NOT NULL,
	slug TEXT NOT NULL UNIQUE,
	git_remote TEXT,
	labels TEXT,
	annotations TEXT,
	created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
	updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
	created_by TEXT,
	owner_id TEXT,
	visibility TEXT NOT NULL DEFAULT 'private',
	default_runtime_broker_id TEXT REFERENCES runtime_brokers(id) ON DELETE SET NULL,
	shared_dirs TEXT,
	github_installation_id INTEGER REFERENCES github_installations(installation_id),
	github_permissions TEXT,
	github_app_status TEXT,
	git_identity TEXT
);

INSERT INTO groves_new SELECT
	id, name, slug, git_remote, labels, annotations,
	created_at, updated_at, created_by, owner_id, visibility,
	default_runtime_broker_id, shared_dirs,
	github_installation_id, github_permissions, github_app_status,
	git_identity
FROM groves ON CONFLICT DO NOTHING;

DROP TABLE IF EXISTS groves CASCADE;
ALTER TABLE groves_new RENAME TO groves;

CREATE INDEX IF NOT EXISTS idx_groves_slug ON groves(slug);
CREATE INDEX IF NOT EXISTS idx_groves_git_remote ON groves(git_remote);
CREATE INDEX IF NOT EXISTS idx_groves_owner ON groves(owner_id);
CREATE INDEX IF NOT EXISTS idx_groves_default_runtime_broker ON groves(default_runtime_broker_id);
`

// Migration V41: Maintenance operations tables for the admin maintenance panel.
// Tracks one-time migrations and repeatable operations with execution history.
const migrationV41 = `
CREATE TABLE IF NOT EXISTS maintenance_operations (
    id          TEXT PRIMARY KEY,
    key         TEXT NOT NULL UNIQUE,
    title       TEXT NOT NULL,
    description TEXT NOT NULL DEFAULT '',
    category    TEXT NOT NULL,
    status      TEXT NOT NULL DEFAULT 'pending',
    created_at  TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    started_at  TIMESTAMP,
    completed_at TIMESTAMP,
    started_by  TEXT,
    result      TEXT,
    metadata    TEXT NOT NULL DEFAULT '{}'
);
CREATE INDEX IF NOT EXISTS idx_maintenance_ops_category ON maintenance_operations(category);
CREATE INDEX IF NOT EXISTS idx_maintenance_ops_status ON maintenance_operations(status);

CREATE TABLE IF NOT EXISTS maintenance_operation_runs (
    id            TEXT PRIMARY KEY,
    operation_key TEXT NOT NULL,
    status        TEXT NOT NULL DEFAULT 'running',
    started_at    TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    completed_at  TIMESTAMP,
    started_by    TEXT,
    result        TEXT,
    log           TEXT NOT NULL DEFAULT '',
    FOREIGN KEY (operation_key) REFERENCES maintenance_operations(key)
);
CREATE INDEX IF NOT EXISTS idx_maintenance_runs_key ON maintenance_operation_runs(operation_key);
CREATE INDEX IF NOT EXISTS idx_maintenance_runs_started ON maintenance_operation_runs(started_at DESC);

-- Seed: one-time migrations
INSERT INTO maintenance_operations (id, key, title, description, category, status)
VALUES (
    gen_random_uuid()::text,
    'secret-hub-id-migration',
    'Secret Hub ID Namespace Migration',
    'Migrates hub-scoped secrets from the legacy fixed "hub" scope ID to the per-instance hub ID. Required when upgrading a hub that was created before the hub ID namespacing feature. Only needed for GCP Secret Manager backend.',
    'migration',
    'pending'
);

-- Seed: repeatable operations
INSERT INTO maintenance_operations (id, key, title, description, category, status)
VALUES (
    gen_random_uuid()::text,
    'pull-images',
    'Pull Container Images',
    'Pulls the latest container images for all configured harnesses from the image registry.',
    'operation',
    'pending'
);

INSERT INTO maintenance_operations (id, key, title, description, category, status)
VALUES (
    gen_random_uuid()::text,
    'rebuild-server',
    'Rebuild Server from Git',
    'Pulls latest code from the repository, rebuilds the server binary and web assets, then restarts the hub service. Equivalent to the fast-deploy mode of gce-start-hub.sh.',
    'operation',
    'pending'
);

INSERT INTO maintenance_operations (id, key, title, description, category, status)
VALUES (
    gen_random_uuid()::text,
    'rebuild-web',
    'Rebuild Web Frontend',
    'Rebuilds only the web frontend assets from source without restarting the server binary. Changes take effect on the next page load.',
    'operation',
    'pending'
);
`

const migrationV42 = `
CREATE TABLE IF NOT EXISTS grove_sync_state (
	grove_id TEXT NOT NULL,
	broker_id TEXT NOT NULL DEFAULT '',
	last_sync_time TIMESTAMP,
	last_commit_sha TEXT,
	file_count INTEGER NOT NULL DEFAULT 0,
	total_bytes INTEGER NOT NULL DEFAULT 0,
	PRIMARY KEY (grove_id, broker_id),
	FOREIGN KEY (grove_id) REFERENCES groves(id) ON DELETE CASCADE
);
CREATE INDEX IF NOT EXISTS idx_grove_sync_state_project ON grove_sync_state(grove_id);
`

// migrationV43 fixes pre-existing signing key secrets that were stored with
// the default secret_type ('environment' or ") instead of 'internal'. Without
// this, stale rows created before the fix would still be resolved and injected
// into agent containers.
const migrationV43 = `
UPDATE secrets SET secret_type = 'internal'
WHERE key IN ('agent_signing_key', 'user_signing_key')
  AND scope = 'hub'
  AND secret_type != 'internal';
`

// Migration V44: Add managed and managed_by columns to gcp_service_accounts table
// for hub-minted service accounts.
const migrationV44 = `
ALTER TABLE gcp_service_accounts ADD COLUMN managed INTEGER NOT NULL DEFAULT 0;
ALTER TABLE gcp_service_accounts ADD COLUMN managed_by TEXT NOT NULL DEFAULT '';
`

// Migration V45: Add allow_progeny column to secrets table
const migrationV45 = `
ALTER TABLE secrets ADD COLUMN allow_progeny INTEGER NOT NULL DEFAULT 0;
`

const migrationV46 = `
ALTER TABLE templates ADD COLUMN default_harness_config TEXT;
`

const migrationV47 = `
INSERT INTO maintenance_operations (id, key, title, description, category, status)
VALUES (
    gen_random_uuid()::text,
    'rebuild-container-binaries',
    'Rebuild Container Binaries',
    'Rebuilds scion and sciontool binaries for Linux containers (make container-binaries). Only available when SCION_DEV_BINARIES is set. Binaries are written to .build/container/ in the source checkout.',
    'operation',
    'pending'
);
`

const migrationV48 = `
CREATE TABLE IF NOT EXISTS allow_list (
    id TEXT PRIMARY KEY,
    email TEXT NOT NULL,
    note TEXT NOT NULL DEFAULT '',
    added_by TEXT NOT NULL,
    invite_id TEXT NOT NULL DEFAULT '',
    created TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE UNIQUE INDEX IF NOT EXISTS allow_list_email_unique ON allow_list (LOWER(email));
`

const migrationV49 = `
CREATE TABLE IF NOT EXISTS invite_codes (
    id TEXT PRIMARY KEY,
    code_hash TEXT NOT NULL UNIQUE,
    code_prefix TEXT NOT NULL,
    max_uses INTEGER NOT NULL DEFAULT 1,
    use_count INTEGER NOT NULL DEFAULT 0,
    expires_at TIMESTAMP NOT NULL,
    revoked INTEGER NOT NULL DEFAULT 0,
    created_by TEXT NOT NULL,
    note TEXT NOT NULL DEFAULT '',
    created TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE INDEX IF NOT EXISTS idx_invite_codes_expires ON invite_codes(expires_at);
`

// migrateV50 renames 'grove' entities to 'project' idempotently.
// This is Phase 4 of the grove-to-project rename strategy.
// Each rename operation checks whether the old name still exists before
// attempting the rename, so the migration can be re-run safely on databases
// that partially applied an earlier (non-idempotent) version of V50.
func migrateV50(ctx context.Context, tx *sql.Tx) error {
	// 1. Rename Tables (check before renaming)
	tableRenames := [][2]string{
		{"groves", "projects"},
		{"grove_contributors", "project_contributors"},
		{"grove_sync_state", "project_sync_state"},
	}
	for _, r := range tableRenames {
		exists, err := tableExists(ctx, tx, r[0])
		if err != nil {
			return fmt.Errorf("checking table %s: %w", r[0], err)
		}
		if exists {
			if _, err := tx.ExecContext(ctx, fmt.Sprintf("ALTER TABLE %s RENAME TO %s", r[0], r[1])); err != nil {
				return fmt.Errorf("renaming table %s to %s: %w", r[0], r[1], err)
			}
		}
	}

	// 2. Rename Columns (check before renaming)
	// After step 1, tables are at their new names. If step 1 was already
	// applied in a prior run, the tables are also at their new names.
	columnRenames := [][3]string{
		{"project_contributors", "grove_id", "project_id"},
		{"project_sync_state", "grove_id", "project_id"},
		{"agents", "grove_id", "project_id"},
		{"templates", "grove_id", "project_id"},
		{"notification_subscriptions", "grove_id", "project_id"},
		{"notifications", "grove_id", "project_id"},
		{"scheduled_events", "grove_id", "project_id"},
		{"schedules", "grove_id", "project_id"},
		{"subscription_templates", "grove_id", "project_id"},
		{"user_access_tokens", "grove_id", "project_id"},
		{"messages", "grove_id", "project_id"},
		{"groups", "grove_id", "project_id"},
		{"gcp_service_accounts", "grove_id", "project_id"},
	}
	for _, r := range columnRenames {
		exists, err := columnExists(ctx, tx, r[0], r[1])
		if err != nil {
			return fmt.Errorf("checking column %s.%s: %w", r[0], r[1], err)
		}
		if exists {
			if _, err := tx.ExecContext(ctx, fmt.Sprintf("ALTER TABLE %s RENAME COLUMN %s TO %s", r[0], r[1], r[2])); err != nil {
				return fmt.Errorf("renaming column %s.%s to %s: %w", r[0], r[1], r[2], err)
			}
		}
	}

	// 3. Update Data Values (already idempotent — UPDATE WHERE is a no-op
	// when the old value no longer exists)
	dataUpdates := `
UPDATE env_vars SET scope = 'project' WHERE scope = 'grove';
UPDATE secrets SET scope = 'project' WHERE scope = 'grove';
UPDATE policies SET scope_type = 'project' WHERE scope_type = 'grove';
UPDATE gcp_service_accounts SET scope = 'project' WHERE scope = 'grove';
UPDATE groups SET group_type = 'project_agents' WHERE group_type = 'grove_agents';
UPDATE notification_subscriptions SET scope = 'project' WHERE scope = 'grove';
UPDATE subscription_templates SET scope = 'project' WHERE scope = 'grove';
UPDATE templates SET scope = 'project' WHERE scope = 'grove';
UPDATE harness_configs SET scope = 'project' WHERE scope = 'grove';
`
	if _, err := tx.ExecContext(ctx, dataUpdates); err != nil {
		return fmt.Errorf("updating data values: %w", err)
	}

	// 4. Rename/Recreate Indexes (already idempotent — DROP IF EXISTS / CREATE IF NOT EXISTS)
	indexSQL := `
DROP INDEX IF EXISTS idx_groves_slug;
CREATE UNIQUE INDEX IF NOT EXISTS idx_projects_slug ON projects(slug);
DROP INDEX IF EXISTS idx_groves_git_remote;
CREATE INDEX IF NOT EXISTS idx_projects_git_remote ON projects(git_remote);
DROP INDEX IF EXISTS idx_groves_owner;
CREATE INDEX IF NOT EXISTS idx_projects_owner ON projects(owner_id);
DROP INDEX IF EXISTS idx_groves_default_runtime_broker;
CREATE INDEX IF NOT EXISTS idx_projects_default_runtime_broker ON projects(default_runtime_broker_id);

DROP INDEX IF EXISTS idx_agents_grove_slug;
DROP INDEX IF EXISTS idx_agents_project_slug;
CREATE UNIQUE INDEX IF NOT EXISTS idx_agents_project_slug ON agents(agent_id, project_id);
DROP INDEX IF EXISTS idx_agents_grove;
CREATE INDEX IF NOT EXISTS idx_agents_project ON agents(project_id);

DROP INDEX IF EXISTS idx_grove_sync_state_grove;
CREATE INDEX IF NOT EXISTS idx_project_sync_state_project ON project_sync_state(project_id);

DROP INDEX IF EXISTS idx_notification_subs_grove;
CREATE INDEX IF NOT EXISTS idx_notification_subs_project ON notification_subscriptions(project_id);

DROP INDEX IF EXISTS idx_notifications_grove;
CREATE INDEX IF NOT EXISTS idx_notifications_project ON notifications(project_id);

DROP INDEX IF EXISTS idx_scheduled_events_grove;
CREATE INDEX IF NOT EXISTS idx_scheduled_events_project ON scheduled_events(project_id);

DROP INDEX IF EXISTS idx_schedules_grove;
CREATE INDEX IF NOT EXISTS idx_schedules_project ON schedules(project_id);

DROP INDEX IF EXISTS idx_sub_templates_grove;
CREATE INDEX IF NOT EXISTS idx_sub_templates_project ON subscription_templates(project_id);

DROP INDEX IF EXISTS idx_messages_grove;
CREATE INDEX IF NOT EXISTS idx_messages_project ON messages(project_id);

DROP INDEX IF EXISTS idx_groups_grove;
CREATE INDEX IF NOT EXISTS idx_groups_project ON groups(project_id);

DROP INDEX IF EXISTS idx_gcp_sa_grove;
CREATE INDEX IF NOT EXISTS idx_gcp_sa_project ON gcp_service_accounts(project_id);
`
	if _, err := tx.ExecContext(ctx, indexSQL); err != nil {
		return fmt.Errorf("updating indexes: %w", err)
	}

	return nil
}

// migrationV51 adds group_id to messages for correlating set[] deliveries.
const migrationV51 = `
ALTER TABLE messages ADD COLUMN group_id TEXT NOT NULL DEFAULT '';
`

// migrationV52 renames the idle activity to working for clearer agent state reporting.
const migrationV52 = `
UPDATE agents SET activity = 'working' WHERE activity = 'idle';
UPDATE agents SET stalled_from_activity = 'working' WHERE stalled_from_activity = 'idle';
`

// migrationV53 adds an index on (created, id) to allow_list for efficient keyset pagination.
// It also ensures the allow_list table exists, because databases created before V48/V49 were
// inserted into the migration sequence already have version 48 recorded with different content
// (the grove-to-project rename that is now V50). On those databases V48 is skipped, so the
// allow_list table was never created.
const migrationV53 = `
CREATE TABLE IF NOT EXISTS allow_list (
    id TEXT PRIMARY KEY,
    email TEXT NOT NULL,
    note TEXT NOT NULL DEFAULT '',
    added_by TEXT NOT NULL,
    invite_id TEXT NOT NULL DEFAULT '',
    created TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE UNIQUE INDEX IF NOT EXISTS allow_list_email_unique ON allow_list (LOWER(email));
CREATE TABLE IF NOT EXISTS invite_codes (
    id TEXT PRIMARY KEY,
    code_hash TEXT NOT NULL UNIQUE,
    code_prefix TEXT NOT NULL,
    max_uses INTEGER NOT NULL DEFAULT 1,
    use_count INTEGER NOT NULL DEFAULT 0,
    expires_at TIMESTAMP NOT NULL,
    revoked INTEGER NOT NULL DEFAULT 0,
    created_by TEXT NOT NULL,
    note TEXT NOT NULL DEFAULT '',
    created TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE INDEX IF NOT EXISTS idx_invite_codes_expires ON invite_codes(expires_at);
CREATE INDEX IF NOT EXISTS idx_allow_list_created_id ON allow_list (created DESC, id DESC);
`

// tableExists checks whether a table with the given name exists in the database.
func tableExists(ctx context.Context, tx *sql.Tx, tableName string) (bool, error) {
	var name string
	err := tx.QueryRowContext(ctx,
		"SELECT table_name FROM information_schema.tables WHERE table_name=$1 AND table_schema='public'", tableName,
	).Scan(&name)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, nil
}

// columnExists checks whether a column with the given name exists in the specified table.
func columnExists(ctx context.Context, tx *sql.Tx, tableName, columnName string) (bool, error) {
	var name string
	err := tx.QueryRowContext(ctx,
		"SELECT column_name FROM information_schema.columns WHERE table_name=$1 AND column_name=$2 AND table_schema='public'",
		tableName, columnName,
	).Scan(&name)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, nil
}
