-- Подвисания туннеля с точностью до мс (hikkari-client plans/ROADMAP.md, 3.3).
-- Ядро клиента (sing-box-lx hikkari 0015) само засекает подвисание: от первого
-- байта, отправленного в туннель после последнего пришедшего, до следующего
-- пришедшего по любому соединению туннеля. Клиент пишет по минуте строку
-- "[downtime] stalls from=<unix> bounds=300,1000,3000 v=…", hub переносит её сюда
-- (internal/downtime): на минуту и порог сколько подвисаний длились не меньше
-- порога (n) и их полная длина в мс (ms). Минута без подвисаний хранится с n = 0,
-- минуты, которые не меряли, не хранятся. Адресов и назначений нет.
CREATE TABLE IF NOT EXISTS product_tester_stall_minutes (
    install_id text NOT NULL,
    minute timestamptz NOT NULL,
    bound_ms int NOT NULL CHECK (bound_ms > 0),
    n int NOT NULL CHECK (n >= 0),
    ms bigint NOT NULL CHECK (ms >= 0),
    PRIMARY KEY (install_id, minute, bound_ms)
);

CREATE INDEX IF NOT EXISTS product_tester_stall_minutes_minute ON product_tester_stall_minutes (minute);
