-- Главная метрика продукта (hikkari-client plans/ROADMAP.md, 3.3): каждая минута,
-- когда у установки включён туннель, с тем, чем она была. Клиент пишет минуты в
-- лог тестера строкой "[downtime] minutes from=<unix> states=oid…", hub переносит
-- их сюда (internal/downtime) и хранит дольше недели лога.
--
-- state: o трафик шёл, i туннель включён и никто им не пользовался, d спросили
-- через туннель и ничего не пришло, c ядро лежало при включённом туннеле,
-- n не отвечала и сеть самого пользователя. Простой по метрике это d и c.
CREATE TABLE IF NOT EXISTS product_tunnel_minutes (
    install_id text NOT NULL,
    minute timestamptz NOT NULL,
    state char(1) NOT NULL CHECK (state IN ('o', 'i', 'd', 'c', 'n')),
    PRIMARY KEY (install_id, minute)
);

CREATE INDEX IF NOT EXISTS product_tunnel_minutes_minute ON product_tunnel_minutes (minute);
