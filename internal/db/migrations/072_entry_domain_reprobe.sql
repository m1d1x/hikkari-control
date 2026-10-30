-- 072 — a burned domain gets a second chance (roadmap 42 §11, owner's decision
-- 25.10).
--
-- A name burned by `domain_blocked` (054) used to be final. But it is still
-- ours — bought, sitting on the registrar account, renewed — and a block by
-- name does lift (an operator's mistake, a list moving, an entry expiring).
-- So once a day the hub asks a vantage about its burned names, apart from the
-- routine rounds (a burned name never joins the watch list: that would be
-- noise in the ban tempo and traffic the prober does not need), and a name
-- that reads clean on enough separate days goes back to the pool as 'owned'.
--
-- last_reprobe_at: when the name was last handed to a vantage. Written when
--                  the task is queued, not when it is answered, so "no more
--                  than once a day" holds even when nothing answers.
-- reprobe_streak:  clean reprobes in a row. `domain_blocked` resets it; a
--                  reading that says nothing about the name (network_down,
--                  unknown) leaves it. Back to 0 when the name is returned.

ALTER TABLE entry_domains
    ADD COLUMN last_reprobe_at timestamptz,
    ADD COLUMN reprobe_streak  integer NOT NULL DEFAULT 0 CHECK (reprobe_streak >= 0);

-- The daily pass: burned names, the longest-unasked first.
CREATE INDEX entry_domains_reprobe ON entry_domains (coalesce(last_reprobe_at, burned_at, created_at))
    WHERE status = 'burned';

-- A domain reprobe is a probe task of its own kind: the one live XHTTP relay
-- whose address the names are tried against, and the names in params
-- ({"domains": [...]}); the verdicts come back per name.
ALTER TABLE entry_probe_tasks
    DROP CONSTRAINT entry_probe_tasks_kind_check,
    ADD CONSTRAINT entry_probe_tasks_kind_check CHECK (kind IN ('admission', 'bench', 'domain')),
    ADD CONSTRAINT entry_probe_tasks_domain_shape
        CHECK (kind <> 'domain' OR (cardinality(relays) = 1 AND jsonb_typeof(params -> 'domains') = 'array'));
