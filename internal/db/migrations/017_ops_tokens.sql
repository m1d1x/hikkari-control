-- Машинный доступ агента к операторским данным: свой класс учётных данных, а не
-- дыра в человеческом. Токен открывает только /api/v1/ops/* (чтение логов,
-- запрос диагностики, переименование) и живёт за тем же SSH-туннелем, что и панель.
-- Хранится только sha256: токен показан один раз при выпуске на сервере.
CREATE TABLE IF NOT EXISTS admin_ops_tokens (
    id bigserial PRIMARY KEY,
    label text NOT NULL,
    token_hash text NOT NULL UNIQUE,
    created_at timestamptz NOT NULL DEFAULT now(),
    last_used_at timestamptz,
    revoked_at timestamptz
);
