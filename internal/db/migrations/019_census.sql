-- Перепись входов подписок, оплаченных владельцем: что измерено и из какой сети.
-- URL подписки и профили только в зашифрованном виде.
CREATE TABLE IF NOT EXISTS census_subscriptions (
    id bigserial PRIMARY KEY,
    name text NOT NULL UNIQUE,
    url_ct bytea, -- NULL у сервиса без импорта: только адреса
    created_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS census_entries (
    id bigserial PRIMARY KEY,
    subscription_id bigint NOT NULL REFERENCES census_subscriptions (id) ON DELETE CASCADE,
    label text NOT NULL,
    host text NOT NULL,
    port integer NOT NULL,
    protocol text NOT NULL,
    transport text NOT NULL,
    security text NOT NULL,
    sni text NOT NULL DEFAULT '',
    ip text NOT NULL DEFAULT '',
    config_ct bytea, -- outbound sing-box с секретами, для census proto
    first_seen_at timestamptz NOT NULL DEFAULT now(),
    gone_at timestamptz,
    UNIQUE (subscription_id, host, port, protocol, transport)
);

-- Ответы Team Cymru по адресу, и входов, и наших релеев.
CREATE TABLE IF NOT EXISTS census_networks (
    ip text PRIMARY KEY,
    asn integer NOT NULL,
    as_name text NOT NULL DEFAULT '',
    country text NOT NULL DEFAULT '',
    checked_at timestamptz NOT NULL DEFAULT now()
);

-- target: метка цели, sub12-443 у входа, ams7-443 у релея. kind: tcp или proto.
CREATE TABLE IF NOT EXISTS census_measurements (
    id bigserial PRIMARY KEY,
    target text NOT NULL,
    vantage text NOT NULL,
    kind text NOT NULL CHECK (kind IN ('tcp', 'proto')),
    opened integer NOT NULL,
    total integer NOT NULL,
    measured_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS census_measurements_latest
    ON census_measurements (target, vantage, kind, measured_at DESC);

-- Выход сервиса: запрос через его туннель пришёл на hub по одноразовому адресу.
-- owner: метка входа (sub12-443) или service:имя у сервиса без импорта.
CREATE TABLE IF NOT EXISTS census_exit_tokens (
    token text PRIMARY KEY,
    owner text NOT NULL UNIQUE,
    created_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS census_exits (
    owner text NOT NULL,
    ip text NOT NULL,
    hits integer NOT NULL DEFAULT 1,
    last_seen_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (owner, ip)
);
