-- One service, one logo: the catalog's combined rows become one row per brand.
--
-- Four groups held the domains of several brands in one rule — «Copilot и
-- Perplexity», «Dailymotion и Rumble», «Tor, Proton, VPN-сайты», «Новости» — so
-- the client, which draws exactly one mark per row, had to show two logos in a
-- tile or a category pictogram. Each brand becomes its own group here, the
-- presets that hold the old ids swap them on the same side, and a fresh
-- release freezes the result so devices get it on their next poll.
--
-- The same split is written out in
-- hikkari-client/plans/research/2026-11-21-preset-split/README.md.

INSERT INTO preset_groups (id, name, icon, rules) VALUES
    ('copilot',      'Microsoft Copilot',        'copilot',
     '{"domains":["copilot.microsoft.com"],"ips":[],"processes":{}}'),
    ('perplexity',   'Perplexity',               'perplexity',
     '{"domains":["perplexity.ai","pplx.ai"],"ips":[],"processes":{}}'),
    ('dailymotion',  'Dailymotion',              'dailymotion',
     '{"domains":["dailymotion.com","dmcdn.net","dm-event.net"],"ips":[],"processes":{}}'),
    ('rumble',       'Rumble',                   'rumble',
     '{"domains":["rumble.com"],"ips":[],"processes":{}}'),
    ('tor',          'Tor',                      'tor',
     '{"domains":["torproject.org"],"ips":[],"processes":{}}'),
    ('proton',       'Proton Mail и VPN',        'proton',
     '{"domains":["proton.me","protonmail.com","protonvpn.com"],"ips":[],"processes":{}}'),
    ('mullvad',      'Mullvad VPN',              'mullvad',
     '{"domains":["mullvad.net"],"ips":[],"processes":{}}'),
    ('nordvpn',      'NordVPN',                  'nordvpn',
     '{"domains":["nordvpn.com"],"ips":[],"processes":{}}'),
    ('psiphon',      'Psiphon',                  'psiphon',
     '{"domains":["psiphon.ca"],"ips":[],"processes":{}}'),
    ('bbc',          'BBC',                      'bbc',
     '{"domains":["bbc.com","bbc.co.uk","bbci.co.uk"],"ips":[],"processes":{}}'),
    ('dw',           'Deutsche Welle',           'dw',
     '{"domains":["dw.com"],"ips":[],"processes":{}}'),
    ('euronews',     'Euronews',                 'euronews',
     '{"domains":["euronews.com"],"ips":[],"processes":{}}'),
    ('rferl',        'Радио Свобода / RFE/RL',   'rferl',
     '{"domains":["svoboda.org","rferl.org","currenttime.tv"],"ips":[],"processes":{}}'),
    ('tvrain',       'Дождь',                    'tvrain',
     '{"domains":["tvrain.tv"],"ips":[],"processes":{}}'),
    ('novayagazeta', 'Новая газета',             'novayagazeta',
     '{"domains":["novayagazeta.eu"],"ips":[],"processes":{}}'),
    ('meduza',       'Медуза',                   'meduza',
     '{"domains":["meduza.io"],"ips":[],"processes":{}}')
ON CONFLICT (id) DO NOTHING;

DO $$
DECLARE
    -- old combined id -> the brands it is split into, in catalog order
    maps jsonb := '{
        "ai-other":    ["copilot", "perplexity"],
        "video-other": ["dailymotion", "rumble"],
        "privacy":     ["tor", "proton", "mullvad", "nordvpn", "psiphon"],
        "news":        ["bbc", "dw", "euronews", "rferl", "tvrain", "novayagazeta", "meduza"]
    }'::jsonb;
    p record;
    side text;
    arr text[];
    out_arr text[];
    gid text;
    repl jsonb;
    i int;
    changed bool;
    svc jsonb;
    part jsonb;
    vnum int;
    rid bigint;
    bumped bool := false;
BEGIN
    FOR p IN SELECT id, bypass, proxy, disabled, default_mode FROM presets LOOP
        changed := false;
        FOREACH side IN ARRAY ARRAY['bypass', 'proxy', 'disabled'] LOOP
            arr := CASE side
                       WHEN 'bypass' THEN p.bypass
                       WHEN 'proxy' THEN p.proxy
                       ELSE p.disabled
                   END;
            out_arr := '{}';
            FOREACH gid IN ARRAY coalesce(arr, '{}') LOOP
                IF maps ? gid THEN
                    repl := maps -> gid;
                    FOR i IN 0 .. jsonb_array_length(repl) - 1 LOOP
                        out_arr := out_arr || (repl ->> i);
                    END LOOP;
                    changed := true;
                ELSE
                    out_arr := out_arr || gid;
                END IF;
            END LOOP;
            -- first occurrence wins: a preset may already list a brand
            SELECT coalesce(array_agg(x ORDER BY ord), '{}') INTO out_arr
            FROM (SELECT DISTINCT ON (x) x, ord
                  FROM unnest(out_arr) WITH ORDINALITY AS t(x, ord)
                  ORDER BY x, ord) d;
            IF side = 'bypass' THEN
                p.bypass := out_arr;
            ELSIF side = 'proxy' THEN
                p.proxy := out_arr;
            ELSE
                p.disabled := out_arr;
            END IF;
        END LOOP;

        CONTINUE WHEN NOT changed;

        UPDATE presets
        SET bypass = p.bypass, proxy = p.proxy, disabled = p.disabled, updated_at = now()
        WHERE id = p.id;

        -- The release, frozen the way internal/presets.freeze does it: bypass,
        -- then proxy, then disabled, each group as a catalog service.
        svc := '[]'::jsonb;
        FOREACH side IN ARRAY ARRAY['bypass', 'proxy', 'disabled'] LOOP
            arr := CASE side
                       WHEN 'bypass' THEN p.bypass
                       WHEN 'proxy' THEN p.proxy
                       ELSE p.disabled
                   END;
            SELECT coalesce(jsonb_agg(jsonb_build_object(
                       'id', g.id,
                       'name', g.name,
                       'icon', g.icon,
                       'domains', coalesce(g.rules -> 'domains', '[]'::jsonb),
                       'ips', coalesce(g.rules -> 'ips', '[]'::jsonb),
                       'processes', coalesce(g.rules -> 'processes', '{}'::jsonb),
                       'defaultModes', to_jsonb(CASE side
                           WHEN 'bypass' THEN ARRAY['bypass']::text[]
                           WHEN 'proxy' THEN ARRAY['proxy']::text[]
                           ELSE '{}'::text[]
                       END)) ORDER BY u.ord), '[]'::jsonb)
            INTO part
            FROM unnest(coalesce(arr, '{}')) WITH ORDINALITY AS u(id, ord)
            JOIN preset_groups g ON g.id = u.id;
            svc := svc || part;
        END LOOP;

        SELECT coalesce(max(v), 0) + 1 INTO vnum FROM preset_releases WHERE preset_id = p.id;
        INSERT INTO preset_releases (preset_id, v, services, created_by, default_mode)
        VALUES (p.id, vnum, svc, 'migration 020: one service, one logo', coalesce(p.default_mode, ''))
        RETURNING id INTO rid;
        -- Stage 100: a shape fix of the list, not an experiment — every bucket
        -- gets it, the same as the panel's «ко всем».
        UPDATE presets
        SET current_release = rid, next_release = NULL, stage = 0, updated_at = now()
        WHERE id = p.id;
        bumped := true;
    END LOOP;

    IF bumped THEN
        UPDATE preset_epoch SET value = value + 1;
    END IF;
END $$;

-- The combined rows are nobody's draft any more: they would only show up in
-- the panel as leftovers.
DELETE FROM preset_groups g
WHERE g.id IN ('ai-other', 'video-other', 'privacy', 'news')
  AND NOT EXISTS (
      SELECT 1 FROM presets p
      WHERE g.id = ANY (p.bypass) OR g.id = ANY (p.proxy) OR g.id = ANY (p.disabled)
  );
