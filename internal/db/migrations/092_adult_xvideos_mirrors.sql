-- The adult group gains the Russian XVIDEOS mirror and the names its pages
-- load from.
--
-- A user reported xv-ru.com not opening (27.11). It is XVIDEOS's own Russian
-- mirror behind Cloudflare (title «XVIDEOS.COM»); its page loads video and
-- thumbnails from xvideos-cdn.com, already in the group, and links to
-- xvideos.net and xvideos.red. Domains match by suffix, so www.xv-ru.com rides
-- with xv-ru.com.
--
-- Same shape as 091: the group's rules change, every preset that holds the
-- group gets a fresh release frozen the way internal/presets.freeze does it,
-- at stage 100.

UPDATE preset_groups g
SET rules = jsonb_set(
        g.rules,
        '{domains}',
        coalesce(g.rules -> 'domains', '[]'::jsonb) || (
            SELECT coalesce(jsonb_agg(d), '[]'::jsonb)
            FROM unnest(ARRAY['xv-ru.com', 'xvideos.net', 'xvideos.red']) AS d
            WHERE NOT coalesce(g.rules -> 'domains', '[]'::jsonb) ? d
        )
    ),
    updated_at = now()
WHERE g.id = 'adult';

DO $$
DECLARE
    p record;
    side text;
    arr text[];
    svc jsonb;
    part jsonb;
    vnum int;
    rid bigint;
    bumped bool := false;
BEGIN
    FOR p IN
        SELECT id, bypass, proxy, disabled, default_mode FROM presets
        WHERE 'adult' = ANY (coalesce(bypass, '{}'))
           OR 'adult' = ANY (coalesce(proxy, '{}'))
           OR 'adult' = ANY (coalesce(disabled, '{}'))
        FOR UPDATE
    LOOP
        svc := '[]'::jsonb;
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
        VALUES (p.id, vnum, svc, 'migration 092: xvideos mirrors', coalesce(p.default_mode, ''))
        RETURNING id INTO rid;
        UPDATE presets
        SET current_release = rid, next_release = NULL, stage = 0, updated_at = now()
        WHERE id = p.id;
        bumped := true;
    END LOOP;

    IF bumped THEN
        UPDATE preset_epoch SET value = value + 1;
    END IF;
END $$;
