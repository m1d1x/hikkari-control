-- 044 — a new relay build rolls out by itself (roadmap 42 П6.14 Ф1).
--
-- The hub carries the relay binary in its image (П6.15 Ф2) and holds ssh to
-- every box, so a hub started from a newer image pushes the new nekko-edge to
-- the fleet on its own: one canary box first (its relays leave the pool while
-- the build is benchmarked from the RU vantage), then the rest one box at a
-- time, each drained of its holders before it is touched.
--
-- What this needs on record:
--
--   * which build each box runs. One binary per box (/usr/local/bin/nekko-edge
--     serves every relay instance on it), so the version is the box's, and it
--     is per architecture: linux-amd64 and linux-arm64 builds of one commit
--     have different digests. edge_sha is what the hub last saw at that path
--     or put there; NULL = never looked (a box provisioned before this).
--
--   * two more relay states, both "not handed out, still deployed, record
--     kept" — a relay in them came from alive and goes back to alive:
--       testing  — on the canary box while a new build is tried on it;
--       draining — its box is next in the rollout; its holders are moved off
--                  before the binary is swapped.
--     Leaving the pool revokes the relay's live grants with reason `drained`;
--     the slot refilled after that is a `drain_replacement`, not a death —
--     the abuse dataset (П6.7, П6.12) must not read a rollout as a ban.
--
--   * the rollouts: one row per (arch, build). Unique, so a build that failed
--     its canary is remembered as bad and never tried again by itself. At most
--     one rollout per architecture moves at a time.

ALTER TABLE entry_relays
    DROP CONSTRAINT entry_relays_status_check,
    DROP CONSTRAINT entry_relays_alive_has_record,
    ADD CONSTRAINT entry_relays_status_check
        CHECK (status IN ('pending', 'alive', 'testing', 'draining', 'dead')),
    -- Every state a relay reaches only through alive keeps its record: it goes
    -- back to the pool without being admitted again.
    ADD CONSTRAINT entry_relays_alive_has_record
        CHECK (status NOT IN ('alive', 'testing', 'draining') OR envelope_ct IS NOT NULL);

ALTER TABLE entry_grants
    DROP CONSTRAINT entry_grants_trigger_check,
    DROP CONSTRAINT entry_grants_revoke_reason_check,
    ADD CONSTRAINT entry_grants_trigger_check
        CHECK (trigger IN ('initial', 'death_replacement', 'drain_replacement')),
    ADD CONSTRAINT entry_grants_revoke_reason_check
        CHECK (revoke_reason IN ('relay_dead', 'drained'));

ALTER TABLE entry_boxes
    ADD COLUMN arch text CHECK (arch IN ('amd64', 'arm64')),
    ADD COLUMN edge_sha text CHECK (edge_sha ~ '^[0-9a-f]{64}$'),
    ADD COLUMN edge_seen_at timestamptz,
    -- A digest means nothing without the architecture it was built for.
    ADD CONSTRAINT entry_boxes_edge_whole CHECK (
        (edge_sha IS NULL) = (edge_seen_at IS NULL)
        AND (edge_sha IS NULL OR arch IS NOT NULL));

CREATE TABLE entry_rollouts (
    id          uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    arch        text NOT NULL CHECK (arch IN ('amd64', 'arm64')),
    -- sha256 of the nekko-edge in the hub's image for that architecture.
    edge_sha    text NOT NULL CHECK (edge_sha ~ '^[0-9a-f]{64}$'),
    --   canary  — the build is on the canary box, benchmarked before and after;
    --   rolling — the canary passed, the rest of the fleet follows one box at a time;
    --   done    — every live box of this architecture runs the build;
    --   failed  — the canary failed (or a box did): rolled back, build marked bad;
    --   stopped — the owner stopped it.
    stage       text NOT NULL DEFAULT 'canary'
                CHECK (stage IN ('canary', 'rolling', 'done', 'failed', 'stopped')),
    -- The box the build was tried on first. Which box is being worked on now
    -- is not stored: it is the one whose relays are draining.
    canary_box  text REFERENCES entry_boxes (name) ON DELETE RESTRICT,
    error       text,
    created_at  timestamptz NOT NULL DEFAULT now(),
    finished_at timestamptz,
    CONSTRAINT entry_rollouts_one_per_build UNIQUE (arch, edge_sha),
    CONSTRAINT entry_rollouts_finished
        CHECK ((stage IN ('canary', 'rolling')) = (finished_at IS NULL)),
    CONSTRAINT entry_rollouts_failed_says_why
        CHECK ((stage IN ('failed', 'stopped')) = (error IS NOT NULL)),
    CONSTRAINT entry_rollouts_canary_named
        CHECK (stage NOT IN ('rolling', 'done') OR canary_box IS NOT NULL)
);

-- One rollout per architecture moves at a time: two would drain two boxes at
-- once and halve the pool's slack.
CREATE UNIQUE INDEX entry_rollouts_one_moving_per_arch
    ON entry_rollouts (arch) WHERE stage IN ('canary', 'rolling');
