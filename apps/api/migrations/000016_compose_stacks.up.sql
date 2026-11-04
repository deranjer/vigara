CREATE TABLE stack_drafts (
 stack_id TEXT NOT NULL,
 user_id UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
 base_commit TEXT NOT NULL DEFAULT '',
 files JSONB NOT NULL,
 version BIGINT NOT NULL DEFAULT 1,
 updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
 PRIMARY KEY(stack_id,user_id)
);
-- Runtime bindings cannot be moved by Git edits. Transfers require a separate fencing workflow.
CREATE TABLE stack_bindings (
 stack_id TEXT PRIMARY KEY,
 agent_id UUID NOT NULL REFERENCES agent_identities(id),
 project_name TEXT NOT NULL,
 UNIQUE(agent_id,project_name)
);
CREATE TABLE stack_deployments (
 id TEXT PRIMARY KEY,
 stack_id TEXT NOT NULL REFERENCES stack_bindings(stack_id),
 agent_id UUID NOT NULL REFERENCES agent_identities(id),
 project_name TEXT NOT NULL,
 actor_id UUID REFERENCES users(id) ON DELETE SET NULL,
 actor_name TEXT NOT NULL,
 commit_id TEXT NOT NULL,
 configuration_digest TEXT NOT NULL,
 snapshot JSONB NOT NULL,
 plan_token TEXT NOT NULL,
 pull BOOLEAN NOT NULL DEFAULT false,
 status TEXT NOT NULL CHECK(status IN ('queued','running','succeeded','failed','unknown')),
 stage TEXT NOT NULL DEFAULT 'queued',
 message TEXT NOT NULL DEFAULT '',
 created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
 finished_at TIMESTAMPTZ
);
CREATE UNIQUE INDEX stack_one_active_deployment ON stack_deployments(stack_id) WHERE status IN ('queued','running','unknown');
CREATE INDEX stack_deployment_history ON stack_deployments(stack_id,created_at DESC);
