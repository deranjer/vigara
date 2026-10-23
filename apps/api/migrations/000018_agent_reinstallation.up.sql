ALTER TABLE agent_enrollment_tokens ADD COLUMN replacement_agent_id UUID REFERENCES agent_identities(id) ON DELETE CASCADE;
CREATE INDEX agent_enrollment_tokens_replacement_idx ON agent_enrollment_tokens(replacement_agent_id) WHERE replacement_agent_id IS NOT NULL;
