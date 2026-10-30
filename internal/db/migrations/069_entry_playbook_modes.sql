-- 069 — the mode of a repair stops being a constant (plan 42 П9.8, owner's
-- decision 25.10).
--
-- Which repairs the hub may run by itself was a Go constant per cause in
-- internal/entry/playbooks.go: free ones `auto`, ones that spend money `ask`.
-- That was the right default (22.10) and the wrong place for it: §4 of the plan
-- has the mode "edited in the console, per cause", П9.8 is exactly the moving
-- of causes to `auto` one at a time, and on the stand 25.10 the owner wanted a
-- real machine bought by the autopilot with money set aside for it — which the
-- code could only give by a rebuild.
--
-- A row here overrides the code's default for one cause; no row means the
-- default. The default stays in the code because it is a judgement about the
-- repair (does it spend), and this table only records an owner's decision to
-- depart from it. Which repair a cause gets is not here: that is the code's,
-- and a mode for a cause the code has no repair for is refused on write.

CREATE TABLE entry_playbook_modes (
    cause      text PRIMARY KEY CHECK (cause <> ''),
    mode       text NOT NULL CHECK (mode IN ('auto', 'ask', 'off')),
    updated_at timestamptz NOT NULL DEFAULT now(),
    -- The admin who decided, or a label for a change made outside the
    -- console. Not a foreign key: the decision outlives the account.
    updated_by text NOT NULL DEFAULT ''
);
