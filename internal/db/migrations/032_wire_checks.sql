-- What a tester's core really sent, against what it should have
-- (hikkari-client plans/2026-11-07-knob-truth-wire-diagnostics.md, W3).
--
-- The client's load round takes each TCP+TLS profile's ClientHello from a
-- loopback mirror and writes it as a [diag:wire] line
-- (hikkari-client engine/core/engine/singbox/diagnostics/wire.go). The hub
-- reads those lines from the tester log stream into wire_hellos and checks
-- each against two things: the hello this core revision sends for the
-- fingerprint (wire_reference), and the knobs the hub handed out for the node
-- and the tester's network (entry_nodes, entry_node_network_knobs).

CREATE TABLE IF NOT EXISTS wire_hellos (
    id bigserial PRIMARY KEY,
    install_id text NOT NULL,
    -- The client's local time as the line has it: with the install and the
    -- profile, what makes a line one line however often a chunk is read.
    client_ts text NOT NULL,
    measured_at timestamptz NOT NULL,
    version text NOT NULL DEFAULT '',
    platform text NOT NULL DEFAULT '',
    profile text NOT NULL,
    proto text NOT NULL DEFAULT '',
    -- 12 hex of the core's revision; '' when the client did not say.
    core text NOT NULL DEFAULT '',
    network text NOT NULL DEFAULT '',
    -- The knobs the client's config holds for the profile: fp, sni, port, flow, alpn.
    knobs jsonb NOT NULL DEFAULT '{}',
    state text NOT NULL CHECK (state IN ('hello', 'skip', 'error')),
    ja4 text NOT NULL DEFAULT '',
    hk text NOT NULL DEFAULT '',
    alpn_sent text NOT NULL DEFAULT '',
    -- The rest of the line's fields (groups, shares, len, why, ...).
    wire jsonb NOT NULL DEFAULT '{}',
    -- What the checks found, [{code, knob, want, got, msg}]; [] is clean.
    findings jsonb NOT NULL DEFAULT '[]',
    UNIQUE (install_id, client_ts, profile)
);
CREATE INDEX IF NOT EXISTS wire_hellos_time ON wire_hellos (measured_at DESC);
CREATE INDEX IF NOT EXISTS wire_hellos_install_time ON wire_hellos (install_id, measured_at DESC);

-- The hello a core revision sends for a fingerprint name, recorded by the same
-- mirror (hikkari-client diagnostics/wire_record_test.go). fp 'go' is a TLS
-- profile without uTLS: crypto/tls's own hello.
CREATE TABLE IF NOT EXISTS wire_reference (
    core text NOT NULL CHECK (core ~ '^[0-9a-f]{12}$'),
    fp text NOT NULL CHECK (fp ~ '^[a-z0-9_]{1,32}$'),
    hk text NOT NULL CHECK (hk ~ '^h1[0-9a-f]{16}$'),
    ja4 text NOT NULL CHECK (ja4 ~ '^[tq][0-9s][0-9][di][0-9]{4}[0-9a-z]{2}_[0-9a-f]{12}_[0-9a-f]{12}$'),
    source text NOT NULL CHECK (source ~ '^[a-z0-9:._/-]{1,64}$'),
    added_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (core, fp)
);

-- When a node's handed-out knobs last changed. A client takes a new value only
-- with its next subscription refresh, so a line before that is not a mismatch:
-- the checks wait until the change is older than the refresh.
ALTER TABLE entry_nodes ADD COLUMN IF NOT EXISTS knobs_at timestamptz NOT NULL DEFAULT now();

CREATE OR REPLACE FUNCTION entry_nodes_knobs_touch() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    NEW.knobs_at := now();
    RETURN NEW;
END
$$;

DROP TRIGGER IF EXISTS entry_nodes_knobs_at ON entry_nodes;
-- relay_id: a new relay has covers of its own, which decide the link's SNI.
CREATE TRIGGER entry_nodes_knobs_at BEFORE UPDATE ON entry_nodes FOR EACH ROW
    WHEN (OLD.fingerprint IS DISTINCT FROM NEW.fingerprint OR OLD.sni IS DISTINCT FROM NEW.sni
          OR OLD.alpn IS DISTINCT FROM NEW.alpn OR OLD.relay_id IS DISTINCT FROM NEW.relay_id)
    EXECUTE FUNCTION entry_nodes_knobs_touch();

CREATE OR REPLACE FUNCTION entry_node_network_knobs_touch() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF TG_OP = 'DELETE' THEN
        -- The network falls back to the node's own value: that is a change too.
        UPDATE entry_nodes SET knobs_at = now() WHERE slug = OLD.slug;
        RETURN OLD;
    END IF;
    NEW.updated_at := now();
    RETURN NEW;
END
$$;

DROP TRIGGER IF EXISTS entry_node_network_knobs_at ON entry_node_network_knobs;
CREATE TRIGGER entry_node_network_knobs_at BEFORE INSERT OR UPDATE ON entry_node_network_knobs FOR EACH ROW
    EXECUTE FUNCTION entry_node_network_knobs_touch();
DROP TRIGGER IF EXISTS entry_node_network_knobs_gone ON entry_node_network_knobs;
CREATE TRIGGER entry_node_network_knobs_gone AFTER DELETE ON entry_node_network_knobs FOR EACH ROW
    EXECUTE FUNCTION entry_node_network_knobs_touch();
