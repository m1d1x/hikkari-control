-- Человекочитаемое имя установщика тестера: «tester-001» вместо сырого
-- install_id. Hub выдаёт алиас при первом сигнале тестерского потока, оператор
-- переименовывает в админке или через MCP; install_id остаётся вторым полем.
-- Здесь же последний сигнал установщика: пустой heartbeat не хранится чанком,
-- а «на связи ли тестер» нужно знать и когда лог молчит.
-- Установщики, что уже слали чанки, получают алиасы в этой же миграции.
CREATE SEQUENCE IF NOT EXISTS support_install_alias_seq;

CREATE TABLE IF NOT EXISTS support_install_aliases (
    install_id text PRIMARY KEY,
    alias text NOT NULL UNIQUE,
    note text NOT NULL DEFAULT '',
    platform text NOT NULL DEFAULT 'unknown',
    version text NOT NULL DEFAULT 'unknown',
    last_seen_at timestamptz NOT NULL DEFAULT now(),
    created_at timestamptz NOT NULL DEFAULT now()
);

INSERT INTO support_install_aliases (install_id, alias, platform, version, last_seen_at, created_at)
SELECT install_id,
       'tester-' || lpad(row_number() OVER (ORDER BY first_seen)::text, 3, '0'),
       platform, version, last_seen, first_seen
FROM (
    SELECT install_id,
           min(received_at) AS first_seen,
           max(received_at) AS last_seen,
           (array_agg(platform ORDER BY received_at DESC))[1] AS platform,
           (array_agg(version ORDER BY received_at DESC))[1] AS version
    FROM support_tester_log_chunks
    GROUP BY install_id
) d
ON CONFLICT (install_id) DO NOTHING;

-- Продолжить нумерацию после выданных, чтобы следующий nextval не столкнулся
-- с занятым алиасом. Пустая таблица — первый алиас будет tester-001.
SELECT setval('support_install_alias_seq',
    COALESCE((SELECT max(substring(alias from '(\d+)$')::int) FROM support_install_aliases), 1),
    EXISTS (SELECT 1 FROM support_install_aliases));
