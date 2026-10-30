-- Hikkari control baseline, 2026-10-30. Fresh installations only.
-- Fleet schema from Nekko @5b1ff719; messenger tables are intentionally absent.
-- Existing experimental Nekko schemas must use a separate, fresh database.
CREATE TABLE public.hub_accounts (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    name text NOT NULL UNIQUE CHECK (name ~ '^[a-z0-9_-]{1,32}$'),
    created_at timestamptz NOT NULL DEFAULT now()
);
CREATE FUNCTION public.admin_audit_log_append_only() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
    RAISE EXCEPTION 'admin_audit_log is append-only (% refused)', TG_OP
        USING ERRCODE = 'insufficient_privilege';
END;
$$;
CREATE TABLE public.admin_audit_log (
    id bigint NOT NULL,
    ts timestamp with time zone DEFAULT now() NOT NULL,
    admin_id uuid,
    action text NOT NULL,
    target_type text NOT NULL,
    target_id text DEFAULT ''::text NOT NULL,
    payload jsonb DEFAULT '{}'::jsonb NOT NULL,
    ip inet,
    user_agent text DEFAULT ''::text NOT NULL
);

CREATE SEQUENCE public.admin_audit_log_id_seq
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;

ALTER SEQUENCE public.admin_audit_log_id_seq OWNED BY public.admin_audit_log.id;

CREATE TABLE public.admin_ceremonies (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    admin_id uuid NOT NULL,
    purpose text NOT NULL,
    session_id uuid,
    data jsonb NOT NULL,
    expires_at timestamp with time zone NOT NULL,
    CONSTRAINT admin_ceremonies_purpose_check CHECK ((purpose = ANY (ARRAY['login'::text, 'enroll'::text, 'step_up'::text, 'add_key'::text])))
);

CREATE TABLE public.admin_passkeys (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    admin_id uuid NOT NULL,
    credential_id bytea NOT NULL,
    credential jsonb NOT NULL,
    name text DEFAULT ''::text NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    last_used_at timestamp with time zone
);

CREATE TABLE public.admin_recovery_codes (
    admin_id uuid NOT NULL,
    code_hash bytea NOT NULL,
    used_at timestamp with time zone
);

CREATE TABLE public.admin_sessions (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    admin_id uuid NOT NULL,
    public_key bytea NOT NULL,
    fingerprint_hash bytea NOT NULL,
    platform text DEFAULT 'unknown'::text NOT NULL,
    last_ip inet,
    last_user_agent text DEFAULT ''::text NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    last_used_at timestamp with time zone DEFAULT now() NOT NULL,
    expires_at timestamp with time zone NOT NULL,
    revoked_at timestamp with time zone,
    revoked_reason text,
    stepped_up_until timestamp with time zone
);

CREATE TABLE public.admins (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    login text NOT NULL,
    password_hash text NOT NULL,
    display_name text DEFAULT ''::text NOT NULL,
    is_disabled boolean DEFAULT false NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    last_login_at timestamp with time zone,
    last_login_ip inet,
    failed_count integer DEFAULT 0 NOT NULL,
    locked_until timestamp with time zone,
    role text DEFAULT 'support'::text NOT NULL,
    enroll_code_hash bytea,
    enroll_code_expires_at timestamp with time zone,
    audit_seen_id bigint DEFAULT 0 NOT NULL,
    CONSTRAINT admins_role_check CHECK ((role = ANY (ARRAY['owner'::text, 'support'::text])))
);

CREATE TABLE public.entry_bench_policy (
    id boolean DEFAULT true NOT NULL,
    min_throughput_pct integer DEFAULT 80 NOT NULL,
    max_rtt_growth_pct integer DEFAULT 130 NOT NULL,
    pairs integer DEFAULT 3 NOT NULL,
    run_seconds integer DEFAULT 300 NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_by text DEFAULT ''::text NOT NULL,
    CONSTRAINT entry_bench_policy_id_check CHECK (id),
    CONSTRAINT entry_bench_policy_max_rtt_growth_pct_check CHECK (((max_rtt_growth_pct >= 100) AND (max_rtt_growth_pct <= 300))),
    CONSTRAINT entry_bench_policy_min_throughput_pct_check CHECK (((min_throughput_pct >= 10) AND (min_throughput_pct <= 100))),
    CONSTRAINT entry_bench_policy_pairs_check CHECK (((pairs >= 1) AND (pairs <= 10))),
    CONSTRAINT entry_bench_policy_run_seconds_check CHECK (((run_seconds >= 60) AND (run_seconds <= 600)))
);

CREATE TABLE public.entry_box_orders (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    name text NOT NULL,
    provider text NOT NULL,
    provider_region text NOT NULL,
    plan text NOT NULL,
    monthly_cents integer NOT NULL,
    status text DEFAULT 'reserved'::text NOT NULL,
    instance_id text,
    error text DEFAULT ''::text NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    unseen_listings integer DEFAULT 0 NOT NULL,
    unseen_since timestamp with time zone,
    CONSTRAINT entry_box_orders_created_has_instance CHECK (((status <> ALL (ARRAY['created'::text, 'undeleted'::text, 'recorded'::text])) OR (instance_id IS NOT NULL))),
    CONSTRAINT entry_box_orders_instance_id_check CHECK ((instance_id <> ''::text)),
    CONSTRAINT entry_box_orders_monthly_cents_check CHECK ((monthly_cents > 0)),
    CONSTRAINT entry_box_orders_name_check CHECK (((name ~ '^[a-z]+[0-9]+$'::text) AND (length(name) <= 24))),
    CONSTRAINT entry_box_orders_plan_check CHECK ((plan <> ''::text)),
    CONSTRAINT entry_box_orders_provider_check CHECK ((provider <> ''::text)),
    CONSTRAINT entry_box_orders_provider_region_check CHECK ((provider_region <> ''::text)),
    CONSTRAINT entry_box_orders_status_check CHECK ((status = ANY (ARRAY['reserved'::text, 'created'::text, 'undeleted'::text, 'recorded'::text, 'released'::text, 'refused'::text]))),
    CONSTRAINT entry_box_orders_unseen_listings_check CHECK ((unseen_listings >= 0))
);

CREATE TABLE public.entry_boxes (
    name text NOT NULL,
    address_ct bytea NOT NULL,
    provider text NOT NULL,
    location text NOT NULL,
    rented_until date,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    ssh_user text DEFAULT 'root'::text NOT NULL,
    host_key text,
    arch text,
    edge_sha text,
    edge_seen_at timestamp with time zone,
    proto text NOT NULL,
    region text,
    retired_at timestamp with time zone,
    domain_ct bytea,
    provider_instance_id text,
    provider_region text DEFAULT ''::text NOT NULL,
    plan text DEFAULT ''::text NOT NULL,
    monthly_cents integer,
    retired_reason text,
    released_at timestamp with time zone,
    host_key_forgotten_at timestamp with time zone,
    handback_since timestamp with time zone,
    CONSTRAINT entry_boxes_arch_check CHECK ((arch = ANY (ARRAY['amd64'::text, 'arm64'::text]))),
    CONSTRAINT entry_boxes_domain_only_xhttp CHECK (((proto = 'vless-xhttp'::text) OR (domain_ct IS NULL))),
    CONSTRAINT entry_boxes_edge_sha_check CHECK ((edge_sha ~ '^[0-9a-f]{64}$'::text)),
    CONSTRAINT entry_boxes_edge_whole CHECK ((((edge_sha IS NULL) = (edge_seen_at IS NULL)) AND ((edge_sha IS NULL) OR (arch IS NOT NULL)))),
    CONSTRAINT entry_boxes_location_check CHECK ((location <> ''::text)),
    CONSTRAINT entry_boxes_monthly_cents_check CHECK (((monthly_cents IS NULL) OR (monthly_cents >= 0))),
    CONSTRAINT entry_boxes_name_check CHECK (((name ~ '^[a-z]+[0-9]+$'::text) AND (length(name) <= 24))),
    CONSTRAINT entry_boxes_proto_check CHECK ((proto = ANY (ARRAY['vless-reality-vision'::text, 'hysteria2'::text, 'vless-xhttp'::text]))),
    CONSTRAINT entry_boxes_provider_check CHECK ((provider <> ''::text)),
    CONSTRAINT entry_boxes_region_check CHECK ((region ~ '^[a-z0-9-]{1,32}$'::text)),
    CONSTRAINT entry_boxes_retired_has_reason CHECK (((retired_at IS NOT NULL) = (retired_reason IS NOT NULL))),
    CONSTRAINT entry_boxes_retired_reason_check CHECK ((retired_reason = ANY (ARRAY['ip_banned'::text, 'fingerprint'::text, 'destination'::text, 'provider_down'::text, 'rotated'::text, 'other'::text]))),
    CONSTRAINT entry_boxes_ssh_user_check CHECK ((ssh_user ~ '^[a-z_][a-z0-9_-]{0,31}$'::text)),
    CONSTRAINT entry_boxes_voice_has_region CHECK (((proto = 'hysteria2'::text) = (region IS NOT NULL)))
);

CREATE TABLE public.entry_cause_pauses (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    cause text NOT NULL,
    reason text DEFAULT ''::text NOT NULL,
    boxes integer DEFAULT 0 NOT NULL,
    paused_at timestamp with time zone DEFAULT now() NOT NULL,
    lifted_at timestamp with time zone,
    lifted_by text DEFAULT ''::text NOT NULL,
    CONSTRAINT entry_cause_pauses_boxes_check CHECK ((boxes >= 0)),
    CONSTRAINT entry_cause_pauses_cause_check CHECK ((cause <> ''::text)),
    CONSTRAINT entry_cause_pauses_lifted_has_time CHECK (((lifted_by = ''::text) OR (lifted_at IS NOT NULL)))
);

CREATE TABLE public.entry_detection_policy (
    id boolean DEFAULT true NOT NULL,
    fail_strikes integer DEFAULT 3 NOT NULL,
    wave_limit integer DEFAULT 3 NOT NULL,
    wave_window_seconds integer DEFAULT 3600 NOT NULL,
    verdict_window_seconds integer DEFAULT 180 NOT NULL,
    repair_cooldown_seconds integer DEFAULT 900 NOT NULL,
    require_two_vantages boolean DEFAULT false NOT NULL,
    ignore_network_down boolean DEFAULT true NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_by text DEFAULT ''::text NOT NULL,
    domain_probe_every_seconds integer DEFAULT 86400 NOT NULL,
    domain_return_clean integer DEFAULT 3 NOT NULL,
    domain_dead_fails integer DEFAULT 14 NOT NULL,
    domain_dead_after_seconds integer DEFAULT 2592000 NOT NULL,
    box_probe_every_seconds integer DEFAULT 86400 NOT NULL,
    box_return_clean integer DEFAULT 2 NOT NULL,
    box_dead_fails integer DEFAULT 2 NOT NULL,
    box_dead_after_seconds integer DEFAULT 259200 NOT NULL,
    cover_probe_every_seconds integer DEFAULT 43200 NOT NULL,
    cover_return_clean integer DEFAULT 3 NOT NULL,
    cover_dead_fails integer DEFAULT 20 NOT NULL,
    cover_dead_after_seconds integer DEFAULT 1209600 NOT NULL,
    CONSTRAINT entry_detection_policy_box_dead_after_seconds_check CHECK (((box_dead_after_seconds >= 86400) AND (box_dead_after_seconds <= 31536000))),
    CONSTRAINT entry_detection_policy_box_dead_fails_check CHECK (((box_dead_fails >= 1) AND (box_dead_fails <= 60))),
    CONSTRAINT entry_detection_policy_box_deadline_leaves_a_return CHECK (((box_dead_after_seconds)::bigint >= ((box_probe_every_seconds)::bigint * box_return_clean))),
    CONSTRAINT entry_detection_policy_box_probe_every_seconds_check CHECK (((box_probe_every_seconds >= 3600) AND (box_probe_every_seconds <= 604800))),
    CONSTRAINT entry_detection_policy_box_return_clean_check CHECK (((box_return_clean >= 2) AND (box_return_clean <= 14))),
    CONSTRAINT entry_detection_policy_box_return_spans_a_day CHECK ((((box_probe_every_seconds)::bigint * (box_return_clean - 1)) >= 72000)),
    CONSTRAINT entry_detection_policy_cover_dead_after_seconds_check CHECK (((cover_dead_after_seconds >= 86400) AND (cover_dead_after_seconds <= 31536000))),
    CONSTRAINT entry_detection_policy_cover_dead_fails_check CHECK (((cover_dead_fails >= 1) AND (cover_dead_fails <= 60))),
    CONSTRAINT entry_detection_policy_cover_deadline_leaves_a_return CHECK (((cover_dead_after_seconds)::bigint >= ((cover_probe_every_seconds)::bigint * cover_return_clean))),
    CONSTRAINT entry_detection_policy_cover_probe_every_seconds_check CHECK (((cover_probe_every_seconds >= 3600) AND (cover_probe_every_seconds <= 604800))),
    CONSTRAINT entry_detection_policy_cover_return_clean_check CHECK (((cover_return_clean >= 2) AND (cover_return_clean <= 14))),
    CONSTRAINT entry_detection_policy_cover_return_spans_a_day CHECK ((((cover_probe_every_seconds)::bigint * (cover_return_clean - 1)) >= 72000)),
    CONSTRAINT entry_detection_policy_domain_dead_after_seconds_check CHECK (((domain_dead_after_seconds >= 86400) AND (domain_dead_after_seconds <= 31536000))),
    CONSTRAINT entry_detection_policy_domain_dead_fails_check CHECK (((domain_dead_fails >= 1) AND (domain_dead_fails <= 60))),
    CONSTRAINT entry_detection_policy_domain_deadline_leaves_a_return CHECK (((domain_dead_after_seconds)::bigint >= ((domain_probe_every_seconds)::bigint * domain_return_clean))),
    CONSTRAINT entry_detection_policy_domain_probe_every_seconds_check CHECK (((domain_probe_every_seconds >= 3600) AND (domain_probe_every_seconds <= 604800))),
    CONSTRAINT entry_detection_policy_domain_return_clean_check CHECK (((domain_return_clean >= 2) AND (domain_return_clean <= 14))),
    CONSTRAINT entry_detection_policy_domain_return_spans_a_day CHECK ((((domain_probe_every_seconds)::bigint * (domain_return_clean - 1)) >= 72000)),
    CONSTRAINT entry_detection_policy_fail_strikes_check CHECK (((fail_strikes >= 1) AND (fail_strikes <= 10))),
    CONSTRAINT entry_detection_policy_id_check CHECK (id),
    CONSTRAINT entry_detection_policy_ignore_network_down_check CHECK (ignore_network_down),
    CONSTRAINT entry_detection_policy_repair_cooldown_seconds_check CHECK (((repair_cooldown_seconds >= 60) AND (repair_cooldown_seconds <= 86400))),
    CONSTRAINT entry_detection_policy_verdict_window_seconds_check CHECK (((verdict_window_seconds >= 60) AND (verdict_window_seconds <= 3600))),
    CONSTRAINT entry_detection_policy_wave_limit_check CHECK (((wave_limit >= 1) AND (wave_limit <= 20))),
    CONSTRAINT entry_detection_policy_wave_window_seconds_check CHECK (((wave_window_seconds >= 60) AND (wave_window_seconds <= 86400)))
);

CREATE TABLE public.entry_domain_exclusions (
    domain text NOT NULL,
    note text DEFAULT ''::text NOT NULL,
    created_by uuid,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT entry_domain_exclusions_domain_check CHECK (((domain = lower(domain)) AND (domain <> ''::text)))
);

CREATE TABLE public.entry_domains (
    domain text NOT NULL,
    tld text NOT NULL,
    status text NOT NULL,
    box text,
    source text NOT NULL,
    cost_cents integer DEFAULT 0 NOT NULL,
    order_id text DEFAULT ''::text NOT NULL,
    idempotency_key text DEFAULT ''::text NOT NULL,
    error text DEFAULT ''::text NOT NULL,
    bought_at timestamp with time zone,
    assigned_at timestamp with time zone,
    burned_at timestamp with time zone,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT entry_domains_cost_cents_check CHECK ((cost_cents >= 0)),
    CONSTRAINT entry_domains_source_check CHECK ((source = ANY (ARRAY['purchased'::text, 'seeded'::text]))),
    CONSTRAINT entry_domains_status_check CHECK ((status = ANY (ARRAY['buying'::text, 'unknown'::text, 'owned'::text, 'assigned'::text, 'burned'::text, 'dead'::text, 'failed'::text])))
);

CREATE TABLE public.entry_fail_reports (
    id bigint NOT NULL,
    account_id uuid NOT NULL,
    relay_id bigint NOT NULL,
    stage text NOT NULL,
    observed_at timestamp with time zone NOT NULL,
    received_at timestamp with time zone DEFAULT now() NOT NULL,
    meta_ct bytea,
    CONSTRAINT entry_fail_reports_stage_check CHECK ((stage = ANY (ARRAY['tcp'::text, 'tls'::text, 'reality'::text, 'websocket'::text, 'timeout'::text])))
);

CREATE SEQUENCE public.entry_fail_reports_id_seq
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;

ALTER SEQUENCE public.entry_fail_reports_id_seq OWNED BY public.entry_fail_reports.id;

CREATE TABLE public.entry_fleet_pools (
    proto text NOT NULL,
    voice_region text DEFAULT ''::text NOT NULL,
    CONSTRAINT entry_fleet_pools_voice_has_region CHECK (((proto = 'hysteria2'::text) = (voice_region <> ''::text))),
    CONSTRAINT entry_fleet_targets_proto_check CHECK ((proto = ANY (ARRAY['vless-reality-vision'::text, 'hysteria2'::text, 'vless-xhttp'::text]))),
    CONSTRAINT entry_fleet_targets_voice_region_check CHECK (((voice_region = ''::text) OR (voice_region ~ '^[a-z0-9-]{1,32}$'::text)))
);

CREATE TABLE public.entry_grants (
    id bigint NOT NULL,
    account_id uuid NOT NULL,
    relay_id bigint NOT NULL,
    trigger text NOT NULL,
    granted_at timestamp with time zone DEFAULT now() NOT NULL,
    revoked_at timestamp with time zone,
    revoke_reason text,
    meta_ct bytea,
    CONSTRAINT entry_grants_revoke_reason_check CHECK ((revoke_reason = ANY (ARRAY['relay_dead'::text, 'drained'::text, 'suspect'::text]))),
    CONSTRAINT entry_grants_revoked_has_reason CHECK (((revoked_at IS NULL) = (revoke_reason IS NULL))),
    CONSTRAINT entry_grants_trigger_check CHECK ((trigger = ANY (ARRAY['initial'::text, 'death_replacement'::text, 'drain_replacement'::text])))
);

CREATE SEQUENCE public.entry_grants_id_seq
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;

ALTER SEQUENCE public.entry_grants_id_seq OWNED BY public.entry_grants.id;

CREATE TABLE public.entry_hub_ssh (
    id boolean DEFAULT true NOT NULL,
    private_ct bytea NOT NULL,
    public_key text NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT entry_hub_ssh_id_check CHECK (id)
);

CREATE TABLE public.entry_incidents (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    box text,
    cause text NOT NULL,
    status text DEFAULT 'open'::text NOT NULL,
    playbook text DEFAULT ''::text NOT NULL,
    decided_by text DEFAULT ''::text NOT NULL,
    advice text DEFAULT ''::text NOT NULL,
    evidence jsonb DEFAULT '{}'::jsonb NOT NULL,
    cost_cents integer DEFAULT 0 NOT NULL,
    outcome text DEFAULT ''::text NOT NULL,
    opened_at timestamp with time zone DEFAULT now() NOT NULL,
    acted_at timestamp with time zone,
    closed_at timestamp with time zone,
    handoff text DEFAULT ''::text NOT NULL,
    admitted_at timestamp with time zone,
    replaced_by text,
    pool text,
    CONSTRAINT entry_incidents_admitted_after_close CHECK (((admitted_at IS NULL) OR (closed_at IS NOT NULL))),
    CONSTRAINT entry_incidents_cause_check CHECK ((cause <> ''::text)),
    CONSTRAINT entry_incidents_closed_has_time CHECK (((status = ANY (ARRAY['resolved'::text, 'dismissed'::text])) = (closed_at IS NOT NULL))),
    CONSTRAINT entry_incidents_cost_cents_check CHECK ((cost_cents >= 0)),
    CONSTRAINT entry_incidents_decided_by_check CHECK ((decided_by = ANY (ARRAY[''::text, 'auto'::text, 'ask'::text, 'manual'::text]))),
    CONSTRAINT entry_incidents_one_subject CHECK (((box IS NULL) <> (pool IS NULL))),
    CONSTRAINT entry_incidents_pool_check CHECK ((pool <> ''::text)),
    CONSTRAINT entry_incidents_status_check CHECK ((status = ANY (ARRAY['open'::text, 'acting'::text, 'resolved'::text, 'dismissed'::text])))
);

CREATE TABLE public.entry_job_log (
    job_id uuid NOT NULL,
    seq integer NOT NULL,
    at timestamp with time zone DEFAULT now() NOT NULL,
    line text NOT NULL
);

CREATE TABLE public.entry_jobs (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    box text NOT NULL,
    kind text NOT NULL,
    status text DEFAULT 'running'::text NOT NULL,
    step text DEFAULT ''::text NOT NULL,
    error text,
    admin_id uuid,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    finished_at timestamp with time zone,
    rollout_id uuid,
    CONSTRAINT entry_jobs_failed_says_why CHECK (((status = 'failed'::text) = (error IS NOT NULL))),
    CONSTRAINT entry_jobs_finished CHECK (((status = 'running'::text) = (finished_at IS NULL))),
    CONSTRAINT entry_jobs_kind_check CHECK ((kind = ANY (ARRAY['provision'::text, 'canary'::text, 'update'::text, 'retire'::text, 'rekey'::text, 'reboot'::text, 'restart'::text, 'redest'::text, 'prober'::text]))),
    CONSTRAINT entry_jobs_rollout_kind CHECK (((kind = ANY (ARRAY['provision'::text, 'retire'::text, 'rekey'::text, 'reboot'::text, 'restart'::text, 'redest'::text, 'prober'::text])) = (rollout_id IS NULL))),
    CONSTRAINT entry_jobs_status_check CHECK ((status = ANY (ARRAY['running'::text, 'done'::text, 'failed'::text])))
);

CREATE TABLE public.entry_playbook_modes (
    cause text NOT NULL,
    mode text NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_by text DEFAULT ''::text NOT NULL,
    CONSTRAINT entry_playbook_modes_cause_check CHECK ((cause <> ''::text)),
    CONSTRAINT entry_playbook_modes_mode_check CHECK ((mode = ANY (ARRAY['auto'::text, 'ask'::text, 'off'::text])))
);

CREATE TABLE public.entry_probe_tasks (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    job_id uuid,
    relays text[] NOT NULL,
    status text DEFAULT 'queued'::text NOT NULL,
    claimed_by text,
    verdicts jsonb,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    claimed_at timestamp with time zone,
    done_at timestamp with time zone,
    kind text DEFAULT 'admission'::text NOT NULL,
    params jsonb,
    result jsonb,
    CONSTRAINT entry_probe_tasks_bench_has_result CHECK (((kind <> 'bench'::text) OR ((status = 'done'::text) = (result IS NOT NULL)))),
    CONSTRAINT entry_probe_tasks_bench_relays CHECK (((kind <> 'bench'::text) OR ((cardinality(relays) >= 1) AND (cardinality(relays) <= 2)))),
    CONSTRAINT entry_probe_tasks_domain_shape CHECK (((kind <> 'domain'::text) OR ((cardinality(relays) = 1) AND (jsonb_typeof((params -> 'domains'::text)) = 'array'::text)))),
    CONSTRAINT entry_probe_tasks_done_has_verdicts CHECK (((status = 'done'::text) = (verdicts IS NOT NULL))),
    CONSTRAINT entry_probe_tasks_kind_check CHECK ((kind = ANY (ARRAY['admission'::text, 'bench'::text, 'domain'::text, 'box'::text]))),
    CONSTRAINT entry_probe_tasks_relays_check CHECK ((cardinality(relays) > 0)),
    CONSTRAINT entry_probe_tasks_status_check CHECK ((status = ANY (ARRAY['queued'::text, 'claimed'::text, 'done'::text])))
);

CREATE TABLE public.entry_providers (
    kind text NOT NULL,
    enabled boolean DEFAULT false NOT NULL,
    creds_ct bytea,
    creds_hint text DEFAULT ''::text NOT NULL,
    tlds text[] DEFAULT '{}'::text[] NOT NULL,
    checked_at timestamp with time zone,
    check_ok boolean,
    check_error text DEFAULT ''::text NOT NULL,
    balance_cents integer,
    account_spent_cents integer,
    account_limit_cents integer,
    updated_by uuid,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    default_region text DEFAULT ''::text NOT NULL,
    default_plan text DEFAULT ''::text NOT NULL,
    CONSTRAINT entry_providers_enabled_has_creds CHECK (((NOT enabled) OR (creds_ct IS NOT NULL))),
    CONSTRAINT entry_providers_kind_check CHECK ((kind ~ '^[a-z][a-z0-9]*$'::text))
);

CREATE TABLE public.entry_relays (
    id bigint NOT NULL,
    name text NOT NULL,
    kind text NOT NULL,
    status text DEFAULT 'pending'::text NOT NULL,
    key_id text,
    envelope_ct bytea,
    issued_at timestamp with time zone,
    expires_at timestamp with time zone,
    signed_at timestamp with time zone,
    died_at timestamp with time zone,
    box text NOT NULL,
    identity_ct bytea NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    died_reason text,
    CONSTRAINT entry_relays_alive_has_record CHECK (((status <> ALL (ARRAY['alive'::text, 'testing'::text, 'draining'::text, 'suspect'::text])) OR (envelope_ct IS NOT NULL))),
    CONSTRAINT entry_relays_dead_has_reason CHECK (((status = 'dead'::text) = (died_reason IS NOT NULL))),
    CONSTRAINT entry_relays_dead_has_time CHECK (((status = 'dead'::text) = (died_at IS NOT NULL))),
    CONSTRAINT entry_relays_died_reason_check CHECK ((died_reason = ANY (ARRAY['ip_banned'::text, 'fingerprint'::text, 'destination'::text, 'provider_down'::text, 'rotated'::text, 'other'::text, 'domain_blocked'::text]))),
    CONSTRAINT entry_relays_kind_check CHECK ((kind = ANY (ARRAY['bootstrap'::text, 'personal'::text]))),
    CONSTRAINT entry_relays_lifetime CHECK ((expires_at > issued_at)),
    CONSTRAINT entry_relays_name_check CHECK ((name ~ '^[a-z0-9-]{1,32}$'::text)),
    CONSTRAINT entry_relays_record_whole CHECK ((((envelope_ct IS NULL) = (key_id IS NULL)) AND ((envelope_ct IS NULL) = (issued_at IS NULL)) AND ((envelope_ct IS NULL) = (expires_at IS NULL)) AND ((envelope_ct IS NULL) = (signed_at IS NULL)))),
    CONSTRAINT entry_relays_status_check CHECK ((status = ANY (ARRAY['pending'::text, 'alive'::text, 'testing'::text, 'draining'::text, 'suspect'::text, 'dead'::text])))
);

CREATE SEQUENCE public.entry_relays_id_seq
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;

ALTER SEQUENCE public.entry_relays_id_seq OWNED BY public.entry_relays.id;

CREATE TABLE public.entry_repairs (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    incident_id uuid NOT NULL,
    box text,
    cause text NOT NULL,
    playbook text NOT NULL,
    decided_by text NOT NULL,
    status text DEFAULT 'running'::text NOT NULL,
    job_id uuid,
    error text DEFAULT ''::text NOT NULL,
    cost_cents integer DEFAULT 0 NOT NULL,
    started_at timestamp with time zone DEFAULT now() NOT NULL,
    finished_at timestamp with time zone,
    pool text,
    CONSTRAINT entry_repairs_cause_check CHECK ((cause <> ''::text)),
    CONSTRAINT entry_repairs_cost_cents_check CHECK ((cost_cents >= 0)),
    CONSTRAINT entry_repairs_decided_by_check CHECK ((decided_by = ANY (ARRAY['auto'::text, 'ask'::text, 'manual'::text]))),
    CONSTRAINT entry_repairs_finished_has_time CHECK (((status = ANY (ARRAY['done'::text, 'failed'::text, 'held'::text])) = (finished_at IS NOT NULL))),
    CONSTRAINT entry_repairs_one_subject CHECK (((box IS NULL) <> (pool IS NULL))),
    CONSTRAINT entry_repairs_playbook_check CHECK ((playbook <> ''::text)),
    CONSTRAINT entry_repairs_pool_check CHECK ((pool <> ''::text)),
    CONSTRAINT entry_repairs_status_check CHECK ((status = ANY (ARRAY['running'::text, 'done'::text, 'failed'::text, 'held'::text])))
);

CREATE TABLE public.entry_rollouts (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    arch text NOT NULL,
    edge_sha text NOT NULL,
    stage text DEFAULT 'canary'::text NOT NULL,
    canary_box text,
    error text,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    finished_at timestamp with time zone,
    CONSTRAINT entry_rollouts_arch_check CHECK ((arch = ANY (ARRAY['amd64'::text, 'arm64'::text]))),
    CONSTRAINT entry_rollouts_canary_named CHECK (((stage <> ALL (ARRAY['rolling'::text, 'done'::text])) OR (canary_box IS NOT NULL))),
    CONSTRAINT entry_rollouts_edge_sha_check CHECK ((edge_sha ~ '^[0-9a-f]{64}$'::text)),
    CONSTRAINT entry_rollouts_failed_says_why CHECK (((stage = ANY (ARRAY['failed'::text, 'stopped'::text])) = (error IS NOT NULL))),
    CONSTRAINT entry_rollouts_finished CHECK (((stage = ANY (ARRAY['canary'::text, 'rolling'::text])) = (finished_at IS NULL))),
    CONSTRAINT entry_rollouts_stage_check CHECK ((stage = ANY (ARRAY['canary'::text, 'rolling'::text, 'done'::text, 'failed'::text, 'stopped'::text])))
);

CREATE TABLE public.entry_sources (
    id boolean DEFAULT true NOT NULL,
    key_id text NOT NULL,
    envelope bytea NOT NULL,
    issued_at timestamp with time zone NOT NULL,
    expires_at timestamp with time zone NOT NULL,
    imported_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT entry_sources_id_check CHECK ((id = true)),
    CONSTRAINT entry_sources_lifetime CHECK ((expires_at > issued_at))
);

CREATE TABLE public.entry_spend_caps (
    role text NOT NULL,
    daily_cap_cents integer NOT NULL,
    monthly_cap_cents integer NOT NULL,
    updated_by uuid,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT entry_spend_caps_daily_cap_cents_check CHECK ((daily_cap_cents >= 0)),
    CONSTRAINT entry_spend_caps_month_covers_day CHECK ((monthly_cap_cents >= daily_cap_cents)),
    CONSTRAINT entry_spend_caps_monthly_cap_cents_check CHECK ((monthly_cap_cents >= 0)),
    CONSTRAINT entry_spend_caps_role_check CHECK ((role = ANY (ARRAY['hosting'::text, 'registrar'::text])))
);

CREATE TABLE public.entry_suspects (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    kind text NOT NULL,
    subject text NOT NULL,
    reason text NOT NULL,
    status text DEFAULT 'suspect'::text NOT NULL,
    since timestamp with time zone DEFAULT now() NOT NULL,
    last_probe_at timestamp with time zone,
    clean_streak integer DEFAULT 0 NOT NULL,
    failed_probes integer DEFAULT 0 NOT NULL,
    ended_at timestamp with time zone,
    outcome text DEFAULT ''::text NOT NULL,
    CONSTRAINT entry_suspects_clean_streak_check CHECK ((clean_streak >= 0)),
    CONSTRAINT entry_suspects_ended CHECK (((status = 'suspect'::text) = (ended_at IS NULL))),
    CONSTRAINT entry_suspects_failed_probes_check CHECK ((failed_probes >= 0)),
    CONSTRAINT entry_suspects_kind_check CHECK ((kind = ANY (ARRAY['domain'::text, 'box'::text, 'cover'::text]))),
    CONSTRAINT entry_suspects_reason_check CHECK ((reason <> ''::text)),
    CONSTRAINT entry_suspects_status_check CHECK ((status = ANY (ARRAY['suspect'::text, 'returned'::text, 'dead'::text, 'withdrawn'::text]))),
    CONSTRAINT entry_suspects_subject_check CHECK ((subject <> ''::text))
);

CREATE TABLE public.entry_vantages (
    slot text NOT NULL,
    title text DEFAULT ''::text NOT NULL,
    box text,
    host_ct bytea,
    provider text DEFAULT ''::text NOT NULL,
    status text DEFAULT 'active'::text NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    retired_at timestamp with time zone,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_by text DEFAULT ''::text NOT NULL,
    token_hash bytea,
    last_admission_poll_at timestamp with time zone,
    CONSTRAINT entry_vantages_retired_check CHECK (((status = 'retired'::text) = (retired_at IS NOT NULL))),
    CONSTRAINT entry_vantages_retired_mute CHECK (((status = 'active'::text) OR (token_hash IS NULL))),
    CONSTRAINT entry_vantages_slot_check CHECK ((slot ~ '^[a-z0-9-]{1,32}$'::text)),
    CONSTRAINT entry_vantages_status_check CHECK ((status = ANY (ARRAY['active'::text, 'retired'::text]))),
    CONSTRAINT entry_vantages_token_hash_check CHECK (((token_hash IS NULL) OR (octet_length(token_hash) = 32)))
);

CREATE TABLE public.hub_subscriptions (
    token_hash bytea NOT NULL,
    account_id uuid NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    revoked_at timestamp with time zone,
    CONSTRAINT hub_subscriptions_token_hash_check CHECK ((length(token_hash) = 32))
);

ALTER TABLE ONLY public.admin_audit_log ALTER COLUMN id SET DEFAULT nextval('public.admin_audit_log_id_seq'::regclass);

ALTER TABLE ONLY public.entry_fail_reports ALTER COLUMN id SET DEFAULT nextval('public.entry_fail_reports_id_seq'::regclass);

ALTER TABLE ONLY public.entry_grants ALTER COLUMN id SET DEFAULT nextval('public.entry_grants_id_seq'::regclass);

ALTER TABLE ONLY public.entry_relays ALTER COLUMN id SET DEFAULT nextval('public.entry_relays_id_seq'::regclass);

ALTER TABLE ONLY public.admin_audit_log
    ADD CONSTRAINT admin_audit_log_pkey PRIMARY KEY (id);

ALTER TABLE ONLY public.admin_ceremonies
    ADD CONSTRAINT admin_ceremonies_pkey PRIMARY KEY (id);

ALTER TABLE ONLY public.admin_passkeys
    ADD CONSTRAINT admin_passkeys_credential_id_key UNIQUE (credential_id);

ALTER TABLE ONLY public.admin_passkeys
    ADD CONSTRAINT admin_passkeys_pkey PRIMARY KEY (id);

ALTER TABLE ONLY public.admin_recovery_codes
    ADD CONSTRAINT admin_recovery_codes_pkey PRIMARY KEY (admin_id, code_hash);

ALTER TABLE ONLY public.admin_sessions
    ADD CONSTRAINT admin_sessions_pkey PRIMARY KEY (id);

ALTER TABLE ONLY public.admins
    ADD CONSTRAINT admins_pkey PRIMARY KEY (id);

ALTER TABLE ONLY public.entry_bench_policy
    ADD CONSTRAINT entry_bench_policy_pkey PRIMARY KEY (id);

ALTER TABLE ONLY public.entry_box_orders
    ADD CONSTRAINT entry_box_orders_pkey PRIMARY KEY (id);

ALTER TABLE ONLY public.entry_boxes
    ADD CONSTRAINT entry_boxes_pkey PRIMARY KEY (name);

ALTER TABLE ONLY public.entry_cause_pauses
    ADD CONSTRAINT entry_cause_pauses_pkey PRIMARY KEY (id);

ALTER TABLE ONLY public.entry_detection_policy
    ADD CONSTRAINT entry_detection_policy_pkey PRIMARY KEY (id);

ALTER TABLE ONLY public.entry_domain_exclusions
    ADD CONSTRAINT entry_domain_exclusions_pkey PRIMARY KEY (domain);

ALTER TABLE ONLY public.entry_domains
    ADD CONSTRAINT entry_domains_pkey PRIMARY KEY (domain);

ALTER TABLE ONLY public.entry_fail_reports
    ADD CONSTRAINT entry_fail_reports_pkey PRIMARY KEY (id);

ALTER TABLE ONLY public.entry_fleet_pools
    ADD CONSTRAINT entry_fleet_pools_pkey PRIMARY KEY (proto, voice_region);

ALTER TABLE ONLY public.entry_grants
    ADD CONSTRAINT entry_grants_pkey PRIMARY KEY (id);

ALTER TABLE ONLY public.entry_hub_ssh
    ADD CONSTRAINT entry_hub_ssh_pkey PRIMARY KEY (id);

ALTER TABLE ONLY public.entry_incidents
    ADD CONSTRAINT entry_incidents_pkey PRIMARY KEY (id);

ALTER TABLE ONLY public.entry_job_log
    ADD CONSTRAINT entry_job_log_pkey PRIMARY KEY (job_id, seq);

ALTER TABLE ONLY public.entry_jobs
    ADD CONSTRAINT entry_jobs_pkey PRIMARY KEY (id);

ALTER TABLE ONLY public.entry_playbook_modes
    ADD CONSTRAINT entry_playbook_modes_pkey PRIMARY KEY (cause);

ALTER TABLE ONLY public.entry_probe_tasks
    ADD CONSTRAINT entry_probe_tasks_pkey PRIMARY KEY (id);

ALTER TABLE ONLY public.entry_providers
    ADD CONSTRAINT entry_providers_pkey PRIMARY KEY (kind);

ALTER TABLE ONLY public.entry_relays
    ADD CONSTRAINT entry_relays_name_key UNIQUE (name);

ALTER TABLE ONLY public.entry_relays
    ADD CONSTRAINT entry_relays_pkey PRIMARY KEY (id);

ALTER TABLE ONLY public.entry_repairs
    ADD CONSTRAINT entry_repairs_pkey PRIMARY KEY (id);

ALTER TABLE ONLY public.entry_rollouts
    ADD CONSTRAINT entry_rollouts_one_per_build UNIQUE (arch, edge_sha);

ALTER TABLE ONLY public.entry_rollouts
    ADD CONSTRAINT entry_rollouts_pkey PRIMARY KEY (id);

ALTER TABLE ONLY public.entry_sources
    ADD CONSTRAINT entry_sources_pkey PRIMARY KEY (id);

ALTER TABLE ONLY public.entry_spend_caps
    ADD CONSTRAINT entry_spend_caps_pkey PRIMARY KEY (role);

ALTER TABLE ONLY public.entry_suspects
    ADD CONSTRAINT entry_suspects_pkey PRIMARY KEY (id);

ALTER TABLE ONLY public.entry_vantages
    ADD CONSTRAINT entry_vantages_pkey PRIMARY KEY (slot);

ALTER TABLE ONLY public.entry_vantages
    ADD CONSTRAINT entry_vantages_token_hash_key UNIQUE (token_hash);

ALTER TABLE ONLY public.hub_subscriptions
    ADD CONSTRAINT hub_subscriptions_pkey PRIMARY KEY (token_hash);

CREATE INDEX admin_ceremonies_expires_idx ON public.admin_ceremonies USING btree (expires_at);

CREATE INDEX admin_passkeys_admin_idx ON public.admin_passkeys USING btree (admin_id);

CREATE INDEX admin_sessions_admin_idx ON public.admin_sessions USING btree (admin_id) WHERE (revoked_at IS NULL);

CREATE INDEX admin_sessions_expires_idx ON public.admin_sessions USING btree (expires_at) WHERE (revoked_at IS NULL);

CREATE UNIQUE INDEX admins_login_key ON public.admins USING btree (lower(login));

CREATE UNIQUE INDEX entry_box_orders_name_in_flight ON public.entry_box_orders USING btree (name) WHERE (status = ANY (ARRAY['reserved'::text, 'created'::text, 'undeleted'::text]));

CREATE INDEX entry_box_orders_open ON public.entry_box_orders USING btree (status, updated_at) WHERE (status = ANY (ARRAY['reserved'::text, 'created'::text, 'undeleted'::text]));

CREATE INDEX entry_box_orders_recent ON public.entry_box_orders USING btree (created_at) WHERE (status <> 'refused'::text);

CREATE UNIQUE INDEX entry_boxes_one_box_per_instance ON public.entry_boxes USING btree (provider_instance_id) WHERE (provider_instance_id IS NOT NULL);

CREATE INDEX entry_boxes_provider_life ON public.entry_boxes USING btree (provider, created_at, retired_at);

CREATE INDEX entry_boxes_undeleted ON public.entry_boxes USING btree (retired_at) WHERE ((retired_at IS NOT NULL) AND (provider_instance_id IS NOT NULL) AND (released_at IS NULL));

CREATE INDEX entry_cause_pauses_by_cause ON public.entry_cause_pauses USING btree (cause, paused_at DESC);

CREATE UNIQUE INDEX entry_cause_pauses_one_open ON public.entry_cause_pauses USING btree (cause) WHERE (lifted_at IS NULL);

CREATE UNIQUE INDEX entry_domains_idem ON public.entry_domains USING btree (idempotency_key) WHERE (idempotency_key <> ''::text);

CREATE INDEX entry_domains_pool ON public.entry_domains USING btree (tld) WHERE (status = 'owned'::text);

CREATE INDEX entry_domains_spend ON public.entry_domains USING btree (created_at) WHERE ((source = 'purchased'::text) AND (status <> 'failed'::text));

CREATE INDEX entry_fail_reports_by_relay ON public.entry_fail_reports USING btree (relay_id, received_at);

CREATE INDEX entry_fail_reports_repeat ON public.entry_fail_reports USING btree (account_id, relay_id, stage, received_at);

CREATE UNIQUE INDEX entry_grants_active ON public.entry_grants USING btree (account_id, relay_id) WHERE (revoked_at IS NULL);

CREATE INDEX entry_grants_active_by_relay ON public.entry_grants USING btree (relay_id) WHERE (revoked_at IS NULL);

CREATE INDEX entry_incidents_by_cause ON public.entry_incidents USING btree (cause, opened_at DESC);

CREATE INDEX entry_incidents_feed ON public.entry_incidents USING btree (opened_at DESC);

CREATE UNIQUE INDEX entry_incidents_one_open ON public.entry_incidents USING btree (box, cause) WHERE (status = ANY (ARRAY['open'::text, 'acting'::text]));

CREATE UNIQUE INDEX entry_incidents_one_open_pool ON public.entry_incidents USING btree (pool, cause) WHERE (status = ANY (ARRAY['open'::text, 'acting'::text]));

CREATE INDEX entry_incidents_replaced_by ON public.entry_incidents USING btree (replaced_by) WHERE (replaced_by IS NOT NULL);

CREATE INDEX entry_jobs_by_box ON public.entry_jobs USING btree (box, created_at DESC);

CREATE INDEX entry_jobs_by_rollout ON public.entry_jobs USING btree (rollout_id, created_at) WHERE (rollout_id IS NOT NULL);

CREATE UNIQUE INDEX entry_jobs_one_running_per_box ON public.entry_jobs USING btree (box) WHERE (status = 'running'::text);

CREATE INDEX entry_probe_tasks_queued ON public.entry_probe_tasks USING btree (kind, created_at) WHERE (status = 'queued'::text);

CREATE INDEX entry_relays_by_box ON public.entry_relays USING btree (box);

CREATE INDEX entry_repairs_box_cause ON public.entry_repairs USING btree (box, cause, started_at DESC);

CREATE INDEX entry_repairs_cause_window ON public.entry_repairs USING btree (cause, started_at DESC);

CREATE UNIQUE INDEX entry_repairs_one_buy_domain ON public.entry_repairs USING btree (playbook) WHERE ((status = 'running'::text) AND (playbook = 'buy_domain'::text));

CREATE UNIQUE INDEX entry_repairs_one_running ON public.entry_repairs USING btree (box) WHERE (status = 'running'::text);

CREATE UNIQUE INDEX entry_repairs_one_scale_up ON public.entry_repairs USING btree (playbook) WHERE ((status = 'running'::text) AND (playbook = 'scale_up'::text));

CREATE INDEX entry_repairs_pool_cause ON public.entry_repairs USING btree (pool, cause, started_at DESC) WHERE (pool IS NOT NULL);

CREATE UNIQUE INDEX entry_rollouts_one_moving_per_arch ON public.entry_rollouts USING btree (arch) WHERE (stage = ANY (ARRAY['canary'::text, 'rolling'::text]));

CREATE UNIQUE INDEX entry_suspects_dead ON public.entry_suspects USING btree (kind, subject) WHERE (status = 'dead'::text);

CREATE INDEX entry_suspects_due ON public.entry_suspects USING btree (kind, COALESCE(last_probe_at, since)) WHERE (status = 'suspect'::text);

CREATE UNIQUE INDEX entry_suspects_open ON public.entry_suspects USING btree (kind, subject) WHERE (status = 'suspect'::text);

CREATE INDEX hub_subscriptions_account ON public.hub_subscriptions USING btree (account_id);

CREATE INDEX idx_admin_audit_action_ts ON public.admin_audit_log USING btree (action, ts DESC);

CREATE INDEX idx_admin_audit_admin_ts ON public.admin_audit_log USING btree (admin_id, ts DESC);

CREATE INDEX idx_admin_audit_target ON public.admin_audit_log USING btree (target_type, target_id, ts DESC);

CREATE INDEX idx_admin_audit_ts ON public.admin_audit_log USING btree (ts DESC);

CREATE INDEX idx_entry_vantages_status ON public.entry_vantages USING btree (status);

CREATE TRIGGER admin_audit_log_no_truncate BEFORE TRUNCATE ON public.admin_audit_log FOR EACH STATEMENT EXECUTE FUNCTION public.admin_audit_log_append_only();

CREATE TRIGGER admin_audit_log_no_update_delete BEFORE DELETE OR UPDATE ON public.admin_audit_log FOR EACH ROW EXECUTE FUNCTION public.admin_audit_log_append_only();

ALTER TABLE ONLY public.admin_audit_log
    ADD CONSTRAINT admin_audit_log_admin_id_fkey FOREIGN KEY (admin_id) REFERENCES public.admins(id) ON DELETE SET NULL;

ALTER TABLE ONLY public.admin_ceremonies
    ADD CONSTRAINT admin_ceremonies_admin_id_fkey FOREIGN KEY (admin_id) REFERENCES public.admins(id) ON DELETE CASCADE;

ALTER TABLE ONLY public.admin_ceremonies
    ADD CONSTRAINT admin_ceremonies_session_id_fkey FOREIGN KEY (session_id) REFERENCES public.admin_sessions(id) ON DELETE CASCADE;

ALTER TABLE ONLY public.admin_passkeys
    ADD CONSTRAINT admin_passkeys_admin_id_fkey FOREIGN KEY (admin_id) REFERENCES public.admins(id) ON DELETE CASCADE;

ALTER TABLE ONLY public.admin_recovery_codes
    ADD CONSTRAINT admin_recovery_codes_admin_id_fkey FOREIGN KEY (admin_id) REFERENCES public.admins(id) ON DELETE CASCADE;

ALTER TABLE ONLY public.admin_sessions
    ADD CONSTRAINT admin_sessions_admin_id_fkey FOREIGN KEY (admin_id) REFERENCES public.admins(id) ON DELETE CASCADE;

ALTER TABLE ONLY public.entry_domain_exclusions
    ADD CONSTRAINT entry_domain_exclusions_created_by_fkey FOREIGN KEY (created_by) REFERENCES public.admins(id) ON DELETE SET NULL;

ALTER TABLE ONLY public.entry_domains
    ADD CONSTRAINT entry_domains_box_fkey FOREIGN KEY (box) REFERENCES public.entry_boxes(name) ON DELETE SET NULL;

ALTER TABLE ONLY public.entry_fail_reports
    ADD CONSTRAINT entry_fail_reports_account_id_fkey FOREIGN KEY (account_id) REFERENCES public.hub_accounts(id) ON DELETE RESTRICT;

ALTER TABLE ONLY public.entry_fail_reports
    ADD CONSTRAINT entry_fail_reports_relay_id_fkey FOREIGN KEY (relay_id) REFERENCES public.entry_relays(id) ON DELETE RESTRICT;

ALTER TABLE ONLY public.entry_grants
    ADD CONSTRAINT entry_grants_account_id_fkey FOREIGN KEY (account_id) REFERENCES public.hub_accounts(id) ON DELETE RESTRICT;

ALTER TABLE ONLY public.entry_grants
    ADD CONSTRAINT entry_grants_relay_id_fkey FOREIGN KEY (relay_id) REFERENCES public.entry_relays(id) ON DELETE RESTRICT;

ALTER TABLE ONLY public.entry_incidents
    ADD CONSTRAINT entry_incidents_box_fkey FOREIGN KEY (box) REFERENCES public.entry_boxes(name) ON DELETE CASCADE;

ALTER TABLE ONLY public.entry_incidents
    ADD CONSTRAINT entry_incidents_replaced_by_fkey FOREIGN KEY (replaced_by) REFERENCES public.entry_boxes(name) ON DELETE SET NULL;

ALTER TABLE ONLY public.entry_job_log
    ADD CONSTRAINT entry_job_log_job_id_fkey FOREIGN KEY (job_id) REFERENCES public.entry_jobs(id) ON DELETE CASCADE;

ALTER TABLE ONLY public.entry_jobs
    ADD CONSTRAINT entry_jobs_box_fkey FOREIGN KEY (box) REFERENCES public.entry_boxes(name) ON DELETE RESTRICT;

ALTER TABLE ONLY public.entry_jobs
    ADD CONSTRAINT entry_jobs_rollout_id_fkey FOREIGN KEY (rollout_id) REFERENCES public.entry_rollouts(id) ON DELETE RESTRICT;

ALTER TABLE ONLY public.entry_probe_tasks
    ADD CONSTRAINT entry_probe_tasks_job_id_fkey FOREIGN KEY (job_id) REFERENCES public.entry_jobs(id) ON DELETE SET NULL;

ALTER TABLE ONLY public.entry_providers
    ADD CONSTRAINT entry_providers_updated_by_fkey FOREIGN KEY (updated_by) REFERENCES public.admins(id) ON DELETE SET NULL;

ALTER TABLE ONLY public.entry_relays
    ADD CONSTRAINT entry_relays_box_fkey FOREIGN KEY (box) REFERENCES public.entry_boxes(name) ON DELETE RESTRICT;

ALTER TABLE ONLY public.entry_repairs
    ADD CONSTRAINT entry_repairs_box_fkey FOREIGN KEY (box) REFERENCES public.entry_boxes(name) ON DELETE CASCADE;

ALTER TABLE ONLY public.entry_repairs
    ADD CONSTRAINT entry_repairs_incident_id_fkey FOREIGN KEY (incident_id) REFERENCES public.entry_incidents(id) ON DELETE CASCADE;

ALTER TABLE ONLY public.entry_repairs
    ADD CONSTRAINT entry_repairs_job_id_fkey FOREIGN KEY (job_id) REFERENCES public.entry_jobs(id) ON DELETE SET NULL;

ALTER TABLE ONLY public.entry_rollouts
    ADD CONSTRAINT entry_rollouts_canary_box_fkey FOREIGN KEY (canary_box) REFERENCES public.entry_boxes(name) ON DELETE RESTRICT;

ALTER TABLE ONLY public.entry_spend_caps
    ADD CONSTRAINT entry_spend_caps_updated_by_fkey FOREIGN KEY (updated_by) REFERENCES public.admins(id) ON DELETE SET NULL;

ALTER TABLE ONLY public.entry_vantages
    ADD CONSTRAINT entry_vantages_box_fkey FOREIGN KEY (box) REFERENCES public.entry_boxes(name) ON DELETE SET NULL;

ALTER TABLE ONLY public.hub_subscriptions
    ADD CONSTRAINT hub_subscriptions_account_id_fkey FOREIGN KEY (account_id) REFERENCES public.hub_accounts(id) ON DELETE CASCADE;

INSERT INTO entry_detection_policy (id) VALUES (true);
INSERT INTO entry_bench_policy (id) VALUES (true);
-- No spend until the owner explicitly sets caps and enables the provider.
INSERT INTO entry_spend_caps (role, daily_cap_cents, monthly_cap_cents)
VALUES ('hosting', 0, 0), ('registrar', 0, 0);
