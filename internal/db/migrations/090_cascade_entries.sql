-- Дальнейшие входы каскада аккаунта (hikkari-client plans/2026-11-24-second-entry-client-failover.md,
-- протокол v1.07): вход из entry_cascades это вход 0, строки здесь идут в ссылке параметром
-- entries= по position. Каждый такой вход ведёт магистралью к тому же ядру, что и вход 0, клиент
-- держит путь на каждый и сам уходит с замолчавшего. Ссылку с entries= hub отдаёт только клиенту,
-- который сказал фичу hikkari-entries. Выдаётся вручную (hub subscriber-cascade-entry).
CREATE TABLE entry_cascade_entries (
    account_id uuid NOT NULL REFERENCES entry_cascades(account_id) ON DELETE CASCADE,
    position smallint NOT NULL CHECK (position >= 1),
    entry_box text NOT NULL REFERENCES entry_boxes(name),
    entry_port integer NOT NULL CHECK (entry_port BETWEEN 1 AND 65535),
    created_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (account_id, position),
    UNIQUE (account_id, entry_box, entry_port)
);
