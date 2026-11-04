CREATE TABLE image_update_settings (
 singleton BOOLEAN PRIMARY KEY DEFAULT true CHECK(singleton),
 interval_minutes INTEGER NOT NULL DEFAULT 360 CHECK(interval_minutes BETWEEN 15 AND 10080),
 maintenance JSONB NOT NULL DEFAULT '{"days":[0],"start":"03:00","minutes":120,"timezone":"UTC"}',
 version BIGINT NOT NULL DEFAULT 1
);
INSERT INTO image_update_settings(singleton) VALUES(true);
CREATE TABLE image_update_hosts (
 agent_id UUID PRIMARY KEY REFERENCES agent_identities(id) ON DELETE CASCADE,
 next_check TIMESTAMPTZ NOT NULL DEFAULT now(),
 checked_at TIMESTAMPTZ,
 message TEXT NOT NULL DEFAULT '',
 cursor_id TEXT NOT NULL DEFAULT '',
 failures INTEGER NOT NULL DEFAULT 0
);
CREATE TABLE container_image_updates (
 agent_id UUID NOT NULL REFERENCES agent_identities(id) ON DELETE CASCADE,
 container_id TEXT NOT NULL,
 result JSONB NOT NULL,
 checked_at TIMESTAMPTZ NOT NULL DEFAULT now(),
 last_success_at TIMESTAMPTZ,
 last_success JSONB,
 ignored BOOLEAN NOT NULL DEFAULT false,
 PRIMARY KEY(agent_id,container_id)
);
CREATE TABLE stack_update_policies (
 stack_id TEXT PRIMARY KEY REFERENCES stack_bindings(stack_id) ON DELETE CASCADE,
 enabled BOOLEAN NOT NULL DEFAULT false,
 maintenance JSONB,
 excluded_services JSONB NOT NULL DEFAULT '[]',
 approved_configuration TEXT NOT NULL DEFAULT '',
 approved_services JSONB NOT NULL DEFAULT '[]',
 paused_reason TEXT NOT NULL DEFAULT '',
 last_slot TEXT NOT NULL DEFAULT '',
 version BIGINT NOT NULL DEFAULT 1,
 updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
ALTER TABLE stack_deployment_previews ADD COLUMN tracking JSONB NOT NULL DEFAULT '{}';
ALTER TABLE stack_deployments ADD COLUMN tracking JSONB NOT NULL DEFAULT '{}';
ALTER TABLE stack_deployments ADD COLUMN automatic BOOLEAN NOT NULL DEFAULT false;
ALTER TABLE stack_deployments ADD COLUMN policy_version BIGINT;
ALTER TABLE alert_rules DROP CONSTRAINT alert_rules_kind_check;
ALTER TABLE alert_rules ADD CONSTRAINT alert_rules_kind_check CHECK(kind IN ('device_unavailable','threshold','backup_failure','backup_overdue','image_update'));
INSERT INTO alert_rules(id,name,kind,enabled,config) VALUES('00000000-0000-0000-0000-000000000024','Container image updates','image_update',false,'{}');
CREATE TABLE image_update_notices (
 agent_id UUID NOT NULL REFERENCES agent_identities(id) ON DELETE CASCADE,
 container_id TEXT NOT NULL,
 digest TEXT NOT NULL,
 created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
 PRIMARY KEY(agent_id,container_id,digest)
);
