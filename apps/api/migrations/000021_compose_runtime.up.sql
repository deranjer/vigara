-- Initial profile bindings are immutable: replacement requires future fencing.
CREATE TABLE stack_profile_bindings (
 profile_id TEXT PRIMARY KEY,
 agent_id UUID NOT NULL REFERENCES agent_identities(id),
 ownership_epoch BIGINT NOT NULL DEFAULT 1 CHECK (ownership_epoch = 1),
 created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
ALTER TABLE stack_deployments ADD COLUMN images JSONB NOT NULL DEFAULT '{}';
ALTER TABLE stack_deployments ADD COLUMN result JSONB NOT NULL DEFAULT '{}';
CREATE TABLE stack_deployment_previews (
 stack_id TEXT NOT NULL,
 user_id UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
 token TEXT NOT NULL,
 agent_id UUID NOT NULL REFERENCES agent_identities(id),
 configuration_digest TEXT NOT NULL,
 commit_id TEXT NOT NULL,
 images JSONB NOT NULL,
 expires_at TIMESTAMPTZ NOT NULL DEFAULT now() + interval '15 minutes',
 PRIMARY KEY(stack_id,user_id)
);
