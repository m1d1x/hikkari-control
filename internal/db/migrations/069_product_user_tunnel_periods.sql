-- Главная метрика продукта (hikkari-client plans/ROADMAP.md, 3.3) у обычного
-- пользователя. У тестера минуты едут логом в product_tunnel_minutes и
-- product_tester_stall_minutes, по строке на минуту. Обычный пользователь шлёт
-- событие телеметрии "tunnel" (hikkari-client docs/TELEMETRY.md §4a), пока у
-- него включён тумблер «Помогать развитию»: период до 10 минут включённого
-- туннеля, сложенный в числа. Строка здесь это одно такое событие.
--
-- install_id случайный идентификатор установки из телеметрии, с аккаунтом и
-- адресом не связан. period начало периода. o, i, d, c, n сколько минут периода
-- трафик шёл, туннелем не пользовались, спросили и не получили ответа, ядро
-- лежало, лежала сеть самого пользователя; простой по метрике это d и c.
-- stall_minutes в скольких минутах ядро мерило подвисания; n300, n1000, n3000 и
-- ms300, ms1000, ms3000 сколько подвисаний длились не меньше порога и их полная
-- длина в мс. Адресов, назначений, профилей и сетей в таблице нет, колонок под
-- них тоже.
CREATE TABLE IF NOT EXISTS product_user_tunnel_periods (
    install_id text NOT NULL,
    period timestamptz NOT NULL,
    o int NOT NULL DEFAULT 0 CHECK (o >= 0),
    i int NOT NULL DEFAULT 0 CHECK (i >= 0),
    d int NOT NULL DEFAULT 0 CHECK (d >= 0),
    c int NOT NULL DEFAULT 0 CHECK (c >= 0),
    n int NOT NULL DEFAULT 0 CHECK (n >= 0),
    stall_minutes int NOT NULL DEFAULT 0 CHECK (stall_minutes >= 0),
    n300 int NOT NULL DEFAULT 0 CHECK (n300 >= 0),
    ms300 bigint NOT NULL DEFAULT 0 CHECK (ms300 >= 0),
    n1000 int NOT NULL DEFAULT 0 CHECK (n1000 >= 0),
    ms1000 bigint NOT NULL DEFAULT 0 CHECK (ms1000 >= 0),
    n3000 int NOT NULL DEFAULT 0 CHECK (n3000 >= 0),
    ms3000 bigint NOT NULL DEFAULT 0 CHECK (ms3000 >= 0),
    PRIMARY KEY (install_id, period)
);

CREATE INDEX IF NOT EXISTS product_user_tunnel_periods_period ON product_user_tunnel_periods (period);
