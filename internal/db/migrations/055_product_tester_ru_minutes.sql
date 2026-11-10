-- Сеть тестера до РФ (вкладка «Тестеры» админки): раз в минуту клиент в режиме
-- тестера спрашивает ya.ru и vk.com мимо туннеля и пишет в лог строку
-- "[rureach] minutes from=<unix> ms=31,x,-". Hub переносит минуты сюда
-- (internal/downtime) рядом с минутами простоя, чтобы поломку туннеля было
-- видно отдельно от поломки домашней сети.
--
-- ms: самый быстрый ответ в миллисекундах; NULL, когда не ответил ни один сайт.
-- Минуты, которые клиент не смог измерить ("-"), не хранятся.
CREATE TABLE IF NOT EXISTS product_tester_ru_minutes (
    install_id text NOT NULL,
    minute timestamptz NOT NULL,
    ms integer CHECK (ms IS NULL OR ms > 0),
    PRIMARY KEY (install_id, minute)
);

CREATE INDEX IF NOT EXISTS product_tester_ru_minutes_minute ON product_tester_ru_minutes (minute);
