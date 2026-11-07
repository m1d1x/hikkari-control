-- A group names its service's own app. The seed gave two groups a whole
-- browser: "google" chrome.exe and "yandex" browser.exe (with their macOS and
-- Linux names). A group past the VPN then took every site the browser opens
-- with it, and each blocked site stalled until the client's auto tunnel proved
-- it silent: tester-005's YouTube waited 7-22 s at every new video host. The
-- validator refuses browsers from now on (internal/presets browserProcesses,
-- the same names); this takes them out of the groups saved before. A release
-- stays frozen: the next one ships the groups as they are now.
WITH browsers(platform, name) AS (
    VALUES
        ('windows', 'chrome.exe'), ('windows', 'msedge.exe'), ('windows', 'firefox.exe'),
        ('windows', 'browser.exe'), ('windows', 'opera.exe'), ('windows', 'brave.exe'),
        ('windows', 'vivaldi.exe'), ('windows', 'chromium.exe'), ('windows', 'iexplore.exe'),
        ('darwin', 'Google Chrome'), ('darwin', 'Google Chrome Helper'), ('darwin', 'Safari'),
        ('darwin', 'Firefox'), ('darwin', 'Microsoft Edge'), ('darwin', 'Yandex'),
        ('darwin', 'Yandex Helper'), ('darwin', 'Opera'), ('darwin', 'Brave Browser'),
        ('darwin', 'Vivaldi'), ('darwin', 'Chromium'), ('darwin', 'Arc'),
        ('linux', 'google-chrome'), ('linux', 'chrome'), ('linux', 'chromium'),
        ('linux', 'chromium-browser'), ('linux', 'firefox'), ('linux', 'yandex-browser'),
        ('linux', 'opera'), ('linux', 'brave'), ('linux', 'brave-browser'),
        ('linux', 'vivaldi'), ('linux', 'microsoft-edge')
),
kept AS (
    SELECT g.id, p.platform, jsonb_agg(e.name ORDER BY e.ord) AS names
    FROM preset_groups g
    CROSS JOIN LATERAL jsonb_each(CASE WHEN jsonb_typeof(g.rules->'processes') = 'object' THEN g.rules->'processes' ELSE '{}'::jsonb END) AS p(platform, list)
    CROSS JOIN LATERAL jsonb_array_elements_text(p.list) WITH ORDINALITY AS e(name, ord)
    WHERE NOT EXISTS (
        SELECT 1 FROM browsers b
        WHERE b.platform = p.platform
          AND (b.name = e.name OR (p.platform = 'windows' AND lower(b.name) = lower(e.name)))
    )
    GROUP BY g.id, p.platform
),
touched AS (
    SELECT DISTINCT g.id
    FROM preset_groups g
    CROSS JOIN LATERAL jsonb_each(CASE WHEN jsonb_typeof(g.rules->'processes') = 'object' THEN g.rules->'processes' ELSE '{}'::jsonb END) AS p(platform, list)
    CROSS JOIN LATERAL jsonb_array_elements_text(p.list) AS e(name)
    JOIN browsers b ON b.platform = p.platform
        AND (b.name = e.name OR (p.platform = 'windows' AND lower(b.name) = lower(e.name)))
)
UPDATE preset_groups g
SET rules = jsonb_set(g.rules, '{processes}', coalesce(
        (SELECT jsonb_object_agg(k.platform, k.names) FROM kept k WHERE k.id = g.id),
        '{}'::jsonb)),
    updated_at = now()
FROM touched t
WHERE g.id = t.id;
