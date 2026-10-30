-- 001_baseline.sql — the schema, as one file.
--
-- Squashed from 146 incremental migrations on 2026-09-08 (audit decision Р9).
-- Those files are in ARCHIVE/db-migrations-legacy/ and are not history you can
-- replay: several add a column a later one drops, and a few promised follow-up
-- DROPs that never landed. This file is the schema that actually exists —
-- dumped from a database with every one of them applied, then with the dead
-- columns and tables removed.
--
-- Changing the schema from here: add 002_*.sql and so on. This file is only
-- regenerated if the whole thing is squashed again; a database that has already
-- applied it never re-reads it (the runner keys on filename).
--
-- Generated with:
--   pg_dump --schema-only --no-owner --no-privileges --no-comments
-- then stripped of psql meta-commands, the session SET preamble, and the
-- schema_migrations table (internal/db.RunMigrations creates that one itself,
-- before it reads any migration file).

--
-- PostgreSQL database dump
--

--
-- Name: admin_audit_log_append_only_guard(); Type: FUNCTION; Schema: public; Owner: -
--

CREATE FUNCTION public.admin_audit_log_append_only_guard() RETURNS trigger
    LANGUAGE plpgsql
    AS $$
BEGIN
    RAISE EXCEPTION 'admin_audit_log is append-only; UPDATE/DELETE blocked';
END;
$$;

--
-- Name: pickfreehandle(text); Type: FUNCTION; Schema: public; Owner: -
--

CREATE FUNCTION public.pickfreehandle(base text) RETURNS text
    LANGUAGE plpgsql
    AS $$
DECLARE
    candidate TEXT;
    i INT := 2;
BEGIN
    candidate := lower(base);
    LOOP
        PERFORM 1 FROM handles WHERE name = candidate;
        IF NOT FOUND THEN
            RETURN candidate;
        END IF;
        candidate := lower(base) || '_' || i::text;
        i := i + 1;
        IF i > 999 THEN
            -- Should never happen for any realistic dataset; bail loud.
            RAISE EXCEPTION 'pickFreeHandle exhausted suffixes for %', base;
        END IF;
    END LOOP;
END;
$$;

--
-- Name: reject_message_edit_history_mutation(); Type: FUNCTION; Schema: public; Owner: -
--

CREATE FUNCTION public.reject_message_edit_history_mutation() RETURNS trigger
    LANGUAGE plpgsql
    AS $$
BEGIN
    RAISE EXCEPTION 'message_edit_history is append-only';
END;
$$;

--
-- Name: reject_user_consents_mutation(); Type: FUNCTION; Schema: public; Owner: -
--

CREATE FUNCTION public.reject_user_consents_mutation() RETURNS trigger
    LANGUAGE plpgsql
    AS $$
BEGIN
    RAISE EXCEPTION 'user_consents is append-only';
END;
$$;

--
-- Name: set_idea_published_at(); Type: FUNCTION; Schema: public; Owner: -
--

CREATE FUNCTION public.set_idea_published_at() RETURNS trigger
    LANGUAGE plpgsql
    AS $$
BEGIN
    IF NEW.ticket_type = 'idea'
       AND NEW.status = 'resolved'
       AND NEW.idea_published_at IS NULL THEN
        IF TG_OP = 'INSERT' THEN
            NEW.idea_published_at = now();
        ELSIF OLD.status IS DISTINCT FROM 'resolved' THEN
            NEW.idea_published_at = now();
        END IF;
    END IF;
    RETURN NEW;
END;
$$;

--
-- Name: admin_audit_log; Type: TABLE; Schema: public; Owner: -
--

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

--
-- Name: admin_audit_log_id_seq; Type: SEQUENCE; Schema: public; Owner: -
--

CREATE SEQUENCE public.admin_audit_log_id_seq
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;

--
-- Name: admin_audit_log_id_seq; Type: SEQUENCE OWNED BY; Schema: public; Owner: -
--

ALTER SEQUENCE public.admin_audit_log_id_seq OWNED BY public.admin_audit_log.id;

--
-- Name: admin_credentials; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.admin_credentials (
    user_id uuid NOT NULL,
    username text NOT NULL,
    password_hash text NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    last_login_at timestamp with time zone,
    last_login_ip inet,
    failed_count integer DEFAULT 0 NOT NULL,
    locked_until timestamp with time zone
);

--
-- Name: auth_lockouts; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.auth_lockouts (
    username text NOT NULL,
    ip inet NOT NULL,
    failed_count integer DEFAULT 0 NOT NULL,
    lockout_level smallint DEFAULT 0 NOT NULL,
    locked_until timestamp with time zone,
    updated_at timestamp with time zone DEFAULT now() NOT NULL
);

--
-- Name: broadcast_post_meta; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.broadcast_post_meta (
    message_id uuid NOT NULL,
    view_count integer DEFAULT 0 NOT NULL,
    pinned boolean DEFAULT false NOT NULL
);

--
-- Name: bug_ticket_replies; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.bug_ticket_replies (
    id bigint NOT NULL,
    ticket_id bigint NOT NULL,
    author_id uuid,
    author_kind text DEFAULT 'admin'::text NOT NULL,
    text text NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL
);

--
-- Name: bug_ticket_replies_id_seq; Type: SEQUENCE; Schema: public; Owner: -
--

CREATE SEQUENCE public.bug_ticket_replies_id_seq
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;

--
-- Name: bug_ticket_replies_id_seq; Type: SEQUENCE OWNED BY; Schema: public; Owner: -
--

ALTER SEQUENCE public.bug_ticket_replies_id_seq OWNED BY public.bug_ticket_replies.id;

--
-- Name: bug_tickets; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.bug_tickets (
    id bigint NOT NULL,
    user_id uuid NOT NULL,
    ts_estimated timestamp with time zone NOT NULL,
    text text NOT NULL,
    snapshot jsonb DEFAULT '{}'::jsonb NOT NULL,
    status text DEFAULT 'open'::text NOT NULL,
    ai_summary text,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    ticket_type text DEFAULT 'bug'::text NOT NULL,
    idea_published_at timestamp with time zone,
    CONSTRAINT bug_tickets_ticket_type_check CHECK ((ticket_type = ANY (ARRAY['bug'::text, 'idea'::text])))
);

--
-- Name: bug_tickets_id_seq; Type: SEQUENCE; Schema: public; Owner: -
--

CREATE SEQUENCE public.bug_tickets_id_seq
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;

--
-- Name: bug_tickets_id_seq; Type: SEQUENCE OWNED BY; Schema: public; Owner: -
--

ALTER SEQUENCE public.bug_tickets_id_seq OWNED BY public.bug_tickets.id;

--
-- Name: channel_invites; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.channel_invites (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    channel_id uuid NOT NULL,
    code character varying(12) NOT NULL,
    creator_id uuid NOT NULL,
    max_uses integer DEFAULT 0 NOT NULL,
    use_count integer DEFAULT 0 NOT NULL,
    expires_at timestamp with time zone,
    show_history boolean DEFAULT true NOT NULL,
    all_rooms boolean DEFAULT false NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL
);

--
-- Name: channel_messages; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.channel_messages (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    channel_id uuid NOT NULL,
    sender_id uuid NOT NULL,
    username character varying(32) NOT NULL,
    content text,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    attachment_s3_key text,
    attachment_type character varying(10),
    file_name text,
    file_size bigint,
    reply_to_id text,
    reply_to_username text,
    reply_to_content text,
    reply_to_attachment_s3_key text,
    file_id uuid,
    edited_at timestamp with time zone,
    forwarded_from_username text,
    forwarded_from_content text,
    quoted_post_id text,
    quoted_channel_id text,
    quoted_channel_name text,
    quoted_channel_avatar text,
    quoted_post_content text,
    quoted_post_author text,
    quoted_post_attachment_s3_key text,
    client_message_id character varying(64),
    content_ct bytea,
    content_key_version smallint,
    mentioned_user_ids uuid[],
    group_id uuid,
    entities_ct bytea,
    entities_key_version smallint,
    mentioned_role_ids uuid[],
    mentions_everyone boolean DEFAULT false NOT NULL,
    thread_root_id uuid,
    mirror_of uuid,
    has_link boolean DEFAULT false NOT NULL
);

--
-- Name: channel_permission_overrides; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.channel_permission_overrides (
    channel_id uuid NOT NULL,
    holder_type character varying(8) NOT NULL,
    holder_id uuid,
    allow_mask bigint DEFAULT 0 NOT NULL,
    deny_mask bigint DEFAULT 0 NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT channel_permission_overrides_check CHECK (((((holder_type)::text = 'everyone'::text) AND (holder_id IS NULL)) OR (((holder_type)::text = ANY ((ARRAY['role'::character varying, 'user'::character varying])::text[])) AND (holder_id IS NOT NULL)))),
    CONSTRAINT channel_permission_overrides_holder_type_check CHECK (((holder_type)::text = ANY ((ARRAY['role'::character varying, 'user'::character varying, 'everyone'::character varying])::text[])))
);

--
-- Name: channel_read_state; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.channel_read_state (
    channel_id uuid NOT NULL,
    user_id uuid NOT NULL,
    last_read_at timestamp with time zone,
    last_read_message_id uuid
);

--
-- Name: chat_mutes; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.chat_mutes (
    user_id uuid NOT NULL,
    chat_type text NOT NULL,
    chat_id uuid NOT NULL,
    mute_mode text NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    mute_expires_at timestamp with time zone,
    CONSTRAINT chat_mutes_chat_type_check CHECK ((chat_type = ANY (ARRAY['channel'::text, 'dm'::text, 'room'::text, 'server'::text, 'news_channel'::text]))),
    CONSTRAINT chat_mutes_mute_mode_check CHECK ((mute_mode = ANY (ARRAY['sounds'::text, 'full'::text])))
);

--
-- Name: conversation_participants; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.conversation_participants (
    conversation_id uuid NOT NULL,
    user_id uuid NOT NULL,
    joined_at timestamp with time zone DEFAULT now() NOT NULL,
    last_read_at timestamp with time zone,
    hidden_at timestamp with time zone,
    last_read_message_id uuid
);

--
-- Name: conversations; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.conversations (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL
);

--
-- Name: drafts; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.drafts (
    user_id uuid NOT NULL,
    chat_type smallint NOT NULL,
    chat_id uuid NOT NULL,
    thread_root_id text DEFAULT ''::text NOT NULL,
    text_ct bytea,
    text_key_version smallint,
    entities_ct bytea,
    entities_key_version smallint,
    cursor integer DEFAULT 0 NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL
);

--
-- Name: email_delivery_events; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.email_delivery_events (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    job_id text NOT NULL,
    recipient text NOT NULL,
    status text NOT NULL,
    event_name text NOT NULL,
    event_time timestamp with time zone NOT NULL,
    received_at timestamp with time zone DEFAULT now() NOT NULL,
    delivery_status text,
    destination_response text,
    user_agent text,
    ip text,
    click_url text,
    metadata jsonb,
    raw jsonb
);

--
-- Name: email_send_log; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.email_send_log (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    sent_at timestamp with time zone DEFAULT now() NOT NULL,
    sent_by_admin_id uuid,
    recipient text NOT NULL,
    template_id text,
    mode text NOT NULL,
    subject text NOT NULL,
    status text NOT NULL,
    error_msg text,
    upstream_message_id text,
    provider text NOT NULL,
    last_status text,
    last_event_at timestamp with time zone,
    delivered_at timestamp with time zone,
    opened_at timestamp with time zone,
    clicked_at timestamp with time zone,
    bounced_at timestamp with time zone,
    CONSTRAINT email_send_log_mode_check CHECK ((mode = ANY (ARRAY['template'::text, 'raw'::text]))),
    CONSTRAINT email_send_log_provider_chk CHECK ((provider = ANY (ARRAY['smtp'::text, 'unisender'::text]))),
    CONSTRAINT email_send_log_status_check CHECK ((status = ANY (ARRAY['sent'::text, 'failed'::text])))
);

--
-- Name: email_verifications; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.email_verifications (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    email character varying(255) NOT NULL,
    code_hash text NOT NULL,
    purpose text NOT NULL,
    attempts integer DEFAULT 0 NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    expires_at timestamp with time zone NOT NULL,
    consumed_at timestamp with time zone,
    verification_token_jti text,
    verification_token_consumed_at timestamp with time zone,
    mail_failed_at timestamp with time zone,
    ip_address inet,
    CONSTRAINT email_verifications_purpose_check CHECK ((purpose = ANY (ARRAY['registration'::text, 'password_reset'::text, 'email_change'::text])))
);

--
-- Name: files; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.files (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    sha256 character(64) NOT NULL,
    s3_key text NOT NULL,
    content_type character varying(100) NOT NULL,
    file_category character varying(20) NOT NULL,
    original_name text,
    size_bytes bigint NOT NULL,
    thumb_s3_key text,
    micro_s3_key text,
    width integer,
    height integer,
    duration_ms integer,
    ref_count integer DEFAULT 1 NOT NULL,
    uploaded_by uuid NOT NULL,
    created_at timestamp with time zone DEFAULT now(),
    original_sha256 character(64),
    verified boolean DEFAULT true NOT NULL,
    verify_started_at timestamp with time zone,
    verify_attempts integer DEFAULT 0 NOT NULL
);

--
-- Name: fraud_action_thresholds; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.fraud_action_thresholds (
    id integer DEFAULT 1 NOT NULL,
    pass_max integer DEFAULT 30 NOT NULL,
    sms_verify_max integer DEFAULT 60 NOT NULL,
    shadowban_max integer DEFAULT 80 NOT NULL,
    updated_at timestamp with time zone DEFAULT now(),
    CONSTRAINT fraud_action_thresholds_id_check CHECK ((id = 1))
);

--
-- Name: fraud_datacenter_patterns; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.fraud_datacenter_patterns (
    id integer NOT NULL,
    pattern text NOT NULL,
    provider text NOT NULL,
    enabled boolean DEFAULT true,
    created_at timestamp with time zone DEFAULT now()
);

--
-- Name: fraud_datacenter_patterns_id_seq; Type: SEQUENCE; Schema: public; Owner: -
--

CREATE SEQUENCE public.fraud_datacenter_patterns_id_seq
    AS integer
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;

--
-- Name: fraud_datacenter_patterns_id_seq; Type: SEQUENCE OWNED BY; Schema: public; Owner: -
--

ALTER SEQUENCE public.fraud_datacenter_patterns_id_seq OWNED BY public.fraud_datacenter_patterns.id;

--
-- Name: fraud_scoring_log; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.fraud_scoring_log (
    id integer NOT NULL,
    user_id uuid NOT NULL,
    scored_at timestamp with time zone DEFAULT now(),
    total_score integer NOT NULL,
    action text NOT NULL,
    breakdown jsonb NOT NULL,
    ip_address text,
    asn_org text,
    fp_hash text,
    pow_time_ms integer,
    ip_address_ct bytea
);

--
-- Name: fraud_scoring_log_id_seq; Type: SEQUENCE; Schema: public; Owner: -
--

CREATE SEQUENCE public.fraud_scoring_log_id_seq
    AS integer
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;

--
-- Name: fraud_scoring_log_id_seq; Type: SEQUENCE OWNED BY; Schema: public; Owner: -
--

ALTER SEQUENCE public.fraud_scoring_log_id_seq OWNED BY public.fraud_scoring_log.id;

--
-- Name: fraud_scoring_signals; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.fraud_scoring_signals (
    key text NOT NULL,
    label text NOT NULL,
    category text NOT NULL,
    points integer DEFAULT 0 NOT NULL,
    enabled boolean DEFAULT true NOT NULL,
    sort_order integer DEFAULT 0 NOT NULL
);

--
-- Name: group_channel_members; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.group_channel_members (
    channel_id uuid NOT NULL,
    user_id uuid NOT NULL,
    joined_at timestamp with time zone DEFAULT now() NOT NULL
);

--
-- Name: group_channels; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.group_channels (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    name character varying(64) NOT NULL,
    slug character varying(32) NOT NULL,
    avatar_s3_key text,
    owner_id uuid NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    server_id uuid,
    category_id uuid,
    description text DEFAULT ''::text NOT NULL,
    avatar_thumb_s3_key text,
    is_synthetic boolean DEFAULT false NOT NULL,
    is_public boolean DEFAULT true NOT NULL,
    is_broadcast boolean DEFAULT false NOT NULL,
    banner_s3_key text,
    is_discussion boolean DEFAULT false NOT NULL
);

--
-- Name: handles; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.handles (
    name text NOT NULL,
    owner_type text NOT NULL,
    owner_id uuid NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT handles_name_lowercase CHECK ((name = lower(name))),
    CONSTRAINT handles_owner_type_check CHECK ((owner_type = ANY (ARRAY['user'::text, 'server'::text, 'group_channel'::text, 'space'::text])))
);

--
-- Name: idea_votes; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.idea_votes (
    idea_id bigint NOT NULL,
    user_id uuid NOT NULL,
    vote smallint NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT idea_votes_vote_check CHECK ((vote = ANY (ARRAY['-1'::integer, 1])))
);

--
-- Name: link_preview_images; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.link_preview_images (
    source_url_sha256 bytea NOT NULL,
    object_key text NOT NULL,
    content_type text NOT NULL,
    size_bytes bigint NOT NULL,
    stored_at timestamp with time zone DEFAULT now() NOT NULL,
    last_seen_at timestamp with time zone DEFAULT now() NOT NULL
);

--
-- Name: log_config; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.log_config (
    id bigint NOT NULL,
    scope text NOT NULL,
    user_id uuid,
    category text NOT NULL,
    min_level text NOT NULL,
    remote_enabled boolean DEFAULT true NOT NULL,
    expires_at timestamp with time zone,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT log_config_check CHECK ((((scope = 'user'::text) AND (user_id IS NOT NULL)) OR ((scope = 'global'::text) AND (user_id IS NULL)))),
    CONSTRAINT log_config_min_level_check CHECK ((min_level = ANY (ARRAY['trace'::text, 'debug'::text, 'info'::text, 'warn'::text, 'error'::text]))),
    CONSTRAINT log_config_scope_check CHECK ((scope = ANY (ARRAY['global'::text, 'user'::text])))
);

--
-- Name: log_config_id_seq; Type: SEQUENCE; Schema: public; Owner: -
--

CREATE SEQUENCE public.log_config_id_seq
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;

--
-- Name: log_config_id_seq; Type: SEQUENCE OWNED BY; Schema: public; Owner: -
--

ALTER SEQUENCE public.log_config_id_seq OWNED BY public.log_config.id;

--
-- Name: mailer_settings; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.mailer_settings (
    id smallint DEFAULT 1 NOT NULL,
    active_provider text,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_by uuid,
    CONSTRAINT mailer_settings_active_provider_check CHECK (((active_provider IS NULL) OR (active_provider = ANY (ARRAY['smtp'::text, 'unisender'::text])))),
    CONSTRAINT mailer_settings_id_check CHECK ((id = 1))
);

--
-- Name: manual_unreads; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.manual_unreads (
    user_id uuid NOT NULL,
    chat_type text NOT NULL,
    chat_id uuid NOT NULL,
    marked_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT manual_unreads_chat_type_check CHECK ((chat_type = ANY (ARRAY['channel'::text, 'dm'::text, 'room'::text, 'server'::text])))
);

--
-- Name: message_deletions; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.message_deletions (
    user_id uuid NOT NULL,
    message_id uuid NOT NULL,
    message_type character varying(10) DEFAULT 'dm'::character varying NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL
);

--
-- Name: message_edit_history; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.message_edit_history (
    id bigint NOT NULL,
    kind text NOT NULL,
    message_id uuid NOT NULL,
    editor_id uuid NOT NULL,
    old_content text,
    edited_at timestamp with time zone DEFAULT now() NOT NULL,
    old_content_ct bytea,
    old_content_key_version smallint,
    CONSTRAINT message_edit_history_kind_check CHECK ((kind = ANY (ARRAY['dm'::text, 'channel'::text])))
);

--
-- Name: message_edit_history_id_seq; Type: SEQUENCE; Schema: public; Owner: -
--

CREATE SEQUENCE public.message_edit_history_id_seq
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;

--
-- Name: message_edit_history_id_seq; Type: SEQUENCE OWNED BY; Schema: public; Owner: -
--

ALTER SEQUENCE public.message_edit_history_id_seq OWNED BY public.message_edit_history.id;

--
-- Name: message_reactions; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.message_reactions (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    message_id uuid NOT NULL,
    user_id uuid NOT NULL,
    emoji text NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL
);

--
-- Name: message_search_tokens; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.message_search_tokens (
    token_hash bytea NOT NULL,
    message_id uuid NOT NULL,
    message_type smallint NOT NULL,
    scope_id uuid NOT NULL,
    token_kind smallint NOT NULL,
    created_at timestamp with time zone NOT NULL
);

--
-- Name: messages; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.messages (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    conversation_id uuid NOT NULL,
    sender_id uuid NOT NULL,
    content text,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    attachment_s3_key text,
    attachment_type character varying(10),
    file_name text,
    file_size bigint,
    reply_to_id text,
    reply_to_username text,
    reply_to_content text,
    reply_to_attachment_s3_key text,
    file_id uuid,
    edited_at timestamp with time zone,
    forwarded_from_username text,
    forwarded_from_content text,
    quoted_post_id text,
    quoted_channel_id text,
    quoted_channel_name text,
    quoted_channel_avatar text,
    quoted_post_content text,
    quoted_post_author text,
    quoted_post_attachment_s3_key text,
    attachment_meta jsonb,
    client_message_id character varying(64),
    message_type text DEFAULT ''::text NOT NULL,
    call_reason text,
    call_duration_sec integer,
    content_ct bytea,
    content_key_version smallint,
    mentioned_user_ids uuid[],
    group_id uuid,
    entities_ct bytea,
    entities_key_version smallint,
    mentioned_role_ids uuid[],
    mentions_everyone boolean DEFAULT false NOT NULL,
    has_link boolean DEFAULT false NOT NULL
);

--
-- Name: notifications; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.notifications (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    user_id uuid NOT NULL,
    type character varying(32) NOT NULL,
    actor_id uuid,
    payload jsonb DEFAULT '{}'::jsonb,
    read boolean DEFAULT false NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    payload_ct bytea,
    payload_key_version smallint
);

--
-- Name: pinned_chats; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.pinned_chats (
    user_id uuid NOT NULL,
    chat_type text NOT NULL,
    chat_id uuid NOT NULL,
    pinned_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT pinned_chats_chat_type_check CHECK ((chat_type = ANY (ARRAY['channel'::text, 'dm'::text, 'room'::text, 'server'::text])))
);

--
-- Name: pinned_server_rooms; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.pinned_server_rooms (
    user_id uuid NOT NULL,
    server_id uuid NOT NULL,
    room_id uuid NOT NULL,
    pinned_at timestamp with time zone DEFAULT now() NOT NULL,
    "position" integer DEFAULT 0 NOT NULL
);

--
-- Name: relationships; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.relationships (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    user_id uuid NOT NULL,
    target_id uuid NOT NULL,
    type character varying(20) NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL
);

--
-- Name: role_members; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.role_members (
    container_type character varying(8) NOT NULL,
    container_id uuid NOT NULL,
    user_id uuid NOT NULL,
    role_id uuid NOT NULL,
    CONSTRAINT role_members_container_type_check CHECK (((container_type)::text = ANY ((ARRAY['server'::character varying, 'room'::character varying])::text[])))
);

--
-- Name: roles; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.roles (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    container_type character varying(8) NOT NULL,
    container_id uuid NOT NULL,
    name character varying(80) NOT NULL,
    color character varying(32) DEFAULT ''::character varying NOT NULL,
    "position" integer DEFAULT 0 NOT NULL,
    permissions bigint DEFAULT 0 NOT NULL,
    is_system boolean DEFAULT false NOT NULL,
    system_key character varying(16),
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT roles_container_type_check CHECK (((container_type)::text = ANY ((ARRAY['server'::character varying, 'room'::character varying])::text[])))
);

--
-- Name: room_categories; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.room_categories (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    server_id uuid NOT NULL,
    name character varying(64) NOT NULL,
    "position" integer DEFAULT 0 NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL
);

--
-- Name: scope_events; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.scope_events (
    scope_kind smallint NOT NULL,
    scope_id uuid NOT NULL,
    seq bigint NOT NULL,
    event_type smallint NOT NULL,
    message_id uuid,
    actor_id uuid,
    payload jsonb DEFAULT '{}'::jsonb NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL
);

--
-- Name: scope_seq; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.scope_seq (
    scope_kind smallint NOT NULL,
    scope_id uuid NOT NULL,
    last_seq bigint DEFAULT 0 NOT NULL
);

--
-- Name: server_bans; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.server_bans (
    server_id uuid NOT NULL,
    user_id uuid NOT NULL,
    banned_by uuid,
    reason text DEFAULT ''::text NOT NULL,
    banned_at timestamp with time zone DEFAULT now() NOT NULL
);

--
-- Name: server_invites; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.server_invites (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    server_id uuid NOT NULL,
    code character varying(12) NOT NULL,
    creator_id uuid NOT NULL,
    max_uses integer DEFAULT 0 NOT NULL,
    use_count integer DEFAULT 0 NOT NULL,
    expires_at timestamp with time zone,
    created_at timestamp with time zone DEFAULT now() NOT NULL
);

--
-- Name: server_members; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.server_members (
    server_id uuid NOT NULL,
    user_id uuid NOT NULL,
    joined_at timestamp with time zone DEFAULT now() NOT NULL,
    timed_out_until timestamp with time zone,
    timeout_reason text,
    join_method text
);

--
-- Name: server_moderation_log; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.server_moderation_log (
    id bigint NOT NULL,
    server_id uuid NOT NULL,
    actor_id uuid,
    target_id uuid NOT NULL,
    action text NOT NULL,
    reason text DEFAULT ''::text NOT NULL,
    expires_at timestamp with time zone,
    role_id uuid,
    role_name text,
    created_at timestamp with time zone DEFAULT now() NOT NULL
);

--
-- Name: server_moderation_log_id_seq; Type: SEQUENCE; Schema: public; Owner: -
--

CREATE SEQUENCE public.server_moderation_log_id_seq
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;

--
-- Name: server_moderation_log_id_seq; Type: SEQUENCE OWNED BY; Schema: public; Owner: -
--

ALTER SEQUENCE public.server_moderation_log_id_seq OWNED BY public.server_moderation_log.id;

--
-- Name: server_mutes; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.server_mutes (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    server_id uuid NOT NULL,
    user_id uuid NOT NULL,
    muted_by uuid,
    expires_at timestamp with time zone,
    reason text DEFAULT ''::text NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL
);

--
-- Name: server_tags; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.server_tags (
    server_id uuid NOT NULL,
    tag character varying(50) NOT NULL
);

--
-- Name: servers; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.servers (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    name character varying(64) NOT NULL,
    slug character varying(32) NOT NULL,
    description text DEFAULT ''::text NOT NULL,
    avatar_s3_key text,
    banner_s3_key text,
    owner_id uuid NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    is_synthetic boolean DEFAULT false NOT NULL,
    is_public boolean DEFAULT true NOT NULL,
    comments_enabled boolean DEFAULT true NOT NULL
);

--
-- Name: sfu_nodes; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.sfu_nodes (
    node_id text NOT NULL,
    region text NOT NULL,
    public_url text NOT NULL,
    grpc_addr text NOT NULL,
    last_heartbeat timestamp with time zone DEFAULT now() NOT NULL,
    status text DEFAULT 'active'::text NOT NULL,
    build_sha text DEFAULT ''::text NOT NULL,
    started_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT sfu_nodes_status_check CHECK ((status = ANY (ARRAY['active'::text, 'draining'::text, 'down'::text])))
);

--
-- Name: spaces; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.spaces (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    primary_handle text NOT NULL,
    server_id uuid,
    broadcast_channel_id uuid,
    default_view text DEFAULT 'news'::text NOT NULL,
    owner_id uuid NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT spaces_check CHECK (((server_id IS NOT NULL) OR (broadcast_channel_id IS NOT NULL))),
    CONSTRAINT spaces_default_view_check CHECK ((default_view = ANY (ARRAY['news'::text, 'server'::text])))
);

--
-- Name: stream_reports; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.stream_reports (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    reporter_id uuid NOT NULL,
    sharer_id uuid,
    category text NOT NULL,
    description text DEFAULT ''::text NOT NULL,
    user_agent text DEFAULT ''::text NOT NULL,
    status text DEFAULT 'new'::text NOT NULL,
    reviewer_id uuid,
    reviewer_note text DEFAULT ''::text NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    resolved_at timestamp with time zone,
    target_kind text DEFAULT 'screen'::text NOT NULL,
    target_ref jsonb,
    content_snapshot text DEFAULT ''::text,
    content_snapshot_ct bytea,
    content_snapshot_key_version smallint,
    CONSTRAINT stream_reports_category_check CHECK ((category = ANY (ARRAY['video'::text, 'audio'::text, 'lag'::text, 'content'::text, 'other'::text]))),
    CONSTRAINT stream_reports_status_check CHECK ((status = ANY (ARRAY['new'::text, 'in_review'::text, 'resolved'::text, 'dismissed'::text]))),
    CONSTRAINT stream_reports_target_kind_check CHECK ((target_kind = ANY (ARRAY['screen'::text, 'voice'::text, 'other'::text])))
);

--
-- Name: user_consents; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.user_consents (
    id bigint NOT NULL,
    user_id uuid NOT NULL,
    policy_version text NOT NULL,
    accepted_at timestamp with time zone DEFAULT now() NOT NULL,
    ip_address inet,
    user_agent text
);

--
-- Name: user_consents_id_seq; Type: SEQUENCE; Schema: public; Owner: -
--

CREATE SEQUENCE public.user_consents_id_seq
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;

--
-- Name: user_consents_id_seq; Type: SEQUENCE OWNED BY; Schema: public; Owner: -
--

ALTER SEQUENCE public.user_consents_id_seq OWNED BY public.user_consents.id;

--
-- Name: user_reports; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.user_reports (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    reporter_id uuid NOT NULL,
    target_id uuid NOT NULL,
    reason text DEFAULT ''::text NOT NULL,
    context text DEFAULT ''::text NOT NULL,
    user_agent text DEFAULT ''::text NOT NULL,
    status text DEFAULT 'new'::text NOT NULL,
    reviewer_id uuid,
    reviewer_note text DEFAULT ''::text NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    resolved_at timestamp with time zone,
    category text DEFAULT 'other'::text NOT NULL,
    target_kind text DEFAULT 'profile'::text NOT NULL,
    target_ref jsonb,
    content_snapshot text DEFAULT ''::text,
    content_snapshot_ct bytea,
    content_snapshot_key_version smallint,
    CONSTRAINT user_reports_category_check CHECK ((category = ANY (ARRAY['harassment'::text, 'spam'::text, 'nsfw'::text, 'impersonation'::text, 'other'::text]))),
    CONSTRAINT user_reports_status_check CHECK ((status = ANY (ARRAY['new'::text, 'in_review'::text, 'resolved'::text, 'dismissed'::text]))),
    CONSTRAINT user_reports_target_kind_check CHECK ((target_kind = ANY (ARRAY['message'::text, 'screen'::text, 'voice'::text, 'profile'::text, 'avatar'::text, 'post'::text, 'block'::text, 'dm'::text, 'other'::text])))
);

--
-- Name: user_sessions; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.user_sessions (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    user_id uuid NOT NULL,
    public_key bytea NOT NULL,
    fingerprint_hash bytea NOT NULL,
    platform text DEFAULT 'unknown'::text NOT NULL,
    last_ip inet,
    last_user_agent text,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    last_used_at timestamp with time zone DEFAULT now() NOT NULL,
    expires_at timestamp with time zone NOT NULL,
    revoked_at timestamp with time zone,
    revoked_reason text,
    last_ip_ct bytea
);

--
-- Name: user_settings; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.user_settings (
    user_id uuid NOT NULL,
    preferences jsonb DEFAULT '{}'::jsonb NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL
);

--
-- Name: user_streak_days; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.user_streak_days (
    user_id uuid NOT NULL,
    day date NOT NULL,
    messages integer DEFAULT 0 NOT NULL,
    voice_seconds integer DEFAULT 0 NOT NULL,
    goal_met boolean DEFAULT false NOT NULL
);

--
-- Name: user_streaks; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.user_streaks (
    user_id uuid NOT NULL,
    streak_days integer DEFAULT 0 NOT NULL,
    today_date date DEFAULT ((now() AT TIME ZONE 'UTC'::text))::date NOT NULL,
    today_messages integer DEFAULT 0 NOT NULL,
    today_voice_seconds integer DEFAULT 0 NOT NULL,
    today_goal_met boolean DEFAULT false NOT NULL,
    last_completed_date date,
    premium_until timestamp with time zone,
    updated_at timestamp with time zone DEFAULT now() NOT NULL
);

--
-- Name: user_themes; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.user_themes (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    user_id uuid NOT NULL,
    name character varying(64) NOT NULL,
    description character varying(256) DEFAULT ''::character varying NOT NULL,
    colors jsonb NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL
);

--
-- Name: users; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.users (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    username character varying(32) NOT NULL,
    password_hash text NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    last_seen timestamp with time zone,
    avatar_s3_key text,
    avatar_thumb_s3_key text,
    role character varying(20) DEFAULT 'user'::character varying NOT NULL,
    storage_used bigint DEFAULT 0,
    storage_limit bigint DEFAULT '107374182400'::bigint,
    risk_score integer DEFAULT 0,
    need_phone_verification boolean DEFAULT false,
    is_shadowbanned boolean DEFAULT false,
    is_banned boolean DEFAULT false,
    privacy_dm text DEFAULT 'everyone'::character varying NOT NULL,
    privacy_friend_requests text DEFAULT 'everyone'::character varying NOT NULL,
    privacy_group_invite text DEFAULT 'everyone'::character varying NOT NULL,
    display_name character varying(32) DEFAULT ''::character varying NOT NULL,
    privacy_online text DEFAULT 'friends'::character varying NOT NULL,
    privacy_game_activity text DEFAULT 'everyone'::character varying NOT NULL,
    privacy_show_servers text DEFAULT 'everyone'::text NOT NULL,
    analytics_enabled boolean DEFAULT true NOT NULL,
    is_system boolean DEFAULT false NOT NULL,
    last_ip_ct bytea,
    is_synthetic boolean DEFAULT false NOT NULL,
    birthday date,
    custom_status text DEFAULT ''::text NOT NULL,
    deletion_requested_at timestamp with time zone,
    privacy_voice_calls text DEFAULT 'friends'::text NOT NULL,
    privacy_voice_messages text DEFAULT 'friends'::text NOT NULL,
    privacy_forward text DEFAULT 'everyone'::text NOT NULL,
    privacy_profile_photo text DEFAULT 'everyone'::text NOT NULL,
    privacy_birthday text DEFAULT 'friends'::text NOT NULL,
    privacy_status text DEFAULT 'everyone'::text NOT NULL,
    presence_status text DEFAULT 'online'::text NOT NULL,
    email character varying(255),
    username_set boolean DEFAULT true NOT NULL,
    CONSTRAINT users_presence_status_check CHECK ((presence_status = ANY (ARRAY['online'::text, 'idle'::text, 'dnd'::text, 'invisible'::text]))),
    CONSTRAINT users_username_lowercase CHECK (((username)::text = lower((username)::text)))
);

--
-- Name: voice_channel_settings; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.voice_channel_settings (
    channel_id text NOT NULL,
    preferred_region text,
    updated_by_user_id uuid,
    updated_at timestamp with time zone DEFAULT now() NOT NULL
);

--
-- Name: voice_room_pins; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.voice_room_pins (
    channel_id text NOT NULL,
    node_id text NOT NULL,
    region text NOT NULL,
    pinned_at timestamp with time zone DEFAULT now() NOT NULL,
    expires_at timestamp with time zone NOT NULL,
    pinned_by_user_id uuid
);

--
-- Name: ws_tickets; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.ws_tickets (
    id text NOT NULL,
    account_id uuid NOT NULL,
    session_id text DEFAULT ''::text NOT NULL,
    username text DEFAULT ''::text NOT NULL,
    expires_at timestamp with time zone NOT NULL
);

--
-- Name: admin_audit_log id; Type: DEFAULT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.admin_audit_log ALTER COLUMN id SET DEFAULT nextval('public.admin_audit_log_id_seq'::regclass);

--
-- Name: bug_ticket_replies id; Type: DEFAULT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.bug_ticket_replies ALTER COLUMN id SET DEFAULT nextval('public.bug_ticket_replies_id_seq'::regclass);

--
-- Name: bug_tickets id; Type: DEFAULT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.bug_tickets ALTER COLUMN id SET DEFAULT nextval('public.bug_tickets_id_seq'::regclass);

--
-- Name: fraud_datacenter_patterns id; Type: DEFAULT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.fraud_datacenter_patterns ALTER COLUMN id SET DEFAULT nextval('public.fraud_datacenter_patterns_id_seq'::regclass);

--
-- Name: fraud_scoring_log id; Type: DEFAULT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.fraud_scoring_log ALTER COLUMN id SET DEFAULT nextval('public.fraud_scoring_log_id_seq'::regclass);

--
-- Name: log_config id; Type: DEFAULT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.log_config ALTER COLUMN id SET DEFAULT nextval('public.log_config_id_seq'::regclass);

--
-- Name: message_edit_history id; Type: DEFAULT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.message_edit_history ALTER COLUMN id SET DEFAULT nextval('public.message_edit_history_id_seq'::regclass);

--
-- Name: server_moderation_log id; Type: DEFAULT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.server_moderation_log ALTER COLUMN id SET DEFAULT nextval('public.server_moderation_log_id_seq'::regclass);

--
-- Name: user_consents id; Type: DEFAULT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.user_consents ALTER COLUMN id SET DEFAULT nextval('public.user_consents_id_seq'::regclass);

--
-- Name: admin_audit_log admin_audit_log_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.admin_audit_log
    ADD CONSTRAINT admin_audit_log_pkey PRIMARY KEY (id);

--
-- Name: admin_credentials admin_credentials_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.admin_credentials
    ADD CONSTRAINT admin_credentials_pkey PRIMARY KEY (user_id);

--
-- Name: admin_credentials admin_credentials_username_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.admin_credentials
    ADD CONSTRAINT admin_credentials_username_key UNIQUE (username);

--
-- Name: auth_lockouts auth_lockouts_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.auth_lockouts
    ADD CONSTRAINT auth_lockouts_pkey PRIMARY KEY (username, ip);

--
-- Name: broadcast_post_meta broadcast_post_meta_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.broadcast_post_meta
    ADD CONSTRAINT broadcast_post_meta_pkey PRIMARY KEY (message_id);

--
-- Name: bug_ticket_replies bug_ticket_replies_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.bug_ticket_replies
    ADD CONSTRAINT bug_ticket_replies_pkey PRIMARY KEY (id);

--
-- Name: bug_tickets bug_tickets_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.bug_tickets
    ADD CONSTRAINT bug_tickets_pkey PRIMARY KEY (id);

--
-- Name: channel_invites channel_invites_code_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.channel_invites
    ADD CONSTRAINT channel_invites_code_key UNIQUE (code);

--
-- Name: channel_invites channel_invites_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.channel_invites
    ADD CONSTRAINT channel_invites_pkey PRIMARY KEY (id);

--
-- Name: channel_messages channel_messages_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.channel_messages
    ADD CONSTRAINT channel_messages_pkey PRIMARY KEY (id);

--
-- Name: channel_read_state channel_read_state_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.channel_read_state
    ADD CONSTRAINT channel_read_state_pkey PRIMARY KEY (channel_id, user_id);

--
-- Name: scope_events chat_events_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.scope_events
    ADD CONSTRAINT chat_events_pkey PRIMARY KEY (scope_kind, scope_id, seq);

--
-- Name: chat_mutes chat_mutes_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.chat_mutes
    ADD CONSTRAINT chat_mutes_pkey PRIMARY KEY (user_id, chat_type, chat_id);

--
-- Name: scope_seq chat_seq_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.scope_seq
    ADD CONSTRAINT chat_seq_pkey PRIMARY KEY (scope_kind, scope_id);

--
-- Name: conversation_participants conversation_participants_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.conversation_participants
    ADD CONSTRAINT conversation_participants_pkey PRIMARY KEY (conversation_id, user_id);

--
-- Name: conversations conversations_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.conversations
    ADD CONSTRAINT conversations_pkey PRIMARY KEY (id);

--
-- Name: drafts drafts_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.drafts
    ADD CONSTRAINT drafts_pkey PRIMARY KEY (user_id, chat_type, chat_id, thread_root_id);

--
-- Name: email_delivery_events email_delivery_events_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.email_delivery_events
    ADD CONSTRAINT email_delivery_events_pkey PRIMARY KEY (id);

--
-- Name: email_send_log email_send_log_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.email_send_log
    ADD CONSTRAINT email_send_log_pkey PRIMARY KEY (id);

--
-- Name: email_verifications email_verifications_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.email_verifications
    ADD CONSTRAINT email_verifications_pkey PRIMARY KEY (id);

--
-- Name: files files_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.files
    ADD CONSTRAINT files_pkey PRIMARY KEY (id);

--
-- Name: fraud_action_thresholds fraud_action_thresholds_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.fraud_action_thresholds
    ADD CONSTRAINT fraud_action_thresholds_pkey PRIMARY KEY (id);

--
-- Name: fraud_datacenter_patterns fraud_datacenter_patterns_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.fraud_datacenter_patterns
    ADD CONSTRAINT fraud_datacenter_patterns_pkey PRIMARY KEY (id);

--
-- Name: fraud_scoring_log fraud_scoring_log_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.fraud_scoring_log
    ADD CONSTRAINT fraud_scoring_log_pkey PRIMARY KEY (id);

--
-- Name: fraud_scoring_signals fraud_scoring_signals_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.fraud_scoring_signals
    ADD CONSTRAINT fraud_scoring_signals_pkey PRIMARY KEY (key);

--
-- Name: group_channel_members group_channel_members_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.group_channel_members
    ADD CONSTRAINT group_channel_members_pkey PRIMARY KEY (channel_id, user_id);

--
-- Name: group_channels group_channels_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.group_channels
    ADD CONSTRAINT group_channels_pkey PRIMARY KEY (id);

--
-- Name: group_channels group_channels_slug_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.group_channels
    ADD CONSTRAINT group_channels_slug_key UNIQUE (slug);

--
-- Name: handles handles_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.handles
    ADD CONSTRAINT handles_pkey PRIMARY KEY (name);

--
-- Name: idea_votes idea_votes_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.idea_votes
    ADD CONSTRAINT idea_votes_pkey PRIMARY KEY (idea_id, user_id);

--
-- Name: link_preview_images link_preview_images_object_key_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.link_preview_images
    ADD CONSTRAINT link_preview_images_object_key_key UNIQUE (object_key);

--
-- Name: link_preview_images link_preview_images_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.link_preview_images
    ADD CONSTRAINT link_preview_images_pkey PRIMARY KEY (source_url_sha256);

--
-- Name: log_config log_config_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.log_config
    ADD CONSTRAINT log_config_pkey PRIMARY KEY (id);

--
-- Name: mailer_settings mailer_settings_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.mailer_settings
    ADD CONSTRAINT mailer_settings_pkey PRIMARY KEY (id);

--
-- Name: manual_unreads manual_unreads_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.manual_unreads
    ADD CONSTRAINT manual_unreads_pkey PRIMARY KEY (user_id, chat_type, chat_id);

--
-- Name: message_deletions message_deletions_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.message_deletions
    ADD CONSTRAINT message_deletions_pkey PRIMARY KEY (user_id, message_id);

--
-- Name: message_edit_history message_edit_history_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.message_edit_history
    ADD CONSTRAINT message_edit_history_pkey PRIMARY KEY (id);

--
-- Name: message_reactions message_reactions_message_id_user_id_emoji_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.message_reactions
    ADD CONSTRAINT message_reactions_message_id_user_id_emoji_key UNIQUE (message_id, user_id, emoji);

--
-- Name: message_reactions message_reactions_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.message_reactions
    ADD CONSTRAINT message_reactions_pkey PRIMARY KEY (id);

--
-- Name: message_search_tokens message_search_tokens_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.message_search_tokens
    ADD CONSTRAINT message_search_tokens_pkey PRIMARY KEY (token_hash, message_id, message_type, token_kind);

--
-- Name: messages messages_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.messages
    ADD CONSTRAINT messages_pkey PRIMARY KEY (id);

--
-- Name: notifications notifications_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.notifications
    ADD CONSTRAINT notifications_pkey PRIMARY KEY (id);

--
-- Name: pinned_chats pinned_chats_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.pinned_chats
    ADD CONSTRAINT pinned_chats_pkey PRIMARY KEY (user_id, chat_type, chat_id);

--
-- Name: pinned_server_rooms pinned_server_rooms_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.pinned_server_rooms
    ADD CONSTRAINT pinned_server_rooms_pkey PRIMARY KEY (user_id, room_id);

--
-- Name: relationships relationships_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.relationships
    ADD CONSTRAINT relationships_pkey PRIMARY KEY (id);

--
-- Name: relationships relationships_user_id_target_id_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.relationships
    ADD CONSTRAINT relationships_user_id_target_id_key UNIQUE (user_id, target_id);

--
-- Name: role_members role_members_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.role_members
    ADD CONSTRAINT role_members_pkey PRIMARY KEY (container_type, container_id, user_id, role_id);

--
-- Name: roles roles_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.roles
    ADD CONSTRAINT roles_pkey PRIMARY KEY (id);

--
-- Name: room_categories room_categories_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.room_categories
    ADD CONSTRAINT room_categories_pkey PRIMARY KEY (id);

--
-- Name: server_bans server_bans_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.server_bans
    ADD CONSTRAINT server_bans_pkey PRIMARY KEY (server_id, user_id);

--
-- Name: server_invites server_invites_code_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.server_invites
    ADD CONSTRAINT server_invites_code_key UNIQUE (code);

--
-- Name: server_invites server_invites_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.server_invites
    ADD CONSTRAINT server_invites_pkey PRIMARY KEY (id);

--
-- Name: server_members server_members_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.server_members
    ADD CONSTRAINT server_members_pkey PRIMARY KEY (server_id, user_id);

--
-- Name: server_moderation_log server_moderation_log_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.server_moderation_log
    ADD CONSTRAINT server_moderation_log_pkey PRIMARY KEY (id);

--
-- Name: server_mutes server_mutes_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.server_mutes
    ADD CONSTRAINT server_mutes_pkey PRIMARY KEY (id);

--
-- Name: server_mutes server_mutes_server_id_user_id_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.server_mutes
    ADD CONSTRAINT server_mutes_server_id_user_id_key UNIQUE (server_id, user_id);

--
-- Name: server_tags server_tags_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.server_tags
    ADD CONSTRAINT server_tags_pkey PRIMARY KEY (server_id, tag);

--
-- Name: servers servers_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.servers
    ADD CONSTRAINT servers_pkey PRIMARY KEY (id);

--
-- Name: servers servers_slug_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.servers
    ADD CONSTRAINT servers_slug_key UNIQUE (slug);

--
-- Name: sfu_nodes sfu_nodes_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.sfu_nodes
    ADD CONSTRAINT sfu_nodes_pkey PRIMARY KEY (node_id);

--
-- Name: spaces spaces_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.spaces
    ADD CONSTRAINT spaces_pkey PRIMARY KEY (id);

--
-- Name: spaces spaces_primary_handle_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.spaces
    ADD CONSTRAINT spaces_primary_handle_key UNIQUE (primary_handle);

--
-- Name: stream_reports stream_reports_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.stream_reports
    ADD CONSTRAINT stream_reports_pkey PRIMARY KEY (id);

--
-- Name: user_consents user_consents_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.user_consents
    ADD CONSTRAINT user_consents_pkey PRIMARY KEY (id);

--
-- Name: user_reports user_reports_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.user_reports
    ADD CONSTRAINT user_reports_pkey PRIMARY KEY (id);

--
-- Name: user_sessions user_sessions_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.user_sessions
    ADD CONSTRAINT user_sessions_pkey PRIMARY KEY (id);

--
-- Name: user_settings user_settings_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.user_settings
    ADD CONSTRAINT user_settings_pkey PRIMARY KEY (user_id);

--
-- Name: user_streak_days user_streak_days_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.user_streak_days
    ADD CONSTRAINT user_streak_days_pkey PRIMARY KEY (user_id, day);

--
-- Name: user_streaks user_streaks_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.user_streaks
    ADD CONSTRAINT user_streaks_pkey PRIMARY KEY (user_id);

--
-- Name: user_themes user_themes_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.user_themes
    ADD CONSTRAINT user_themes_pkey PRIMARY KEY (id);

--
-- Name: users users_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.users
    ADD CONSTRAINT users_pkey PRIMARY KEY (id);

--
-- Name: users users_username_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.users
    ADD CONSTRAINT users_username_key UNIQUE (username);

--
-- Name: voice_channel_settings voice_channel_settings_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.voice_channel_settings
    ADD CONSTRAINT voice_channel_settings_pkey PRIMARY KEY (channel_id);

--
-- Name: voice_room_pins voice_room_pins_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.voice_room_pins
    ADD CONSTRAINT voice_room_pins_pkey PRIMARY KEY (channel_id);

--
-- Name: ws_tickets ws_tickets_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.ws_tickets
    ADD CONSTRAINT ws_tickets_pkey PRIMARY KEY (id);

--
-- Name: drafts_user_id_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX drafts_user_id_idx ON public.drafts USING btree (user_id);

--
-- Name: files_sha256_user_verified_uniq; Type: INDEX; Schema: public; Owner: -
--

CREATE UNIQUE INDEX files_sha256_user_verified_uniq ON public.files USING btree (sha256, uploaded_by) WHERE (verified = true);

--
-- Name: idx_admin_audit_action_ts; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_admin_audit_action_ts ON public.admin_audit_log USING btree (action, ts DESC);

--
-- Name: idx_admin_audit_admin_ts; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_admin_audit_admin_ts ON public.admin_audit_log USING btree (admin_id, ts DESC);

--
-- Name: idx_admin_audit_target; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_admin_audit_target ON public.admin_audit_log USING btree (target_type, target_id, ts DESC);

--
-- Name: idx_admin_audit_ts; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_admin_audit_ts ON public.admin_audit_log USING btree (ts DESC);

--
-- Name: idx_auth_lockouts_locked_until; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_auth_lockouts_locked_until ON public.auth_lockouts USING btree (locked_until) WHERE (locked_until IS NOT NULL);

--
-- Name: idx_auth_lockouts_updated_at; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_auth_lockouts_updated_at ON public.auth_lockouts USING btree (updated_at) WHERE (locked_until IS NULL);

--
-- Name: idx_broadcast_post_meta_pinned; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_broadcast_post_meta_pinned ON public.broadcast_post_meta USING btree (message_id) WHERE pinned;

--
-- Name: idx_bug_ticket_replies_ticket; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_bug_ticket_replies_ticket ON public.bug_ticket_replies USING btree (ticket_id, created_at);

--
-- Name: idx_bug_tickets_status; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_bug_tickets_status ON public.bug_tickets USING btree (status, created_at DESC);

--
-- Name: idx_bug_tickets_type_status; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_bug_tickets_type_status ON public.bug_tickets USING btree (ticket_type, status, created_at DESC);

--
-- Name: idx_bug_tickets_user; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_bug_tickets_user ON public.bug_tickets USING btree (user_id, created_at DESC);

--
-- Name: idx_channel_invites_channel; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_channel_invites_channel ON public.channel_invites USING btree (channel_id);

--
-- Name: idx_channel_invites_code; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_channel_invites_code ON public.channel_invites USING btree (code);

--
-- Name: idx_channel_messages_attachment_s3_key; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_channel_messages_attachment_s3_key ON public.channel_messages USING btree (attachment_s3_key) WHERE (attachment_s3_key IS NOT NULL);

--
-- Name: idx_channel_messages_channel; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_channel_messages_channel ON public.channel_messages USING btree (channel_id, created_at);

--
-- Name: idx_channel_messages_file_id; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_channel_messages_file_id ON public.channel_messages USING btree (file_id) WHERE (file_id IS NOT NULL);

--
-- Name: idx_channel_messages_group; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_channel_messages_group ON public.channel_messages USING btree (channel_id, group_id) WHERE (group_id IS NOT NULL);

--
-- Name: idx_channel_messages_mentions; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_channel_messages_mentions ON public.channel_messages USING gin (mentioned_user_ids);

--
-- Name: idx_channel_messages_mentions_everyone; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_channel_messages_mentions_everyone ON public.channel_messages USING btree (channel_id, created_at) WHERE mentions_everyone;

--
-- Name: idx_channel_messages_role_mentions; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_channel_messages_role_mentions ON public.channel_messages USING gin (mentioned_role_ids);

--
-- Name: idx_channel_messages_sender; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_channel_messages_sender ON public.channel_messages USING btree (sender_id);

--
-- Name: idx_channel_messages_sender_links; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_channel_messages_sender_links ON public.channel_messages USING btree (sender_id) WHERE has_link;

--
-- Name: idx_channel_messages_thread; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_channel_messages_thread ON public.channel_messages USING btree (thread_root_id, created_at) WHERE (thread_root_id IS NOT NULL);

--
-- Name: idx_channel_overrides_channel; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_channel_overrides_channel ON public.channel_permission_overrides USING btree (channel_id);

--
-- Name: idx_chat_mutes_expires; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_chat_mutes_expires ON public.chat_mutes USING btree (mute_expires_at) WHERE (mute_expires_at IS NOT NULL);

--
-- Name: idx_chat_mutes_user; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_chat_mutes_user ON public.chat_mutes USING btree (user_id);

--
-- Name: idx_conv_participants_lookup; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_conv_participants_lookup ON public.conversation_participants USING btree (user_id, conversation_id);

--
-- Name: idx_conv_participants_user; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_conv_participants_user ON public.conversation_participants USING btree (user_id);

--
-- Name: idx_email_delivery_events_job; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_email_delivery_events_job ON public.email_delivery_events USING btree (job_id, event_time DESC);

--
-- Name: idx_email_delivery_events_recipient; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_email_delivery_events_recipient ON public.email_delivery_events USING btree (recipient, event_time DESC);

--
-- Name: idx_email_delivery_events_status; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_email_delivery_events_status ON public.email_delivery_events USING btree (status, event_time DESC);

--
-- Name: idx_email_send_log_provider; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_email_send_log_provider ON public.email_send_log USING btree (provider, sent_at DESC);

--
-- Name: idx_email_send_log_recipient; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_email_send_log_recipient ON public.email_send_log USING btree (recipient, sent_at DESC);

--
-- Name: idx_email_send_log_sent_at_desc; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_email_send_log_sent_at_desc ON public.email_send_log USING btree (sent_at DESC);

--
-- Name: idx_email_send_log_status; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_email_send_log_status ON public.email_send_log USING btree (status) WHERE (status = 'failed'::text);

--
-- Name: idx_email_verifications_email_purpose_created; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_email_verifications_email_purpose_created ON public.email_verifications USING btree (email, purpose, created_at DESC);

--
-- Name: idx_email_verifications_expires; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_email_verifications_expires ON public.email_verifications USING btree (expires_at);

--
-- Name: idx_email_verifications_jti; Type: INDEX; Schema: public; Owner: -
--

CREATE UNIQUE INDEX idx_email_verifications_jti ON public.email_verifications USING btree (verification_token_jti) WHERE (verification_token_jti IS NOT NULL);

--
-- Name: idx_files_category; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_files_category ON public.files USING btree (file_category);

--
-- Name: idx_files_micro_s3_key; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_files_micro_s3_key ON public.files USING btree (micro_s3_key) WHERE (micro_s3_key IS NOT NULL);

--
-- Name: idx_files_original_sha256; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_files_original_sha256 ON public.files USING btree (original_sha256) WHERE (original_sha256 IS NOT NULL);

--
-- Name: idx_files_s3_key; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_files_s3_key ON public.files USING btree (s3_key);

--
-- Name: idx_files_sha256; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_files_sha256 ON public.files USING btree (sha256);

--
-- Name: idx_files_sha256_lookup; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_files_sha256_lookup ON public.files USING btree (sha256, uploaded_by);

--
-- Name: idx_files_thumb_s3_key; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_files_thumb_s3_key ON public.files USING btree (thumb_s3_key) WHERE (thumb_s3_key IS NOT NULL);

--
-- Name: idx_files_unverified; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_files_unverified ON public.files USING btree (id) WHERE (verified = false);

--
-- Name: idx_files_uploaded_by; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_files_uploaded_by ON public.files USING btree (uploaded_by);

--
-- Name: idx_fraud_scoring_log_user; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_fraud_scoring_log_user ON public.fraud_scoring_log USING btree (user_id);

--
-- Name: idx_group_channel_members_user; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_group_channel_members_user ON public.group_channel_members USING btree (user_id);

--
-- Name: idx_group_channels_avatar_s3_key; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_group_channels_avatar_s3_key ON public.group_channels USING btree (avatar_s3_key) WHERE (avatar_s3_key IS NOT NULL);

--
-- Name: idx_group_channels_avatar_thumb_s3_key; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_group_channels_avatar_thumb_s3_key ON public.group_channels USING btree (avatar_thumb_s3_key) WHERE (avatar_thumb_s3_key IS NOT NULL);

--
-- Name: idx_group_channels_real; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_group_channels_real ON public.group_channels USING btree (id) WHERE (NOT is_synthetic);

--
-- Name: idx_group_channels_server; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_group_channels_server ON public.group_channels USING btree (server_id);

--
-- Name: idx_group_channels_slug; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_group_channels_slug ON public.group_channels USING btree (slug);

--
-- Name: idx_handles_owner; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_handles_owner ON public.handles USING btree (owner_type, owner_id);

--
-- Name: idx_idea_votes_idea; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_idea_votes_idea ON public.idea_votes USING btree (idea_id);

--
-- Name: idx_idea_votes_user; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_idea_votes_user ON public.idea_votes USING btree (user_id, updated_at DESC);

--
-- Name: idx_link_preview_images_last_seen; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_link_preview_images_last_seen ON public.link_preview_images USING btree (last_seen_at);

--
-- Name: idx_manual_unreads_user; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_manual_unreads_user ON public.manual_unreads USING btree (user_id);

--
-- Name: idx_message_deletions_user; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_message_deletions_user ON public.message_deletions USING btree (user_id);

--
-- Name: idx_message_edit_history_message; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_message_edit_history_message ON public.message_edit_history USING btree (kind, message_id, edited_at DESC);

--
-- Name: idx_messages_attachment_s3_key; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_messages_attachment_s3_key ON public.messages USING btree (attachment_s3_key) WHERE (attachment_s3_key IS NOT NULL);

--
-- Name: idx_messages_conversation; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_messages_conversation ON public.messages USING btree (conversation_id, created_at);

--
-- Name: idx_messages_file_id; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_messages_file_id ON public.messages USING btree (file_id) WHERE (file_id IS NOT NULL);

--
-- Name: idx_messages_group; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_messages_group ON public.messages USING btree (conversation_id, group_id) WHERE (group_id IS NOT NULL);

--
-- Name: idx_messages_mentions; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_messages_mentions ON public.messages USING gin (mentioned_user_ids);

--
-- Name: idx_messages_mentions_everyone; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_messages_mentions_everyone ON public.messages USING btree (conversation_id, created_at) WHERE mentions_everyone;

--
-- Name: idx_messages_role_mentions; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_messages_role_mentions ON public.messages USING gin (mentioned_role_ids);

--
-- Name: idx_messages_sender; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_messages_sender ON public.messages USING btree (sender_id);

--
-- Name: idx_messages_sender_time; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_messages_sender_time ON public.messages USING btree (sender_id, created_at);

--
-- Name: idx_msg_search_by_message; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_msg_search_by_message ON public.message_search_tokens USING btree (message_id, message_type);

--
-- Name: idx_msg_search_lookup; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_msg_search_lookup ON public.message_search_tokens USING btree (token_hash, scope_id, created_at DESC);

--
-- Name: idx_notifications_user_created; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_notifications_user_created ON public.notifications USING btree (user_id, created_at DESC);

--
-- Name: idx_notifications_user_unread; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_notifications_user_unread ON public.notifications USING btree (user_id) WHERE (read = false);

--
-- Name: idx_pinned_chats_user; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_pinned_chats_user ON public.pinned_chats USING btree (user_id);

--
-- Name: idx_pinned_server_rooms_user; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_pinned_server_rooms_user ON public.pinned_server_rooms USING btree (user_id);

--
-- Name: idx_reactions_message; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_reactions_message ON public.message_reactions USING btree (message_id);

--
-- Name: idx_reactions_user; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_reactions_user ON public.message_reactions USING btree (user_id);

--
-- Name: idx_relationships_target; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_relationships_target ON public.relationships USING btree (target_id, type);

--
-- Name: idx_relationships_user; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_relationships_user ON public.relationships USING btree (user_id, type);

--
-- Name: idx_role_members_user; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_role_members_user ON public.role_members USING btree (container_type, container_id, user_id);

--
-- Name: idx_roles_container; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_roles_container ON public.roles USING btree (container_type, container_id, "position" DESC);

--
-- Name: idx_room_categories_server; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_room_categories_server ON public.room_categories USING btree (server_id, "position");

--
-- Name: idx_server_bans_user; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_server_bans_user ON public.server_bans USING btree (user_id);

--
-- Name: idx_server_invites_code; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_server_invites_code ON public.server_invites USING btree (code);

--
-- Name: idx_server_members_user; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_server_members_user ON public.server_members USING btree (user_id);

--
-- Name: idx_server_moderation_log_server; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_server_moderation_log_server ON public.server_moderation_log USING btree (server_id, id DESC);

--
-- Name: idx_server_moderation_log_target; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_server_moderation_log_target ON public.server_moderation_log USING btree (server_id, target_id, id DESC);

--
-- Name: idx_server_mutes_expires; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_server_mutes_expires ON public.server_mutes USING btree (expires_at) WHERE (expires_at IS NOT NULL);

--
-- Name: idx_server_mutes_lookup; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_server_mutes_lookup ON public.server_mutes USING btree (server_id, user_id);

--
-- Name: idx_server_tags_tag; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_server_tags_tag ON public.server_tags USING btree (tag);

--
-- Name: idx_servers_avatar_s3_key; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_servers_avatar_s3_key ON public.servers USING btree (avatar_s3_key) WHERE (avatar_s3_key IS NOT NULL);

--
-- Name: idx_servers_banner_s3_key; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_servers_banner_s3_key ON public.servers USING btree (banner_s3_key) WHERE (banner_s3_key IS NOT NULL);

--
-- Name: idx_servers_real; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_servers_real ON public.servers USING btree (id) WHERE (NOT is_synthetic);

--
-- Name: idx_servers_slug; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_servers_slug ON public.servers USING btree (slug);

--
-- Name: idx_sfu_nodes_region_status; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_sfu_nodes_region_status ON public.sfu_nodes USING btree (region, status, last_heartbeat DESC);

--
-- Name: idx_sfu_nodes_status_heartbeat; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_sfu_nodes_status_heartbeat ON public.sfu_nodes USING btree (status, last_heartbeat DESC);

--
-- Name: idx_spaces_owner; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_spaces_owner ON public.spaces USING btree (owner_id);

--
-- Name: idx_stream_reports_reporter_created; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_stream_reports_reporter_created ON public.stream_reports USING btree (reporter_id, created_at DESC);

--
-- Name: idx_stream_reports_sharer_created; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_stream_reports_sharer_created ON public.stream_reports USING btree (sharer_id, created_at DESC) WHERE (sharer_id IS NOT NULL);

--
-- Name: idx_stream_reports_status_created; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_stream_reports_status_created ON public.stream_reports USING btree (status, created_at DESC);

--
-- Name: idx_user_consents_user; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_user_consents_user ON public.user_consents USING btree (user_id, accepted_at DESC);

--
-- Name: idx_user_reports_category_status; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_user_reports_category_status ON public.user_reports USING btree (category, status, created_at DESC);

--
-- Name: idx_user_reports_reporter_created; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_user_reports_reporter_created ON public.user_reports USING btree (reporter_id, created_at DESC);

--
-- Name: idx_user_reports_status_created; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_user_reports_status_created ON public.user_reports USING btree (status, created_at DESC);

--
-- Name: idx_user_reports_target_created; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_user_reports_target_created ON public.user_reports USING btree (target_id, created_at DESC);

--
-- Name: idx_user_sessions_expires_at; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_user_sessions_expires_at ON public.user_sessions USING btree (expires_at);

--
-- Name: idx_user_sessions_revoked_at; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_user_sessions_revoked_at ON public.user_sessions USING btree (revoked_at) WHERE (revoked_at IS NOT NULL);

--
-- Name: idx_user_sessions_user_active; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_user_sessions_user_active ON public.user_sessions USING btree (user_id, last_used_at DESC) WHERE (revoked_at IS NULL);

--
-- Name: idx_user_streak_days_day; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_user_streak_days_day ON public.user_streak_days USING btree (day DESC);

--
-- Name: idx_user_themes_user_id; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_user_themes_user_id ON public.user_themes USING btree (user_id);

--
-- Name: idx_users_avatar_s3_key; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_users_avatar_s3_key ON public.users USING btree (avatar_s3_key) WHERE (avatar_s3_key IS NOT NULL);

--
-- Name: idx_users_avatar_thumb_s3_key; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_users_avatar_thumb_s3_key ON public.users USING btree (avatar_thumb_s3_key) WHERE (avatar_thumb_s3_key IS NOT NULL);

--
-- Name: idx_users_deletion_requested_at; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_users_deletion_requested_at ON public.users USING btree (deletion_requested_at) WHERE (deletion_requested_at IS NOT NULL);

--
-- Name: idx_users_email_lower; Type: INDEX; Schema: public; Owner: -
--

CREATE UNIQUE INDEX idx_users_email_lower ON public.users USING btree (lower((email)::text)) WHERE (email IS NOT NULL);

--
-- Name: idx_users_username; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_users_username ON public.users USING btree (username);

--
-- Name: idx_voice_room_pins_expires; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_voice_room_pins_expires ON public.voice_room_pins USING btree (expires_at);

--
-- Name: idx_ws_tickets_expires_at; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_ws_tickets_expires_at ON public.ws_tickets USING btree (expires_at);

--
-- Name: log_config_expires; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX log_config_expires ON public.log_config USING btree (expires_at) WHERE (expires_at IS NOT NULL);

--
-- Name: log_config_global_cat; Type: INDEX; Schema: public; Owner: -
--

CREATE UNIQUE INDEX log_config_global_cat ON public.log_config USING btree (category) WHERE (scope = 'global'::text);

--
-- Name: log_config_user_cat; Type: INDEX; Schema: public; Owner: -
--

CREATE UNIQUE INDEX log_config_user_cat ON public.log_config USING btree (user_id, category) WHERE (scope = 'user'::text);

--
-- Name: uniq_channel_overrides_everyone; Type: INDEX; Schema: public; Owner: -
--

CREATE UNIQUE INDEX uniq_channel_overrides_everyone ON public.channel_permission_overrides USING btree (channel_id) WHERE ((holder_type)::text = 'everyone'::text);

--
-- Name: uniq_channel_overrides_holder; Type: INDEX; Schema: public; Owner: -
--

CREATE UNIQUE INDEX uniq_channel_overrides_holder ON public.channel_permission_overrides USING btree (channel_id, holder_type, holder_id) WHERE (holder_id IS NOT NULL);

--
-- Name: uniq_email_delivery_events_event; Type: INDEX; Schema: public; Owner: -
--

CREATE UNIQUE INDEX uniq_email_delivery_events_event ON public.email_delivery_events USING btree (job_id, status, event_time);

--
-- Name: uniq_roles_system; Type: INDEX; Schema: public; Owner: -
--

CREATE UNIQUE INDEX uniq_roles_system ON public.roles USING btree (container_type, container_id, system_key) WHERE (system_key IS NOT NULL);

--
-- Name: uq_channel_messages_mirror_per_channel; Type: INDEX; Schema: public; Owner: -
--

CREATE UNIQUE INDEX uq_channel_messages_mirror_per_channel ON public.channel_messages USING btree (channel_id, mirror_of) WHERE (mirror_of IS NOT NULL);

--
-- Name: uq_channel_messages_sender_client; Type: INDEX; Schema: public; Owner: -
--

CREATE UNIQUE INDEX uq_channel_messages_sender_client ON public.channel_messages USING btree (sender_id, client_message_id) WHERE (client_message_id IS NOT NULL);

--
-- Name: uq_group_channels_discussion_per_server; Type: INDEX; Schema: public; Owner: -
--

CREATE UNIQUE INDEX uq_group_channels_discussion_per_server ON public.group_channels USING btree (server_id) WHERE is_discussion;

--
-- Name: uq_messages_sender_client; Type: INDEX; Schema: public; Owner: -
--

CREATE UNIQUE INDEX uq_messages_sender_client ON public.messages USING btree (sender_id, client_message_id) WHERE (client_message_id IS NOT NULL);

--
-- Name: uq_spaces_broadcast_channel; Type: INDEX; Schema: public; Owner: -
--

CREATE UNIQUE INDEX uq_spaces_broadcast_channel ON public.spaces USING btree (broadcast_channel_id) WHERE (broadcast_channel_id IS NOT NULL);

--
-- Name: uq_spaces_server; Type: INDEX; Schema: public; Owner: -
--

CREATE UNIQUE INDEX uq_spaces_server ON public.spaces USING btree (server_id) WHERE (server_id IS NOT NULL);

--
-- Name: admin_audit_log admin_audit_log_no_update; Type: TRIGGER; Schema: public; Owner: -
--

CREATE TRIGGER admin_audit_log_no_update BEFORE DELETE OR UPDATE ON public.admin_audit_log FOR EACH ROW EXECUTE FUNCTION public.admin_audit_log_append_only_guard();

--
-- Name: message_edit_history message_edit_history_no_update; Type: TRIGGER; Schema: public; Owner: -
--

CREATE TRIGGER message_edit_history_no_update BEFORE DELETE OR UPDATE ON public.message_edit_history FOR EACH ROW EXECUTE FUNCTION public.reject_message_edit_history_mutation();

--
-- Name: bug_tickets trg_set_idea_published_at; Type: TRIGGER; Schema: public; Owner: -
--

CREATE TRIGGER trg_set_idea_published_at BEFORE INSERT OR UPDATE ON public.bug_tickets FOR EACH ROW EXECUTE FUNCTION public.set_idea_published_at();

--
-- Name: user_consents user_consents_no_update; Type: TRIGGER; Schema: public; Owner: -
--

CREATE TRIGGER user_consents_no_update BEFORE DELETE OR UPDATE ON public.user_consents FOR EACH ROW EXECUTE FUNCTION public.reject_user_consents_mutation();

--
-- Name: admin_audit_log admin_audit_log_admin_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.admin_audit_log
    ADD CONSTRAINT admin_audit_log_admin_id_fkey FOREIGN KEY (admin_id) REFERENCES public.users(id) ON DELETE SET NULL;

--
-- Name: admin_credentials admin_credentials_user_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.admin_credentials
    ADD CONSTRAINT admin_credentials_user_id_fkey FOREIGN KEY (user_id) REFERENCES public.users(id) ON DELETE CASCADE;

--
-- Name: broadcast_post_meta broadcast_post_meta_message_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.broadcast_post_meta
    ADD CONSTRAINT broadcast_post_meta_message_id_fkey FOREIGN KEY (message_id) REFERENCES public.channel_messages(id) ON DELETE CASCADE;

--
-- Name: bug_ticket_replies bug_ticket_replies_author_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.bug_ticket_replies
    ADD CONSTRAINT bug_ticket_replies_author_id_fkey FOREIGN KEY (author_id) REFERENCES public.users(id) ON DELETE SET NULL;

--
-- Name: bug_ticket_replies bug_ticket_replies_ticket_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.bug_ticket_replies
    ADD CONSTRAINT bug_ticket_replies_ticket_id_fkey FOREIGN KEY (ticket_id) REFERENCES public.bug_tickets(id) ON DELETE CASCADE;

--
-- Name: bug_tickets bug_tickets_user_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.bug_tickets
    ADD CONSTRAINT bug_tickets_user_id_fkey FOREIGN KEY (user_id) REFERENCES public.users(id) ON DELETE CASCADE;

--
-- Name: channel_invites channel_invites_channel_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.channel_invites
    ADD CONSTRAINT channel_invites_channel_id_fkey FOREIGN KEY (channel_id) REFERENCES public.group_channels(id) ON DELETE CASCADE;

--
-- Name: channel_invites channel_invites_creator_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.channel_invites
    ADD CONSTRAINT channel_invites_creator_id_fkey FOREIGN KEY (creator_id) REFERENCES public.users(id) ON DELETE CASCADE;

--
-- Name: channel_messages channel_messages_channel_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.channel_messages
    ADD CONSTRAINT channel_messages_channel_id_fkey FOREIGN KEY (channel_id) REFERENCES public.group_channels(id) ON DELETE CASCADE;

--
-- Name: channel_messages channel_messages_file_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.channel_messages
    ADD CONSTRAINT channel_messages_file_id_fkey FOREIGN KEY (file_id) REFERENCES public.files(id) ON DELETE SET NULL;

--
-- Name: channel_messages channel_messages_mirror_of_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.channel_messages
    ADD CONSTRAINT channel_messages_mirror_of_fkey FOREIGN KEY (mirror_of) REFERENCES public.channel_messages(id) ON DELETE CASCADE;

--
-- Name: channel_messages channel_messages_sender_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.channel_messages
    ADD CONSTRAINT channel_messages_sender_id_fkey FOREIGN KEY (sender_id) REFERENCES public.users(id) ON DELETE SET NULL;

--
-- Name: channel_messages channel_messages_thread_root_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.channel_messages
    ADD CONSTRAINT channel_messages_thread_root_id_fkey FOREIGN KEY (thread_root_id) REFERENCES public.channel_messages(id) ON DELETE SET NULL;

--
-- Name: channel_permission_overrides channel_permission_overrides_channel_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.channel_permission_overrides
    ADD CONSTRAINT channel_permission_overrides_channel_id_fkey FOREIGN KEY (channel_id) REFERENCES public.group_channels(id) ON DELETE CASCADE;

--
-- Name: channel_read_state channel_read_state_channel_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.channel_read_state
    ADD CONSTRAINT channel_read_state_channel_id_fkey FOREIGN KEY (channel_id) REFERENCES public.group_channels(id) ON DELETE CASCADE;

--
-- Name: channel_read_state channel_read_state_user_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.channel_read_state
    ADD CONSTRAINT channel_read_state_user_id_fkey FOREIGN KEY (user_id) REFERENCES public.users(id) ON DELETE CASCADE;

--
-- Name: chat_mutes chat_mutes_user_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.chat_mutes
    ADD CONSTRAINT chat_mutes_user_id_fkey FOREIGN KEY (user_id) REFERENCES public.users(id) ON DELETE CASCADE;

--
-- Name: conversation_participants conversation_participants_conversation_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.conversation_participants
    ADD CONSTRAINT conversation_participants_conversation_id_fkey FOREIGN KEY (conversation_id) REFERENCES public.conversations(id) ON DELETE CASCADE;

--
-- Name: conversation_participants conversation_participants_user_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.conversation_participants
    ADD CONSTRAINT conversation_participants_user_id_fkey FOREIGN KEY (user_id) REFERENCES public.users(id) ON DELETE CASCADE;

--
-- Name: drafts drafts_user_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.drafts
    ADD CONSTRAINT drafts_user_id_fkey FOREIGN KEY (user_id) REFERENCES public.users(id) ON DELETE CASCADE;

--
-- Name: email_send_log email_send_log_sent_by_admin_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.email_send_log
    ADD CONSTRAINT email_send_log_sent_by_admin_id_fkey FOREIGN KEY (sent_by_admin_id) REFERENCES public.users(id) ON DELETE SET NULL;

--
-- Name: files files_uploaded_by_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.files
    ADD CONSTRAINT files_uploaded_by_fkey FOREIGN KEY (uploaded_by) REFERENCES public.users(id) ON DELETE CASCADE;

--
-- Name: fraud_scoring_log fraud_scoring_log_user_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.fraud_scoring_log
    ADD CONSTRAINT fraud_scoring_log_user_id_fkey FOREIGN KEY (user_id) REFERENCES public.users(id);

--
-- Name: group_channel_members group_channel_members_channel_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.group_channel_members
    ADD CONSTRAINT group_channel_members_channel_id_fkey FOREIGN KEY (channel_id) REFERENCES public.group_channels(id) ON DELETE CASCADE;

--
-- Name: group_channel_members group_channel_members_user_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.group_channel_members
    ADD CONSTRAINT group_channel_members_user_id_fkey FOREIGN KEY (user_id) REFERENCES public.users(id) ON DELETE CASCADE;

--
-- Name: group_channels group_channels_category_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.group_channels
    ADD CONSTRAINT group_channels_category_id_fkey FOREIGN KEY (category_id) REFERENCES public.room_categories(id) ON DELETE SET NULL;

--
-- Name: group_channels group_channels_owner_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.group_channels
    ADD CONSTRAINT group_channels_owner_id_fkey FOREIGN KEY (owner_id) REFERENCES public.users(id) ON DELETE CASCADE;

--
-- Name: group_channels group_channels_server_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.group_channels
    ADD CONSTRAINT group_channels_server_id_fkey FOREIGN KEY (server_id) REFERENCES public.servers(id) ON DELETE SET NULL;

--
-- Name: idea_votes idea_votes_idea_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.idea_votes
    ADD CONSTRAINT idea_votes_idea_id_fkey FOREIGN KEY (idea_id) REFERENCES public.bug_tickets(id) ON DELETE CASCADE;

--
-- Name: idea_votes idea_votes_user_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.idea_votes
    ADD CONSTRAINT idea_votes_user_id_fkey FOREIGN KEY (user_id) REFERENCES public.users(id) ON DELETE CASCADE;

--
-- Name: mailer_settings mailer_settings_updated_by_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.mailer_settings
    ADD CONSTRAINT mailer_settings_updated_by_fkey FOREIGN KEY (updated_by) REFERENCES public.users(id) ON DELETE SET NULL;

--
-- Name: manual_unreads manual_unreads_user_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.manual_unreads
    ADD CONSTRAINT manual_unreads_user_id_fkey FOREIGN KEY (user_id) REFERENCES public.users(id) ON DELETE CASCADE;

--
-- Name: message_deletions message_deletions_user_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.message_deletions
    ADD CONSTRAINT message_deletions_user_id_fkey FOREIGN KEY (user_id) REFERENCES public.users(id) ON DELETE CASCADE;

--
-- Name: message_reactions message_reactions_user_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.message_reactions
    ADD CONSTRAINT message_reactions_user_id_fkey FOREIGN KEY (user_id) REFERENCES public.users(id) ON DELETE CASCADE;

--
-- Name: messages messages_conversation_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.messages
    ADD CONSTRAINT messages_conversation_id_fkey FOREIGN KEY (conversation_id) REFERENCES public.conversations(id) ON DELETE CASCADE;

--
-- Name: messages messages_file_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.messages
    ADD CONSTRAINT messages_file_id_fkey FOREIGN KEY (file_id) REFERENCES public.files(id) ON DELETE SET NULL;

--
-- Name: messages messages_sender_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.messages
    ADD CONSTRAINT messages_sender_id_fkey FOREIGN KEY (sender_id) REFERENCES public.users(id) ON DELETE SET NULL;

--
-- Name: notifications notifications_actor_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.notifications
    ADD CONSTRAINT notifications_actor_id_fkey FOREIGN KEY (actor_id) REFERENCES public.users(id) ON DELETE SET NULL;

--
-- Name: notifications notifications_user_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.notifications
    ADD CONSTRAINT notifications_user_id_fkey FOREIGN KEY (user_id) REFERENCES public.users(id) ON DELETE CASCADE;

--
-- Name: pinned_chats pinned_chats_user_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.pinned_chats
    ADD CONSTRAINT pinned_chats_user_id_fkey FOREIGN KEY (user_id) REFERENCES public.users(id) ON DELETE CASCADE;

--
-- Name: pinned_server_rooms pinned_server_rooms_room_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.pinned_server_rooms
    ADD CONSTRAINT pinned_server_rooms_room_id_fkey FOREIGN KEY (room_id) REFERENCES public.group_channels(id) ON DELETE CASCADE;

--
-- Name: pinned_server_rooms pinned_server_rooms_server_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.pinned_server_rooms
    ADD CONSTRAINT pinned_server_rooms_server_id_fkey FOREIGN KEY (server_id) REFERENCES public.servers(id) ON DELETE CASCADE;

--
-- Name: pinned_server_rooms pinned_server_rooms_user_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.pinned_server_rooms
    ADD CONSTRAINT pinned_server_rooms_user_id_fkey FOREIGN KEY (user_id) REFERENCES public.users(id) ON DELETE CASCADE;

--
-- Name: relationships relationships_target_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.relationships
    ADD CONSTRAINT relationships_target_id_fkey FOREIGN KEY (target_id) REFERENCES public.users(id);

--
-- Name: relationships relationships_user_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.relationships
    ADD CONSTRAINT relationships_user_id_fkey FOREIGN KEY (user_id) REFERENCES public.users(id);

--
-- Name: role_members role_members_role_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.role_members
    ADD CONSTRAINT role_members_role_id_fkey FOREIGN KEY (role_id) REFERENCES public.roles(id) ON DELETE CASCADE;

--
-- Name: role_members role_members_user_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.role_members
    ADD CONSTRAINT role_members_user_id_fkey FOREIGN KEY (user_id) REFERENCES public.users(id) ON DELETE CASCADE;

--
-- Name: room_categories room_categories_server_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.room_categories
    ADD CONSTRAINT room_categories_server_id_fkey FOREIGN KEY (server_id) REFERENCES public.servers(id) ON DELETE CASCADE;

--
-- Name: server_bans server_bans_banned_by_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.server_bans
    ADD CONSTRAINT server_bans_banned_by_fkey FOREIGN KEY (banned_by) REFERENCES public.users(id) ON DELETE SET NULL;

--
-- Name: server_bans server_bans_server_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.server_bans
    ADD CONSTRAINT server_bans_server_id_fkey FOREIGN KEY (server_id) REFERENCES public.servers(id) ON DELETE CASCADE;

--
-- Name: server_bans server_bans_user_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.server_bans
    ADD CONSTRAINT server_bans_user_id_fkey FOREIGN KEY (user_id) REFERENCES public.users(id) ON DELETE CASCADE;

--
-- Name: server_invites server_invites_creator_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.server_invites
    ADD CONSTRAINT server_invites_creator_id_fkey FOREIGN KEY (creator_id) REFERENCES public.users(id) ON DELETE CASCADE;

--
-- Name: server_invites server_invites_server_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.server_invites
    ADD CONSTRAINT server_invites_server_id_fkey FOREIGN KEY (server_id) REFERENCES public.servers(id) ON DELETE CASCADE;

--
-- Name: server_members server_members_server_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.server_members
    ADD CONSTRAINT server_members_server_id_fkey FOREIGN KEY (server_id) REFERENCES public.servers(id) ON DELETE CASCADE;

--
-- Name: server_members server_members_user_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.server_members
    ADD CONSTRAINT server_members_user_id_fkey FOREIGN KEY (user_id) REFERENCES public.users(id) ON DELETE CASCADE;

--
-- Name: server_moderation_log server_moderation_log_actor_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.server_moderation_log
    ADD CONSTRAINT server_moderation_log_actor_id_fkey FOREIGN KEY (actor_id) REFERENCES public.users(id) ON DELETE SET NULL;

--
-- Name: server_moderation_log server_moderation_log_server_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.server_moderation_log
    ADD CONSTRAINT server_moderation_log_server_id_fkey FOREIGN KEY (server_id) REFERENCES public.servers(id) ON DELETE CASCADE;

--
-- Name: server_moderation_log server_moderation_log_target_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.server_moderation_log
    ADD CONSTRAINT server_moderation_log_target_id_fkey FOREIGN KEY (target_id) REFERENCES public.users(id) ON DELETE CASCADE;

--
-- Name: server_mutes server_mutes_muted_by_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.server_mutes
    ADD CONSTRAINT server_mutes_muted_by_fkey FOREIGN KEY (muted_by) REFERENCES public.users(id) ON DELETE SET NULL;

--
-- Name: server_mutes server_mutes_server_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.server_mutes
    ADD CONSTRAINT server_mutes_server_id_fkey FOREIGN KEY (server_id) REFERENCES public.servers(id) ON DELETE CASCADE;

--
-- Name: server_mutes server_mutes_user_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.server_mutes
    ADD CONSTRAINT server_mutes_user_id_fkey FOREIGN KEY (user_id) REFERENCES public.users(id) ON DELETE CASCADE;

--
-- Name: server_tags server_tags_server_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.server_tags
    ADD CONSTRAINT server_tags_server_id_fkey FOREIGN KEY (server_id) REFERENCES public.servers(id) ON DELETE CASCADE;

--
-- Name: servers servers_owner_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.servers
    ADD CONSTRAINT servers_owner_id_fkey FOREIGN KEY (owner_id) REFERENCES public.users(id) ON DELETE CASCADE;

--
-- Name: spaces spaces_broadcast_channel_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.spaces
    ADD CONSTRAINT spaces_broadcast_channel_id_fkey FOREIGN KEY (broadcast_channel_id) REFERENCES public.group_channels(id) ON DELETE SET NULL;

--
-- Name: spaces spaces_owner_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.spaces
    ADD CONSTRAINT spaces_owner_id_fkey FOREIGN KEY (owner_id) REFERENCES public.users(id) ON DELETE CASCADE;

--
-- Name: spaces spaces_server_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.spaces
    ADD CONSTRAINT spaces_server_id_fkey FOREIGN KEY (server_id) REFERENCES public.servers(id) ON DELETE SET NULL;

--
-- Name: stream_reports stream_reports_reporter_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.stream_reports
    ADD CONSTRAINT stream_reports_reporter_id_fkey FOREIGN KEY (reporter_id) REFERENCES public.users(id) ON DELETE CASCADE;

--
-- Name: stream_reports stream_reports_reviewer_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.stream_reports
    ADD CONSTRAINT stream_reports_reviewer_id_fkey FOREIGN KEY (reviewer_id) REFERENCES public.users(id) ON DELETE SET NULL;

--
-- Name: stream_reports stream_reports_sharer_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.stream_reports
    ADD CONSTRAINT stream_reports_sharer_id_fkey FOREIGN KEY (sharer_id) REFERENCES public.users(id) ON DELETE SET NULL;

--
-- Name: user_consents user_consents_user_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.user_consents
    ADD CONSTRAINT user_consents_user_id_fkey FOREIGN KEY (user_id) REFERENCES public.users(id) ON DELETE CASCADE;

--
-- Name: user_reports user_reports_reporter_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.user_reports
    ADD CONSTRAINT user_reports_reporter_id_fkey FOREIGN KEY (reporter_id) REFERENCES public.users(id) ON DELETE CASCADE;

--
-- Name: user_reports user_reports_reviewer_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.user_reports
    ADD CONSTRAINT user_reports_reviewer_id_fkey FOREIGN KEY (reviewer_id) REFERENCES public.users(id) ON DELETE SET NULL;

--
-- Name: user_reports user_reports_target_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.user_reports
    ADD CONSTRAINT user_reports_target_id_fkey FOREIGN KEY (target_id) REFERENCES public.users(id) ON DELETE CASCADE;

--
-- Name: user_sessions user_sessions_user_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.user_sessions
    ADD CONSTRAINT user_sessions_user_id_fkey FOREIGN KEY (user_id) REFERENCES public.users(id) ON DELETE CASCADE;

--
-- Name: user_settings user_settings_user_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.user_settings
    ADD CONSTRAINT user_settings_user_id_fkey FOREIGN KEY (user_id) REFERENCES public.users(id) ON DELETE CASCADE;

--
-- Name: user_streak_days user_streak_days_user_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.user_streak_days
    ADD CONSTRAINT user_streak_days_user_id_fkey FOREIGN KEY (user_id) REFERENCES public.users(id) ON DELETE CASCADE;

--
-- Name: user_streaks user_streaks_user_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.user_streaks
    ADD CONSTRAINT user_streaks_user_id_fkey FOREIGN KEY (user_id) REFERENCES public.users(id) ON DELETE CASCADE;

--
-- Name: user_themes user_themes_user_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.user_themes
    ADD CONSTRAINT user_themes_user_id_fkey FOREIGN KEY (user_id) REFERENCES public.users(id) ON DELETE CASCADE;

--
-- Name: voice_channel_settings voice_channel_settings_updated_by_user_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.voice_channel_settings
    ADD CONSTRAINT voice_channel_settings_updated_by_user_id_fkey FOREIGN KEY (updated_by_user_id) REFERENCES public.users(id) ON DELETE SET NULL;

--
-- Name: voice_room_pins voice_room_pins_pinned_by_user_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.voice_room_pins
    ADD CONSTRAINT voice_room_pins_pinned_by_user_id_fkey FOREIGN KEY (pinned_by_user_id) REFERENCES public.users(id) ON DELETE SET NULL;

--
-- PostgreSQL database dump complete
--

--
-- Seed rows
--
-- A schema-only dump carries no rows, and four of the squashed migrations
-- seeded data the code depends on finding. They are reproduced verbatim from
-- those files; each is ON CONFLICT-guarded, so re-running changes nothing.
--

-- The official support account, «Nekko» with the verified badge. Fixed UUID
-- so internal/support can reference it without a lookup by name; the name is
-- the brand in every language, like Telegram's own service account.
INSERT INTO users (id, username, password_hash, display_name, is_system, privacy_dm)
VALUES (
    '00000000-0000-0000-0000-000000000001',
    'support',
    '!system-no-login!',
    'Nekko',
    true,
    'everyone'
)
ON CONFLICT (id) DO UPDATE
SET username = EXCLUDED.username,
    display_name = EXCLUDED.display_name,
    is_system = EXCLUDED.is_system,
    privacy_dm = EXCLUDED.privacy_dm;

-- Fraud scoring: datacenter patterns and signal weights. These are starting
-- values, not constants — the admin panel edits the rows.
INSERT INTO fraud_datacenter_patterns (pattern, provider) VALUES
    ('amazon',          'Amazon AWS'),
    ('digitalocean',    'DigitalOcean'),
    ('hetzner',         'Hetzner'),
    ('ovh',             'OVH'),
    ('linode',          'Linode / Akamai'),
    ('vultr',           'Vultr'),
    ('google cloud',    'Google Cloud'),
    ('microsoft',       'Microsoft Azure'),
    ('cloudflare',      'Cloudflare'),
    ('fastly',          'Fastly'),
    ('choopa',          'Vultr / Choopa'),
    ('as-colocrossing', 'ColoCrossing'),
    ('serverius',       'Serverius'),
    ('m247',            'M247'),
    ('mullvad',         'Mullvad VPN'),
    ('nordvpn',         'NordVPN'),
    ('expressvpn',      'ExpressVPN')
ON CONFLICT DO NOTHING;

INSERT INTO fraud_scoring_signals (key, label, category, points, sort_order) VALUES
    ('is_datacenter',         'Datacenter IP',            'ip_reputation', 25,  1),
    ('is_vpn',                'VPN detected',             'ip_reputation', 20,  2),
    ('is_proxy',              'Proxy detected',           'ip_reputation', 30,  3),
    ('is_tor',                'Tor exit node',            'ip_reputation', 50,  4),
    ('is_hosting',            'Hosting provider',         'ip_reputation', 15,  5),
    ('pow_not_solved',        'PoW not solved',           'pow',           50,  6),
    ('pow_time_too_fast',     'PoW solved too fast',      'pow',           45,  7),
    ('fingerprint_seen',      'Fingerprint reuse',        'fingerprint',   25,  8),
    ('synthetic_events',      'Synthetic events',         'automation',    40,  9),
    ('webdriver_present',     'Webdriver flag',           'automation',    35, 10),
    ('headless_detected',     'Headless browser',         'automation',    30, 11),
    ('stack_trace_bot',       'Bot stack trace',          'automation',    40, 12),
    ('plugins_inconsistency', 'Plugins mismatch',         'automation',    15, 13),
    ('screen_inconsistency',  'Screen mismatch',          'automation',    10, 14),
    ('mouse_entropy_low',     'Low mouse entropy',        'behavior',      15, 15),
    ('time_on_form_too_short','Form filled too fast',     'behavior',      20, 16),
    ('instant_clicks',        'Instant clicks',           'behavior',      20, 17),
    ('no_mouse_movement',     'No mouse movement',        'behavior',      15, 18),
    ('registration_velocity', 'Reg velocity >5/24h',      'velocity',      20, 19)
ON CONFLICT (key) DO NOTHING;

INSERT INTO fraud_action_thresholds (id, pass_max, sms_verify_max, shadowban_max)
VALUES (1, 30, 60, 80)
ON CONFLICT (id) DO NOTHING;

-- Mailer settings singleton. A NULL active_provider means "fall back to the
-- MAILER_PROVIDER env var"; the code assumes the row exists.
INSERT INTO mailer_settings (id, active_provider)
VALUES (1, NULL)
ON CONFLICT (id) DO NOTHING;
