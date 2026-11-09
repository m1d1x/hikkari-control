-- Наблюдение за каскадом (hikkari-client plans/2026-11-05-ru-cascade.md;
-- internal/cascadewatch): что стоит, и что по нему намерено.
--
-- trunk_entries: вход магистрали, который поставил hub (entry-box trunk-entry),
-- и его выход. Пишет StandTrunkEntry, стирает StopTrunkEntry. Наблюдение
-- берёт пары отсюда.
CREATE TABLE trunk_entries (
    entry_box text PRIMARY KEY REFERENCES entry_boxes(name),
    exit_box text NOT NULL REFERENCES entry_boxes(name),
    -- порт входа → порт выхода на loopback, {"8443": 443, "8053": 2053}
    forward jsonb NOT NULL,
    members integer NOT NULL CHECK (members BETWEEN 1 AND 128),
    stood_at timestamptz NOT NULL DEFAULT now(),
    CHECK (exit_box <> entry_box)
);

-- trunk_runs: датасет каскада. Слои: entry (строка stats входа раз в минуту),
-- event (смерть члена пула, смена эпохи выхода, старт входа), vps, path, trunk,
-- protocol (раунд раз в 10 минут, общий round_ts), verdict (какой слой раунда
-- первым стал красным). vantage: где мерили (entry, exit или слот точки),
-- target: что мерили (direct, entry или адрес-метка). Ключей и адресов
-- клиентов в data нет.
CREATE TABLE trunk_runs (
    id bigserial PRIMARY KEY,
    ts timestamptz NOT NULL,
    round_ts timestamptz,
    entry_box text NOT NULL,
    exit_box text NOT NULL,
    layer text NOT NULL CHECK (layer IN ('entry', 'event', 'vps', 'path', 'trunk', 'protocol', 'verdict')),
    vantage text NOT NULL DEFAULT '',
    target text NOT NULL DEFAULT '',
    ok boolean,
    data jsonb NOT NULL DEFAULT '{}'
);
CREATE INDEX trunk_runs_ts ON trunk_runs (ts);
CREATE INDEX trunk_runs_layer ON trunk_runs (entry_box, layer, ts);
CREATE INDEX trunk_runs_round ON trunk_runs (round_ts) WHERE round_ts IS NOT NULL;

-- Протокольный слой каскада мерят пробники точек задачей стенда своего вида
-- (cascade-v1): ссылки задачи не флот, а две ссылки выхода, прямая и через
-- вход cascade_entry.
ALTER TABLE stand_tasks ADD COLUMN cascade_entry text;
ALTER TABLE stand_tasks ADD COLUMN cascade_round timestamptz;
