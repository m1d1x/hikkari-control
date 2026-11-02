-- Запрос диагностики у тестера: команда едет в ответе на POST /tester-logs,
-- результат приходит чанком src=«doctor nonce=…» с подтверждением nonce.
-- Команда не несёт данных и не исполняет код: kind из белого списка хаба и клиента.
CREATE TABLE IF NOT EXISTS support_tester_commands (
    nonce text PRIMARY KEY,
    install_id text NOT NULL,
    kind text NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    delivered_at timestamptz,
    acked_at timestamptz
);

CREATE INDEX IF NOT EXISTS support_tester_commands_install_idx
    ON support_tester_commands (install_id, created_at DESC);
