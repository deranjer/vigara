-- Initial schema for a fresh Vigara installation.
-- Includes all server features and native GitHub App authentication.
-- Previous migration histories require a fresh database; they are not upgraded.

CREATE EXTENSION IF NOT EXISTS pgcrypto WITH SCHEMA public;

CREATE TABLE agent_download_sessions (
    id uuid NOT NULL,
    device_name text NOT NULL,
    target text NOT NULL,
    version text NOT NULL,
    status text DEFAULT 'waiting'::text NOT NULL,
    expires_at timestamp with time zone NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT agent_download_sessions_device_name_check CHECK (((length(device_name) >= 1) AND (length(device_name) <= 120))),
    CONSTRAINT agent_download_sessions_status_check CHECK ((status = ANY (ARRAY['waiting'::text, 'enrolled'::text, 'canceled'::text, 'expired'::text]))),
    CONSTRAINT agent_download_sessions_target_check CHECK ((target = ANY (ARRAY['windows-amd64'::text, 'linux-amd64'::text, 'linux-arm64'::text])))
);

CREATE TABLE agent_enrollment_tokens (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    token_hash bytea NOT NULL,
    capabilities text[] DEFAULT '{}'::text[] NOT NULL,
    expires_at timestamp with time zone NOT NULL,
    used_at timestamp with time zone,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    replacement_agent_id uuid,
    managed_backup bytea
);

CREATE TABLE agent_identities (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    device_id uuid NOT NULL,
    credential_hash bytea NOT NULL,
    protocol_version text NOT NULL,
    revoked_at timestamp with time zone,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    platform text DEFAULT ''::text NOT NULL,
    architecture text DEFAULT ''::text NOT NULL,
    agent_version text DEFAULT ''::text NOT NULL,
    capabilities jsonb DEFAULT '[]'::jsonb NOT NULL,
    allowed_capabilities text[] DEFAULT '{}'::text[] NOT NULL
);

CREATE TABLE agent_reports (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    agent_id uuid NOT NULL,
    report_id text NOT NULL,
    payload jsonb NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL
);

CREATE TABLE agent_telemetry (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    agent_id uuid NOT NULL,
    report_id text NOT NULL,
    observed_at timestamp with time zone NOT NULL,
    payload jsonb NOT NULL,
    received_at timestamp with time zone DEFAULT now() NOT NULL
)
WITH (autovacuum_vacuum_scale_factor='0.02', autovacuum_analyze_scale_factor='0.01');

CREATE TABLE agent_telemetry_latest (
    agent_id uuid NOT NULL,
    report_id text NOT NULL,
    observed_at timestamp with time zone NOT NULL,
    payload jsonb NOT NULL,
    received_at timestamp with time zone NOT NULL
)
WITH (autovacuum_vacuum_scale_factor='0.02', autovacuum_analyze_scale_factor='0.01');

CREATE TABLE alert_events (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    incident_id uuid NOT NULL,
    transition text NOT NULL,
    occurred_at timestamp with time zone DEFAULT now() NOT NULL,
    payload jsonb NOT NULL,
    delivered_at timestamp with time zone,
    CONSTRAINT alert_events_transition_check CHECK ((transition = ANY (ARRAY['firing'::text, 'resolved'::text])))
);

CREATE TABLE alert_incidents (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    rule_id uuid NOT NULL,
    dimension text DEFAULT ''::text NOT NULL,
    status text NOT NULL,
    summary text NOT NULL,
    started_at timestamp with time zone NOT NULL,
    last_observed_at timestamp with time zone,
    resolved_at timestamp with time zone,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT alert_incidents_status_check CHECK ((status = ANY (ARRAY['firing'::text, 'resolved'::text])))
);

CREATE TABLE alert_rules (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    name text NOT NULL,
    kind text NOT NULL,
    enabled boolean DEFAULT true NOT NULL,
    config jsonb NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT alert_rules_kind_check CHECK ((kind = ANY (ARRAY['device_unavailable'::text, 'threshold'::text, 'backup_failure'::text, 'backup_overdue'::text]))),
    CONSTRAINT alert_rules_name_check CHECK (((length(name) >= 1) AND (length(name) <= 120)))
);

CREATE TABLE android_file_backups (
    object_id uuid DEFAULT gen_random_uuid() NOT NULL,
    agent_id uuid NOT NULL,
    backup_id uuid NOT NULL,
    file_id uuid NOT NULL,
    display_name text NOT NULL,
    content_sha256 bytea NOT NULL,
    byte_size bigint NOT NULL,
    status text NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    committed_at timestamp with time zone,
    CONSTRAINT android_file_backups_byte_size_check CHECK ((byte_size > 0)),
    CONSTRAINT android_file_backups_check CHECK (((status = 'committed'::text) = (committed_at IS NOT NULL))),
    CONSTRAINT android_file_backups_content_sha256_check CHECK ((octet_length(content_sha256) = 32)),
    CONSTRAINT android_file_backups_status_check CHECK ((status = ANY (ARRAY['uploading'::text, 'committed'::text])))
);

CREATE TABLE app_setup (
    singleton_id smallint NOT NULL,
    completed_at timestamp with time zone,
    CONSTRAINT app_setup_singleton_id_check CHECK ((singleton_id = 1))
);

CREATE TABLE audit_events (
    id bigint NOT NULL,
    occurred_at timestamp with time zone DEFAULT now() NOT NULL,
    actor_user_id uuid,
    actor_name text NOT NULL,
    action text NOT NULL,
    target_kind text DEFAULT ''::text NOT NULL,
    target_id text DEFAULT ''::text NOT NULL,
    agent_id uuid,
    outcome text NOT NULL,
    detail jsonb DEFAULT '{}'::jsonb NOT NULL,
    CONSTRAINT audit_events_action_check CHECK (((length(action) >= 1) AND (length(action) <= 64))),
    CONSTRAINT audit_events_actor_name_check CHECK (((length(actor_name) >= 1) AND (length(actor_name) <= 128))),
    CONSTRAINT audit_events_outcome_check CHECK ((outcome = ANY (ARRAY['success'::text, 'failure'::text, 'denied'::text]))),
    CONSTRAINT audit_events_target_id_check CHECK ((length(target_id) <= 256)),
    CONSTRAINT audit_events_target_kind_check CHECK ((length(target_kind) <= 64))
);

ALTER TABLE audit_events ALTER COLUMN id ADD GENERATED ALWAYS AS IDENTITY (
    SEQUENCE NAME audit_events_id_seq
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1
);

CREATE TABLE backup_job_attempts (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    job_id uuid NOT NULL,
    attempt_number integer NOT NULL,
    lease_token uuid DEFAULT gen_random_uuid() NOT NULL,
    leased_at timestamp with time zone DEFAULT now() NOT NULL,
    lease_expires_at timestamp with time zone NOT NULL,
    finished_at timestamp with time zone,
    result_status text,
    result_id text,
    result_payload jsonb,
    outcome_unknown boolean DEFAULT false NOT NULL,
    progress_sequence bigint DEFAULT 0 NOT NULL,
    progress_payload jsonb,
    progress_at timestamp with time zone,
    CONSTRAINT backup_job_attempts_attempt_number_check CHECK (((attempt_number >= 1) AND (attempt_number <= 100))),
    CONSTRAINT backup_job_attempts_check CHECK ((lease_expires_at > leased_at)),
    CONSTRAINT backup_job_attempts_result_pair_check CHECK (((result_id IS NULL) = (result_payload IS NULL))),
    CONSTRAINT backup_job_attempts_result_status_check CHECK ((result_status = ANY (ARRAY['succeeded'::text, 'failed'::text, 'timed_out'::text, 'cancelled'::text])))
);

CREATE TABLE backup_jobs (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    policy_id uuid NOT NULL,
    agent_id uuid NOT NULL,
    profile_id text NOT NULL,
    policy_revision integer NOT NULL,
    policy_snapshot jsonb NOT NULL,
    scheduled_for timestamp with time zone NOT NULL,
    status text DEFAULT 'pending'::text NOT NULL,
    attempt_count integer DEFAULT 0 NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT backup_jobs_attempt_count_check CHECK (((attempt_count >= 0) AND (attempt_count <= 100))),
    CONSTRAINT backup_jobs_policy_revision_check CHECK ((policy_revision > 0)),
    CONSTRAINT backup_jobs_policy_snapshot_check CHECK ((jsonb_typeof(policy_snapshot) = 'object'::text)),
    CONSTRAINT backup_jobs_profile_id_check CHECK (((length(profile_id) >= 1) AND (length(profile_id) <= 64))),
    CONSTRAINT backup_jobs_status_check CHECK ((status = ANY (ARRAY['waiting'::text, 'pending'::text, 'leased'::text, 'completed'::text, 'skipped'::text, 'unknown'::text])))
);

CREATE TABLE backup_policies (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    device_id uuid NOT NULL,
    agent_id uuid NOT NULL,
    profile_id text NOT NULL,
    name text NOT NULL,
    destination text NOT NULL,
    included_paths jsonb NOT NULL,
    excluded_paths jsonb NOT NULL,
    retention jsonb NOT NULL,
    schedule jsonb NOT NULL,
    missed_run_policy text NOT NULL,
    disconnected_device_policy text NOT NULL,
    enabled boolean DEFAULT true NOT NULL,
    revision integer DEFAULT 1 NOT NULL,
    retired_at timestamp with time zone,
    next_run_at timestamp with time zone NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT backup_policies_destination_check CHECK (((length(destination) >= 1) AND (length(destination) <= 2048))),
    CONSTRAINT backup_policies_disconnected_device_policy_check CHECK ((disconnected_device_policy = ANY (ARRAY['wait'::text, 'skip'::text]))),
    CONSTRAINT backup_policies_excluded_paths_check CHECK ((jsonb_typeof(excluded_paths) = 'array'::text)),
    CONSTRAINT backup_policies_included_paths_check CHECK ((jsonb_typeof(included_paths) = 'array'::text)),
    CONSTRAINT backup_policies_missed_run_policy_check CHECK ((missed_run_policy = ANY (ARRAY['skip'::text, 'run_once'::text]))),
    CONSTRAINT backup_policies_name_check CHECK (((length(name) >= 1) AND (length(name) <= 120))),
    CONSTRAINT backup_policies_profile_id_check CHECK (((length(profile_id) >= 1) AND (length(profile_id) <= 64))),
    CONSTRAINT backup_policies_profile_id_check1 CHECK ((profile_id ~ '^[a-z0-9_-]+$'::text)),
    CONSTRAINT backup_policies_retention_check CHECK ((jsonb_typeof(retention) = 'object'::text)),
    CONSTRAINT backup_policies_revision_check CHECK ((revision > 0)),
    CONSTRAINT backup_policies_schedule_check CHECK ((jsonb_typeof(schedule) = 'object'::text))
);

CREATE TABLE backup_status (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    device_id uuid NOT NULL,
    policy_key text NOT NULL,
    status text NOT NULL,
    observed_at timestamp with time zone NOT NULL,
    message text DEFAULT ''::text NOT NULL,
    source_id text NOT NULL,
    CONSTRAINT backup_status_message_check CHECK ((length(message) <= 512)),
    CONSTRAINT backup_status_policy_key_check CHECK (((length(policy_key) >= 1) AND (length(policy_key) <= 128))),
    CONSTRAINT backup_status_source_id_check CHECK (((length(source_id) >= 1) AND (length(source_id) <= 128))),
    CONSTRAINT backup_status_status_check CHECK ((status = ANY (ARRAY['success'::text, 'failure'::text, 'running'::text])))
);

CREATE TABLE device_addresses (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    device_id uuid NOT NULL,
    address text NOT NULL,
    address_type text NOT NULL,
    provenance text NOT NULL,
    first_seen_at timestamp with time zone DEFAULT now() NOT NULL,
    last_seen_at timestamp with time zone DEFAULT now() NOT NULL
);

CREATE TABLE device_availability_policies (
    device_id uuid NOT NULL,
    weekdays jsonb NOT NULL,
    awake_start time without time zone NOT NULL,
    awake_end time without time zone NOT NULL,
    timezone text NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT device_availability_policies_check CHECK ((awake_start <> awake_end)),
    CONSTRAINT device_availability_policies_timezone_check CHECK (((length(timezone) >= 1) AND (length(timezone) <= 128))),
    CONSTRAINT device_availability_policies_weekdays_check CHECK ((jsonb_typeof(weekdays) = 'array'::text))
);

CREATE TABLE device_identifier_sources (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    identifier_id uuid NOT NULL,
    provenance text NOT NULL,
    reported_value text NOT NULL,
    first_seen_at timestamp with time zone DEFAULT now() NOT NULL,
    last_seen_at timestamp with time zone DEFAULT now() NOT NULL
);

CREATE TABLE device_identifiers (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    device_id uuid NOT NULL,
    kind text NOT NULL,
    normalized_value text NOT NULL,
    reported_value text NOT NULL,
    provenance text NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL
);

CREATE TABLE device_maintenance_windows (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    device_id uuid NOT NULL,
    starts_at timestamp with time zone NOT NULL,
    ends_at timestamp with time zone NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT device_maintenance_windows_check CHECK ((ends_at > starts_at)),
    CONSTRAINT device_maintenance_windows_check1 CHECK ((ends_at <= (starts_at + '30 days'::interval)))
);

CREATE TABLE device_sources (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    device_id uuid NOT NULL,
    source_type text NOT NULL,
    source_id text NOT NULL,
    provenance text NOT NULL,
    last_seen_at timestamp with time zone DEFAULT now() NOT NULL
);

CREATE TABLE devices (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    display_name text NOT NULL,
    platform_id text,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    last_seen_at timestamp with time zone
);

CREATE TABLE managed_backup_profiles (
    agent_id uuid NOT NULL,
    policy_id uuid NOT NULL,
    config bytea NOT NULL,
    revision bigint DEFAULT 1 NOT NULL,
    applied_revision bigint DEFAULT 0 NOT NULL,
    initialized boolean DEFAULT false NOT NULL,
    error_code text DEFAULT ''::text NOT NULL,
    reported_at timestamp with time zone
);

CREATE TABLE mcp_oauth_clients (
    id text NOT NULL,
    name text NOT NULL,
    redirect_uris text[] NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL
);

CREATE TABLE mcp_oauth_codes (
    code_hash bytea NOT NULL,
    session_id uuid NOT NULL,
    client_id text NOT NULL,
    redirect_uri text NOT NULL,
    challenge text NOT NULL,
    scopes text[] NOT NULL,
    resource text NOT NULL,
    expires_at timestamp with time zone NOT NULL
);

CREATE TABLE mcp_oauth_grants (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    session_id uuid NOT NULL,
    client_id text NOT NULL,
    scopes text[] NOT NULL,
    resource text NOT NULL,
    access_hash bytea NOT NULL,
    access_expires_at timestamp with time zone NOT NULL,
    expires_at timestamp with time zone NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL
);

CREATE TABLE mcp_oauth_refresh_tokens (
    token_hash bytea NOT NULL,
    grant_id uuid NOT NULL,
    used boolean DEFAULT false NOT NULL
);

CREATE TABLE notification_deliveries (
    event_id uuid NOT NULL,
    destination_id text NOT NULL,
    status text DEFAULT 'pending'::text NOT NULL,
    attempts integer DEFAULT 0 NOT NULL,
    next_attempt_at timestamp with time zone DEFAULT now() NOT NULL,
    lease_token uuid,
    lease_until timestamp with time zone,
    last_error_code text,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT notification_deliveries_attempts_check CHECK ((attempts >= 0)),
    CONSTRAINT notification_deliveries_check CHECK ((((status = 'sending'::text) AND (lease_token IS NOT NULL) AND (lease_until IS NOT NULL)) OR ((status <> 'sending'::text) AND (lease_token IS NULL) AND (lease_until IS NULL)))),
    CONSTRAINT notification_deliveries_status_check CHECK ((status = ANY (ARRAY['pending'::text, 'sending'::text, 'delivered'::text, 'dead'::text])))
);

CREATE TABLE notification_destinations (
    id text NOT NULL,
    activated_at timestamp with time zone NOT NULL,
    CONSTRAINT notification_destinations_id_check CHECK (((length(id) >= 1) AND (length(id) <= 120)))
);

CREATE TABLE scheduled_tasks (
    id text NOT NULL,
    enabled boolean DEFAULT true NOT NULL,
    settings jsonb DEFAULT '{}'::jsonb NOT NULL,
    interval_seconds integer NOT NULL,
    status text DEFAULT 'never'::text NOT NULL,
    last_started_at timestamp with time zone,
    last_finished_at timestamp with time zone,
    next_run_at timestamp with time zone DEFAULT now() NOT NULL,
    last_error text DEFAULT ''::text NOT NULL,
    run_id uuid,
    lease_until timestamp with time zone,
    CONSTRAINT scheduled_tasks_id_check CHECK (((length(id) >= 1) AND (length(id) <= 80))),
    CONSTRAINT scheduled_tasks_interval_seconds_check CHECK ((interval_seconds > 0)),
    CONSTRAINT scheduled_tasks_status_check CHECK ((status = ANY (ARRAY['never'::text, 'running'::text, 'succeeded'::text, 'failed'::text])))
);

CREATE TABLE stack_bindings (
    stack_id text NOT NULL,
    agent_id uuid NOT NULL,
    project_name text NOT NULL,
    directory text DEFAULT ''::text NOT NULL,
    display_name text DEFAULT ''::text NOT NULL
);

CREATE TABLE stack_deployment_previews (
    stack_id text NOT NULL,
    user_id uuid NOT NULL,
    token text NOT NULL,
    agent_id uuid NOT NULL,
    configuration_digest text NOT NULL,
    commit_id text NOT NULL,
    images jsonb NOT NULL,
    expires_at timestamp with time zone DEFAULT (now() + '00:15:00'::interval) NOT NULL
);

CREATE TABLE stack_deployments (
    id text NOT NULL,
    stack_id text NOT NULL,
    agent_id uuid NOT NULL,
    project_name text NOT NULL,
    actor_id uuid,
    actor_name text NOT NULL,
    commit_id text NOT NULL,
    configuration_digest text NOT NULL,
    snapshot jsonb NOT NULL,
    plan_token text NOT NULL,
    pull boolean DEFAULT false NOT NULL,
    status text NOT NULL,
    stage text DEFAULT 'queued'::text NOT NULL,
    message text DEFAULT ''::text NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    finished_at timestamp with time zone,
    images jsonb DEFAULT '{}'::jsonb NOT NULL,
    result jsonb DEFAULT '{}'::jsonb NOT NULL,
    CONSTRAINT stack_deployments_status_check CHECK ((status = ANY (ARRAY['queued'::text, 'running'::text, 'succeeded'::text, 'failed'::text, 'unknown'::text])))
);

CREATE TABLE stack_drafts (
    stack_id text NOT NULL,
    user_id uuid NOT NULL,
    base_commit text DEFAULT ''::text NOT NULL,
    files jsonb NOT NULL,
    version bigint DEFAULT 1 NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL
);

CREATE TABLE stack_git_settings (
    singleton boolean DEFAULT true NOT NULL,
    remote text NOT NULL,
    branch text NOT NULL,
    version bigint DEFAULT 1 NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    auth_mode text DEFAULT 'server'::text NOT NULL,
    CONSTRAINT stack_git_settings_auth_mode_check CHECK ((auth_mode = ANY (ARRAY['server'::text, 'github-app'::text]))),
    CONSTRAINT stack_git_settings_singleton_check CHECK (singleton)
);

CREATE TABLE stack_github_app (
    singleton boolean DEFAULT true NOT NULL,
    app_id bigint NOT NULL,
    slug text NOT NULL,
    installation_id bigint NOT NULL,
    remote text NOT NULL,
    branch text NOT NULL,
    credentials bytea NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT stack_github_app_singleton_check CHECK (singleton)
);

CREATE TABLE stack_github_app_setups (
    state_hash text NOT NULL,
    user_id uuid NOT NULL,
    session_hash text NOT NULL,
    phase text NOT NULL,
    remote text NOT NULL,
    branch text NOT NULL,
    credentials bytea,
    expires_at timestamp with time zone NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT stack_github_app_setups_phase_check CHECK ((phase = ANY (ARRAY['register'::text, 'converting'::text, 'install'::text, 'finishing'::text])))
);

CREATE TABLE stack_profile_bindings (
    profile_id text NOT NULL,
    agent_id uuid NOT NULL,
    ownership_epoch bigint DEFAULT 1 NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT stack_profile_bindings_ownership_epoch_check CHECK ((ownership_epoch = 1))
);

CREATE TABLE telemetry_ingest_limits (
    agent_id uuid NOT NULL,
    window_started_at timestamp with time zone NOT NULL,
    snapshot_count integer DEFAULT 0 NOT NULL,
    CONSTRAINT telemetry_ingest_limits_snapshot_count_check CHECK ((snapshot_count >= 0))
);

CREATE TABLE telemetry_metric_rollups (
    agent_id uuid NOT NULL,
    metric_name text NOT NULL,
    labels jsonb DEFAULT '{}'::jsonb NOT NULL,
    bucket_start timestamp with time zone NOT NULL,
    sample_count bigint NOT NULL,
    sum_value double precision NOT NULL,
    min_value double precision NOT NULL,
    max_value double precision NOT NULL,
    CONSTRAINT telemetry_metric_rollups_sample_count_check CHECK ((sample_count > 0))
)
WITH (autovacuum_vacuum_scale_factor='0.02', autovacuum_analyze_scale_factor='0.01');

CREATE TABLE telemetry_metric_series (
    agent_id uuid NOT NULL,
    metric_name text NOT NULL,
    labels jsonb DEFAULT '{}'::jsonb NOT NULL,
    legacy boolean DEFAULT false NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL
);

CREATE TABLE telemetry_report_dedup (
    agent_id uuid NOT NULL,
    report_id text NOT NULL,
    received_at timestamp with time zone DEFAULT now() NOT NULL
)
WITH (autovacuum_vacuum_scale_factor='0.02', autovacuum_analyze_scale_factor='0.01');

CREATE TABLE user_sessions (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    user_id uuid NOT NULL,
    token_hash bytea NOT NULL,
    expires_at timestamp with time zone NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL
);

CREATE TABLE users (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    username text NOT NULL,
    display_name text NOT NULL,
    password_hash text NOT NULL,
    role text DEFAULT 'admin'::text NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT users_role_check CHECK ((role = ANY (ARRAY['viewer'::text, 'operator'::text, 'admin'::text]))),
    CONSTRAINT users_username_check CHECK ((username = lower(username)))
);

ALTER TABLE ONLY agent_download_sessions
    ADD CONSTRAINT agent_download_sessions_pkey PRIMARY KEY (id);

ALTER TABLE ONLY agent_enrollment_tokens
    ADD CONSTRAINT agent_enrollment_tokens_pkey PRIMARY KEY (id);

ALTER TABLE ONLY agent_enrollment_tokens
    ADD CONSTRAINT agent_enrollment_tokens_token_hash_key UNIQUE (token_hash);

ALTER TABLE ONLY agent_identities
    ADD CONSTRAINT agent_identities_credential_hash_key UNIQUE (credential_hash);

ALTER TABLE ONLY agent_identities
    ADD CONSTRAINT agent_identities_pkey PRIMARY KEY (id);

ALTER TABLE ONLY agent_reports
    ADD CONSTRAINT agent_reports_agent_id_report_id_key UNIQUE (agent_id, report_id);

ALTER TABLE ONLY agent_reports
    ADD CONSTRAINT agent_reports_pkey PRIMARY KEY (id);

ALTER TABLE ONLY agent_telemetry
    ADD CONSTRAINT agent_telemetry_agent_id_report_id_key UNIQUE (agent_id, report_id);

ALTER TABLE ONLY agent_telemetry_latest
    ADD CONSTRAINT agent_telemetry_latest_pkey PRIMARY KEY (agent_id);

ALTER TABLE ONLY agent_telemetry
    ADD CONSTRAINT agent_telemetry_pkey PRIMARY KEY (id);

ALTER TABLE ONLY alert_events
    ADD CONSTRAINT alert_events_incident_id_transition_occurred_at_key UNIQUE (incident_id, transition, occurred_at);

ALTER TABLE ONLY alert_events
    ADD CONSTRAINT alert_events_pkey PRIMARY KEY (id);

ALTER TABLE ONLY alert_incidents
    ADD CONSTRAINT alert_incidents_pkey PRIMARY KEY (id);

ALTER TABLE ONLY alert_incidents
    ADD CONSTRAINT alert_incidents_rule_id_dimension_key UNIQUE (rule_id, dimension);

ALTER TABLE ONLY alert_rules
    ADD CONSTRAINT alert_rules_pkey PRIMARY KEY (id);

ALTER TABLE ONLY android_file_backups
    ADD CONSTRAINT android_file_backups_agent_id_backup_id_file_id_key UNIQUE (agent_id, backup_id, file_id);

ALTER TABLE ONLY android_file_backups
    ADD CONSTRAINT android_file_backups_pkey PRIMARY KEY (object_id);

ALTER TABLE ONLY app_setup
    ADD CONSTRAINT app_setup_pkey PRIMARY KEY (singleton_id);

ALTER TABLE ONLY audit_events
    ADD CONSTRAINT audit_events_pkey PRIMARY KEY (id);

ALTER TABLE ONLY backup_job_attempts
    ADD CONSTRAINT backup_job_attempts_job_id_attempt_number_key UNIQUE (job_id, attempt_number);

ALTER TABLE ONLY backup_job_attempts
    ADD CONSTRAINT backup_job_attempts_lease_token_key UNIQUE (lease_token);

ALTER TABLE ONLY backup_job_attempts
    ADD CONSTRAINT backup_job_attempts_pkey PRIMARY KEY (id);

ALTER TABLE ONLY backup_jobs
    ADD CONSTRAINT backup_jobs_pkey PRIMARY KEY (id);

ALTER TABLE ONLY backup_jobs
    ADD CONSTRAINT backup_jobs_policy_id_scheduled_for_key UNIQUE (policy_id, scheduled_for);

ALTER TABLE ONLY backup_policies
    ADD CONSTRAINT backup_policies_pkey PRIMARY KEY (id);

ALTER TABLE ONLY backup_status
    ADD CONSTRAINT backup_status_device_id_source_id_key UNIQUE (device_id, source_id);

ALTER TABLE ONLY backup_status
    ADD CONSTRAINT backup_status_pkey PRIMARY KEY (id);

ALTER TABLE ONLY device_addresses
    ADD CONSTRAINT device_addresses_device_id_address_address_type_provenance_key UNIQUE (device_id, address, address_type, provenance);

ALTER TABLE ONLY device_addresses
    ADD CONSTRAINT device_addresses_pkey PRIMARY KEY (id);

ALTER TABLE ONLY device_availability_policies
    ADD CONSTRAINT device_availability_policies_pkey PRIMARY KEY (device_id);

ALTER TABLE ONLY device_identifier_sources
    ADD CONSTRAINT device_identifier_sources_identifier_id_provenance_key UNIQUE (identifier_id, provenance);

ALTER TABLE ONLY device_identifier_sources
    ADD CONSTRAINT device_identifier_sources_pkey PRIMARY KEY (id);

ALTER TABLE ONLY device_identifiers
    ADD CONSTRAINT device_identifiers_kind_normalized_value_key UNIQUE (kind, normalized_value);

ALTER TABLE ONLY device_identifiers
    ADD CONSTRAINT device_identifiers_pkey PRIMARY KEY (id);

ALTER TABLE ONLY device_maintenance_windows
    ADD CONSTRAINT device_maintenance_windows_pkey PRIMARY KEY (id);

ALTER TABLE ONLY device_sources
    ADD CONSTRAINT device_sources_pkey PRIMARY KEY (id);

ALTER TABLE ONLY device_sources
    ADD CONSTRAINT device_sources_source_type_source_id_key UNIQUE (source_type, source_id);

ALTER TABLE ONLY devices
    ADD CONSTRAINT devices_pkey PRIMARY KEY (id);

ALTER TABLE ONLY managed_backup_profiles
    ADD CONSTRAINT managed_backup_profiles_pkey PRIMARY KEY (agent_id);

ALTER TABLE ONLY managed_backup_profiles
    ADD CONSTRAINT managed_backup_profiles_policy_id_key UNIQUE (policy_id);

ALTER TABLE ONLY mcp_oauth_clients
    ADD CONSTRAINT mcp_oauth_clients_pkey PRIMARY KEY (id);

ALTER TABLE ONLY mcp_oauth_codes
    ADD CONSTRAINT mcp_oauth_codes_pkey PRIMARY KEY (code_hash);

ALTER TABLE ONLY mcp_oauth_grants
    ADD CONSTRAINT mcp_oauth_grants_access_hash_key UNIQUE (access_hash);

ALTER TABLE ONLY mcp_oauth_grants
    ADD CONSTRAINT mcp_oauth_grants_pkey PRIMARY KEY (id);

ALTER TABLE ONLY mcp_oauth_refresh_tokens
    ADD CONSTRAINT mcp_oauth_refresh_tokens_pkey PRIMARY KEY (token_hash);

ALTER TABLE ONLY notification_deliveries
    ADD CONSTRAINT notification_deliveries_pkey PRIMARY KEY (event_id, destination_id);

ALTER TABLE ONLY notification_destinations
    ADD CONSTRAINT notification_destinations_pkey PRIMARY KEY (id);

ALTER TABLE ONLY scheduled_tasks
    ADD CONSTRAINT scheduled_tasks_pkey PRIMARY KEY (id);

ALTER TABLE ONLY stack_bindings
    ADD CONSTRAINT stack_bindings_agent_id_project_name_key UNIQUE (agent_id, project_name);

ALTER TABLE ONLY stack_bindings
    ADD CONSTRAINT stack_bindings_pkey PRIMARY KEY (stack_id);

ALTER TABLE ONLY stack_deployment_previews
    ADD CONSTRAINT stack_deployment_previews_pkey PRIMARY KEY (stack_id, user_id);

ALTER TABLE ONLY stack_deployments
    ADD CONSTRAINT stack_deployments_pkey PRIMARY KEY (id);

ALTER TABLE ONLY stack_drafts
    ADD CONSTRAINT stack_drafts_pkey PRIMARY KEY (stack_id, user_id);

ALTER TABLE ONLY stack_git_settings
    ADD CONSTRAINT stack_git_settings_pkey PRIMARY KEY (singleton);

ALTER TABLE ONLY stack_github_app
    ADD CONSTRAINT stack_github_app_pkey PRIMARY KEY (singleton);

ALTER TABLE ONLY stack_github_app_setups
    ADD CONSTRAINT stack_github_app_setups_pkey PRIMARY KEY (state_hash);

ALTER TABLE ONLY stack_profile_bindings
    ADD CONSTRAINT stack_profile_bindings_pkey PRIMARY KEY (profile_id);

ALTER TABLE ONLY telemetry_ingest_limits
    ADD CONSTRAINT telemetry_ingest_limits_pkey PRIMARY KEY (agent_id);

ALTER TABLE ONLY telemetry_metric_rollups
    ADD CONSTRAINT telemetry_metric_rollups_pkey PRIMARY KEY (agent_id, metric_name, labels, bucket_start);

ALTER TABLE ONLY telemetry_metric_series
    ADD CONSTRAINT telemetry_metric_series_pkey PRIMARY KEY (agent_id, metric_name, labels);

ALTER TABLE ONLY telemetry_report_dedup
    ADD CONSTRAINT telemetry_report_dedup_pkey PRIMARY KEY (agent_id, report_id);

ALTER TABLE ONLY user_sessions
    ADD CONSTRAINT user_sessions_pkey PRIMARY KEY (id);

ALTER TABLE ONLY user_sessions
    ADD CONSTRAINT user_sessions_token_hash_key UNIQUE (token_hash);

ALTER TABLE ONLY users
    ADD CONSTRAINT users_pkey PRIMARY KEY (id);

ALTER TABLE ONLY users
    ADD CONSTRAINT users_username_key UNIQUE (username);

CREATE INDEX agent_enrollment_tokens_replacement_idx ON agent_enrollment_tokens USING btree (replacement_agent_id) WHERE (replacement_agent_id IS NOT NULL);

CREATE INDEX agent_reports_created_at_idx ON agent_reports USING btree (created_at);

CREATE INDEX agent_telemetry_agent_observed_at_idx ON agent_telemetry USING btree (agent_id, observed_at DESC);

CREATE INDEX agent_telemetry_observed_at_idx ON agent_telemetry USING btree (observed_at, id);

CREATE INDEX alert_events_pending_idx ON alert_events USING btree (occurred_at) WHERE (delivered_at IS NULL);

CREATE INDEX alert_incidents_status_idx ON alert_incidents USING btree (status, updated_at DESC);

CREATE INDEX alert_rules_enabled_idx ON alert_rules USING btree (enabled, kind);

CREATE INDEX android_file_backups_agent_created_idx ON android_file_backups USING btree (agent_id, created_at DESC);

CREATE INDEX audit_events_actor_idx ON audit_events USING btree (actor_user_id, occurred_at DESC) WHERE (actor_user_id IS NOT NULL);

CREATE INDEX audit_events_agent_idx ON audit_events USING btree (agent_id, occurred_at DESC) WHERE (agent_id IS NOT NULL);

CREATE INDEX audit_events_occurred_idx ON audit_events USING btree (occurred_at DESC, id DESC);

CREATE INDEX backup_job_attempts_active_idx ON backup_job_attempts USING btree (lease_expires_at) WHERE (finished_at IS NULL);

CREATE INDEX backup_job_attempts_progress_idx ON backup_job_attempts USING btree (job_id, attempt_number DESC);

CREATE INDEX backup_jobs_agent_pending_idx ON backup_jobs USING btree (agent_id, created_at) WHERE (status = 'pending'::text);

CREATE INDEX backup_jobs_coordination_idx ON backup_jobs USING btree (agent_id, profile_id, status);

CREATE UNIQUE INDEX backup_policies_agent_profile_active_idx ON backup_policies USING btree (agent_id, profile_id) WHERE (retired_at IS NULL);

CREATE INDEX backup_policies_due_idx ON backup_policies USING btree (next_run_at) WHERE enabled;

CREATE INDEX backup_status_latest_idx ON backup_status USING btree (device_id, policy_key, observed_at DESC);

CREATE INDEX device_addresses_device_idx ON device_addresses USING btree (device_id);

CREATE INDEX device_identifier_sources_identifier_idx ON device_identifier_sources USING btree (identifier_id);

CREATE INDEX device_identifiers_device_idx ON device_identifiers USING btree (device_id);

CREATE INDEX device_maintenance_windows_device_idx ON device_maintenance_windows USING btree (device_id, starts_at, ends_at);

CREATE INDEX device_sources_device_idx ON device_sources USING btree (device_id);

CREATE UNIQUE INDEX devices_platform_id_unique ON devices USING btree (platform_id) WHERE (platform_id IS NOT NULL);

CREATE INDEX mcp_oauth_codes_expiry_idx ON mcp_oauth_codes USING btree (expires_at);

CREATE INDEX mcp_oauth_grants_expiry_idx ON mcp_oauth_grants USING btree (expires_at);

CREATE INDEX mcp_oauth_grants_session_idx ON mcp_oauth_grants USING btree (session_id);

CREATE INDEX mcp_oauth_refresh_grant_idx ON mcp_oauth_refresh_tokens USING btree (grant_id);

CREATE INDEX notification_deliveries_claim_idx ON notification_deliveries USING btree (destination_id, next_attempt_at, updated_at) WHERE (status = ANY (ARRAY['pending'::text, 'sending'::text]));

CREATE INDEX notification_deliveries_incident_order_idx ON notification_deliveries USING btree (destination_id, event_id, status);

CREATE INDEX notification_seed_events_idx ON alert_events USING btree (occurred_at, id);

CREATE INDEX stack_deployment_history ON stack_deployments USING btree (stack_id, created_at DESC);

CREATE INDEX stack_github_app_setups_expiry ON stack_github_app_setups USING btree (expires_at);

CREATE UNIQUE INDEX stack_one_active_deployment ON stack_deployments USING btree (stack_id) WHERE (status = ANY (ARRAY['queued'::text, 'running'::text, 'unknown'::text]));

CREATE INDEX telemetry_metric_rollups_bucket_idx ON telemetry_metric_rollups USING btree (bucket_start);

CREATE INDEX telemetry_report_dedup_received_idx ON telemetry_report_dedup USING btree (received_at);

CREATE INDEX user_sessions_expiry_idx ON user_sessions USING btree (expires_at);

ALTER TABLE ONLY agent_download_sessions
    ADD CONSTRAINT agent_download_sessions_id_fkey FOREIGN KEY (id) REFERENCES agent_enrollment_tokens(id) ON DELETE CASCADE;

ALTER TABLE ONLY agent_enrollment_tokens
    ADD CONSTRAINT agent_enrollment_tokens_replacement_agent_id_fkey FOREIGN KEY (replacement_agent_id) REFERENCES agent_identities(id) ON DELETE CASCADE;

ALTER TABLE ONLY agent_identities
    ADD CONSTRAINT agent_identities_device_id_fkey FOREIGN KEY (device_id) REFERENCES devices(id) ON DELETE CASCADE;

ALTER TABLE ONLY agent_reports
    ADD CONSTRAINT agent_reports_agent_id_fkey FOREIGN KEY (agent_id) REFERENCES agent_identities(id) ON DELETE CASCADE;

ALTER TABLE ONLY agent_telemetry
    ADD CONSTRAINT agent_telemetry_agent_id_fkey FOREIGN KEY (agent_id) REFERENCES agent_identities(id) ON DELETE CASCADE;

ALTER TABLE ONLY agent_telemetry_latest
    ADD CONSTRAINT agent_telemetry_latest_agent_id_fkey FOREIGN KEY (agent_id) REFERENCES agent_identities(id) ON DELETE CASCADE;

ALTER TABLE ONLY alert_events
    ADD CONSTRAINT alert_events_incident_id_fkey FOREIGN KEY (incident_id) REFERENCES alert_incidents(id) ON DELETE CASCADE;

ALTER TABLE ONLY alert_incidents
    ADD CONSTRAINT alert_incidents_rule_id_fkey FOREIGN KEY (rule_id) REFERENCES alert_rules(id) ON DELETE CASCADE;

ALTER TABLE ONLY android_file_backups
    ADD CONSTRAINT android_file_backups_agent_id_fkey FOREIGN KEY (agent_id) REFERENCES agent_identities(id) ON DELETE CASCADE;

ALTER TABLE ONLY audit_events
    ADD CONSTRAINT audit_events_actor_user_id_fkey FOREIGN KEY (actor_user_id) REFERENCES users(id) ON DELETE SET NULL;

ALTER TABLE ONLY audit_events
    ADD CONSTRAINT audit_events_agent_id_fkey FOREIGN KEY (agent_id) REFERENCES agent_identities(id) ON DELETE SET NULL;

ALTER TABLE ONLY backup_job_attempts
    ADD CONSTRAINT backup_job_attempts_job_id_fkey FOREIGN KEY (job_id) REFERENCES backup_jobs(id) ON DELETE RESTRICT;

ALTER TABLE ONLY backup_jobs
    ADD CONSTRAINT backup_jobs_agent_id_fkey FOREIGN KEY (agent_id) REFERENCES agent_identities(id) ON DELETE RESTRICT;

ALTER TABLE ONLY backup_jobs
    ADD CONSTRAINT backup_jobs_policy_id_fkey FOREIGN KEY (policy_id) REFERENCES backup_policies(id) ON DELETE RESTRICT;

ALTER TABLE ONLY backup_policies
    ADD CONSTRAINT backup_policies_agent_id_fkey FOREIGN KEY (agent_id) REFERENCES agent_identities(id) ON DELETE CASCADE;

ALTER TABLE ONLY backup_policies
    ADD CONSTRAINT backup_policies_device_id_fkey FOREIGN KEY (device_id) REFERENCES devices(id) ON DELETE CASCADE;

ALTER TABLE ONLY backup_status
    ADD CONSTRAINT backup_status_device_id_fkey FOREIGN KEY (device_id) REFERENCES devices(id) ON DELETE CASCADE;

ALTER TABLE ONLY device_addresses
    ADD CONSTRAINT device_addresses_device_id_fkey FOREIGN KEY (device_id) REFERENCES devices(id) ON DELETE CASCADE;

ALTER TABLE ONLY device_availability_policies
    ADD CONSTRAINT device_availability_policies_device_id_fkey FOREIGN KEY (device_id) REFERENCES devices(id) ON DELETE CASCADE;

ALTER TABLE ONLY device_identifier_sources
    ADD CONSTRAINT device_identifier_sources_identifier_id_fkey FOREIGN KEY (identifier_id) REFERENCES device_identifiers(id) ON DELETE CASCADE;

ALTER TABLE ONLY device_identifiers
    ADD CONSTRAINT device_identifiers_device_id_fkey FOREIGN KEY (device_id) REFERENCES devices(id) ON DELETE CASCADE;

ALTER TABLE ONLY device_maintenance_windows
    ADD CONSTRAINT device_maintenance_windows_device_id_fkey FOREIGN KEY (device_id) REFERENCES devices(id) ON DELETE CASCADE;

ALTER TABLE ONLY device_sources
    ADD CONSTRAINT device_sources_device_id_fkey FOREIGN KEY (device_id) REFERENCES devices(id) ON DELETE CASCADE;

ALTER TABLE ONLY managed_backup_profiles
    ADD CONSTRAINT managed_backup_profiles_agent_id_fkey FOREIGN KEY (agent_id) REFERENCES agent_identities(id) ON DELETE CASCADE;

ALTER TABLE ONLY managed_backup_profiles
    ADD CONSTRAINT managed_backup_profiles_policy_id_fkey FOREIGN KEY (policy_id) REFERENCES backup_policies(id) ON DELETE CASCADE;

ALTER TABLE ONLY mcp_oauth_codes
    ADD CONSTRAINT mcp_oauth_codes_client_id_fkey FOREIGN KEY (client_id) REFERENCES mcp_oauth_clients(id) ON DELETE CASCADE;

ALTER TABLE ONLY mcp_oauth_codes
    ADD CONSTRAINT mcp_oauth_codes_session_id_fkey FOREIGN KEY (session_id) REFERENCES user_sessions(id) ON DELETE CASCADE;

ALTER TABLE ONLY mcp_oauth_grants
    ADD CONSTRAINT mcp_oauth_grants_client_id_fkey FOREIGN KEY (client_id) REFERENCES mcp_oauth_clients(id) ON DELETE CASCADE;

ALTER TABLE ONLY mcp_oauth_grants
    ADD CONSTRAINT mcp_oauth_grants_session_id_fkey FOREIGN KEY (session_id) REFERENCES user_sessions(id) ON DELETE CASCADE;

ALTER TABLE ONLY mcp_oauth_refresh_tokens
    ADD CONSTRAINT mcp_oauth_refresh_tokens_grant_id_fkey FOREIGN KEY (grant_id) REFERENCES mcp_oauth_grants(id) ON DELETE CASCADE;

ALTER TABLE ONLY notification_deliveries
    ADD CONSTRAINT notification_deliveries_destination_id_fkey FOREIGN KEY (destination_id) REFERENCES notification_destinations(id) ON DELETE CASCADE;

ALTER TABLE ONLY notification_deliveries
    ADD CONSTRAINT notification_deliveries_event_id_fkey FOREIGN KEY (event_id) REFERENCES alert_events(id) ON DELETE CASCADE;

ALTER TABLE ONLY stack_bindings
    ADD CONSTRAINT stack_bindings_agent_id_fkey FOREIGN KEY (agent_id) REFERENCES agent_identities(id);

ALTER TABLE ONLY stack_deployment_previews
    ADD CONSTRAINT stack_deployment_previews_agent_id_fkey FOREIGN KEY (agent_id) REFERENCES agent_identities(id);

ALTER TABLE ONLY stack_deployment_previews
    ADD CONSTRAINT stack_deployment_previews_user_id_fkey FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE CASCADE;

ALTER TABLE ONLY stack_deployments
    ADD CONSTRAINT stack_deployments_actor_id_fkey FOREIGN KEY (actor_id) REFERENCES users(id) ON DELETE SET NULL;

ALTER TABLE ONLY stack_deployments
    ADD CONSTRAINT stack_deployments_agent_id_fkey FOREIGN KEY (agent_id) REFERENCES agent_identities(id);

ALTER TABLE ONLY stack_deployments
    ADD CONSTRAINT stack_deployments_stack_id_fkey FOREIGN KEY (stack_id) REFERENCES stack_bindings(stack_id);

ALTER TABLE ONLY stack_drafts
    ADD CONSTRAINT stack_drafts_user_id_fkey FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE CASCADE;

ALTER TABLE ONLY stack_github_app_setups
    ADD CONSTRAINT stack_github_app_setups_user_id_fkey FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE CASCADE;

ALTER TABLE ONLY stack_profile_bindings
    ADD CONSTRAINT stack_profile_bindings_agent_id_fkey FOREIGN KEY (agent_id) REFERENCES agent_identities(id);

ALTER TABLE ONLY telemetry_ingest_limits
    ADD CONSTRAINT telemetry_ingest_limits_agent_id_fkey FOREIGN KEY (agent_id) REFERENCES agent_identities(id) ON DELETE CASCADE;

ALTER TABLE ONLY telemetry_metric_rollups
    ADD CONSTRAINT telemetry_metric_rollups_agent_id_fkey FOREIGN KEY (agent_id) REFERENCES agent_identities(id) ON DELETE CASCADE;

ALTER TABLE ONLY telemetry_metric_series
    ADD CONSTRAINT telemetry_metric_series_agent_id_fkey FOREIGN KEY (agent_id) REFERENCES agent_identities(id) ON DELETE CASCADE;

ALTER TABLE ONLY telemetry_report_dedup
    ADD CONSTRAINT telemetry_report_dedup_agent_id_fkey FOREIGN KEY (agent_id) REFERENCES agent_identities(id) ON DELETE CASCADE;

ALTER TABLE ONLY user_sessions
    ADD CONSTRAINT user_sessions_user_id_fkey FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE CASCADE;

INSERT INTO app_setup (singleton_id) VALUES (1);
