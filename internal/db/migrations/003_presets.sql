-- Routing presets (hikkari-client engine/core/presets). A group is one
-- service of the client's catalog, edited in place; a preset puts groups on
-- a side (past the vpn or through it). A release freezes a preset's groups
-- into the catalog clients fetch, so editing a group never reaches a device
-- until someone releases again.
CREATE TABLE preset_groups (
    id text PRIMARY KEY CHECK (id ~ '^[a-z0-9][a-z0-9-]{0,63}$'),
    name text NOT NULL CHECK (btrim(name) <> '' AND length(name) <= 100),
    icon text NOT NULL DEFAULT '' CHECK (icon = '' OR icon ~ '^[a-z0-9][a-z0-9-]{0,63}$'),
    -- {domains, ips, processes: {os: [...]}, folders: {os: [...]}}, the
    -- catalog service's own fields
    rules jsonb NOT NULL,
    updated_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE presets (
    id text PRIMARY KEY CHECK (id ~ '^[a-z0-9][a-z0-9-]{0,63}$'),
    name text NOT NULL CHECK (btrim(name) <> '' AND length(name) <= 100),
    -- the preset of every device whose country no other preset claims
    is_default boolean NOT NULL DEFAULT false,
    countries text[] NOT NULL DEFAULT '{}',
    -- the draft: group ids per side, in the order clients list them
    bypass text[] NOT NULL DEFAULT '{}',
    proxy text[] NOT NULL DEFAULT '{}',
    current_release bigint,
    next_release bigint,
    -- percent of buckets (0-99) that get next_release
    stage smallint NOT NULL DEFAULT 0 CHECK (stage BETWEEN 0 AND 100),
    updated_at timestamptz NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX presets_one_default ON presets (is_default) WHERE is_default;

CREATE TABLE preset_releases (
    id bigserial PRIMARY KEY,
    preset_id text NOT NULL REFERENCES presets(id) ON DELETE CASCADE,
    v integer NOT NULL CHECK (v >= 1),
    -- the catalog's services, frozen; the version is the epoch at serve time
    services jsonb NOT NULL,
    created_by text NOT NULL DEFAULT '',
    created_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE (preset_id, v)
);

ALTER TABLE presets
    ADD FOREIGN KEY (current_release) REFERENCES preset_releases(id),
    ADD FOREIGN KEY (next_release) REFERENCES preset_releases(id);

-- The catalog version every client sees. It grows on every change to what
-- any device is served, so one device's version never goes down, whichever
-- release it moves to. The client's bundled catalog is version 2.
CREATE TABLE preset_epoch (
    only_row boolean PRIMARY KEY DEFAULT true CHECK (only_row),
    value bigint NOT NULL
);
INSERT INTO preset_epoch (value) VALUES (2);
