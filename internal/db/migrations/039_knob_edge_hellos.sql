-- What reached a Reality node: the edge's hello tap counts every ClientHello
-- by the cover it names and its hk, the hash hikkari-client's release test
-- pins for what the core sends (edge/hellotap.go; hikkari-client
-- plans/2026-11-07-knob-truth-wire-diagnostics.md, W5). Totals per edge run
-- (boot), as the pulse sends them: a row only grows until the run ends. No
-- client address is kept.
CREATE TABLE IF NOT EXISTS knob_edge_hellos (
    relay text NOT NULL,
    boot text NOT NULL,
    -- The cover the hello named, '-' for none of the relay's covers.
    cover text NOT NULL CHECK (length(cover) BETWEEN 1 AND 253),
    -- An hk (h1 + 16 hex) or unread, unparsed, other.
    hk text NOT NULL CHECK (hk ~ '^(h1[0-9a-f]{16}|unread|unparsed|other)$'),
    conns bigint NOT NULL CHECK (conns >= 0),
    first_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (relay, boot, cover, hk)
);
CREATE INDEX IF NOT EXISTS knob_edge_hellos_time ON knob_edge_hellos (updated_at DESC);
