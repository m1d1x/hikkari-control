-- 057 — domains the hub must never use for a relay (roadmap 42 П9.1).
--
-- A relay's domain is a consumable: it goes into the pool, onto a box, and is
-- burned on its first block. The registrar account also holds domains that
-- must never take that path — the brand's, legacy ones, personal ones. Until
-- now only the configured brand domain was refused, by code. This table is the
-- owner's list on top of it, edited in the console.
--
-- A row covers the name and every subdomain of it. The hub refuses an
-- excluded name everywhere a domain enters relay use: seeding into the pool,
-- buying, a box given a domain by hand, and drawing from the pool (a name
-- excluded after it was pooled is simply never drawn). A name already on a
-- box stays there until that box is re-provisioned; the console says so.

CREATE TABLE entry_domain_exclusions (
    domain     text PRIMARY KEY CHECK (domain = lower(domain) AND domain <> ''),
    note       text NOT NULL DEFAULT '',
    created_by uuid REFERENCES admins(id) ON DELETE SET NULL,
    created_at timestamptz NOT NULL DEFAULT now()
);
