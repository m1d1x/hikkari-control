-- Каскад для одного аккаунта (hikkari-client plans/2026-11-05-ru-cascade.md):
-- подписка аккаунта отдаёт только Hikkari-релеи выхода exit_box, но с адресом
-- входа entry_box и его портом entry_port, который магистраль везёт к релею.
-- TLS клиента сквозной до релея: SNI и ключ остаются релея выхода. Выдаётся
-- вручную (hub subscriber-cascade); у остальных аккаунтов строки нет.
CREATE TABLE entry_cascades (
    account_id uuid PRIMARY KEY REFERENCES hub_accounts(id) ON DELETE CASCADE,
    exit_box text NOT NULL REFERENCES entry_boxes(name),
    entry_box text NOT NULL REFERENCES entry_boxes(name),
    entry_port integer NOT NULL CHECK (entry_port BETWEEN 1 AND 65535),
    created_at timestamptz NOT NULL DEFAULT now(),
    CHECK (exit_box <> entry_box)
);
