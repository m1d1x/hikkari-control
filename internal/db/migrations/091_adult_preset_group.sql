-- Adult sites through the VPN: one catalog group, «Adult», on the proxy side of
-- the ru preset.
--
-- Users asked for it, the owner agreed (27.11): people watch this, and a VPN
-- that covers everyday needs covers this too. To keep face the row wears no
-- brand: the client draws the icon id `adult` as an «18+» plate and fetches
-- nothing from these sites. Domains match by suffix, so a site's own
-- subdomains ride with it; the CDNs that serve the video are listed beside it.
--
-- A migration, not a panel edit, the same as 075: the catalog is data, and the
-- fix belongs in schema history every hub gets on deploy. The release is frozen
-- the way internal/presets.freeze does it (folders kept, absent ones left out)
-- and goes to stage 100.

INSERT INTO preset_groups (id, name, icon, rules) VALUES
    ('adult', 'Adult', 'adult',
     '{"domains":[
         "pornhub.com", "pornhub.org", "phncdn.com",
         "xvideos.com", "xvideos-cdn.com",
         "xnxx.com", "xnxx-cdn.com",
         "xhamster.com", "xhcdn.com",
         "redtube.com", "rdtcdn.com",
         "youporn.com", "ypncdn.com",
         "spankbang.com",
         "eporner.com",
         "chaturbate.com", "highwebmedia.com",
         "onlyfans.com"
       ],"ips":[],"processes":{}}')
ON CONFLICT (id) DO NOTHING;

DO $$
DECLARE
    p record;
    side text;
    arr text[];
    svc jsonb := '[]'::jsonb;
    part jsonb;
    vnum int;
    rid bigint;
BEGIN
    SELECT id, bypass, proxy, disabled, default_mode INTO p
    FROM presets WHERE id = 'ru' FOR UPDATE;
    IF NOT FOUND
       OR 'adult' = ANY (coalesce(p.bypass, '{}'))
       OR 'adult' = ANY (coalesce(p.proxy, '{}'))
       OR 'adult' = ANY (coalesce(p.disabled, '{}')) THEN
        RETURN;
    END IF;

    p.proxy := coalesce(p.proxy, '{}') || 'adult'::text;
    UPDATE presets SET proxy = p.proxy, updated_at = now() WHERE id = p.id;

    FOREACH side IN ARRAY ARRAY['bypass', 'proxy', 'disabled'] LOOP
        arr := CASE side
                   WHEN 'bypass' THEN p.bypass
                   WHEN 'proxy' THEN p.proxy
                   ELSE p.disabled
               END;
        SELECT coalesce(jsonb_agg(jsonb_strip_nulls(jsonb_build_object(
                   'id', g.id,
                   'name', g.name,
                   'icon', g.icon,
                   'domains', coalesce(g.rules -> 'domains', '[]'::jsonb),
                   'ips', coalesce(g.rules -> 'ips', '[]'::jsonb),
                   'processes', coalesce(g.rules -> 'processes', '{}'::jsonb),
                   'folders', g.rules -> 'folders',
                   'defaultModes', to_jsonb(CASE side
                       WHEN 'bypass' THEN ARRAY['bypass']::text[]
                       WHEN 'proxy' THEN ARRAY['proxy']::text[]
                       ELSE '{}'::text[]
                   END))) ORDER BY u.ord), '[]'::jsonb)
        INTO part
        FROM unnest(coalesce(arr, '{}')) WITH ORDINALITY AS u(id, ord)
        JOIN preset_groups g ON g.id = u.id;
        svc := svc || part;
    END LOOP;

    SELECT coalesce(max(v), 0) + 1 INTO vnum FROM preset_releases WHERE preset_id = p.id;
    INSERT INTO preset_releases (preset_id, v, services, created_by, default_mode)
    VALUES (p.id, vnum, svc, 'migration 091: adult sites', coalesce(p.default_mode, ''))
    RETURNING id INTO rid;
    UPDATE presets
    SET current_release = rid, next_release = NULL, stage = 0, updated_at = now()
    WHERE id = p.id;
    UPDATE preset_epoch SET value = value + 1;
END $$;
