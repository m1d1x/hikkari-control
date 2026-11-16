-- Сертификат группы приёмников (hikkari-client plans/2026-11-16-second-receiver.md,
-- P1): приёмники одного домена на разных машинах отвечают клиенту одним именем.
-- hub выпускает сертификат проверкой DNS-01 (TXT у регистратора) и кладёт его в
-- env каждого релея Hikkari с этим доменом (EDGE_HIKKARI_CERT, EDGE_HIKKARI_KEY).
-- Ключ сертификата и ключ аккаунта ACME зашифрованы ключом содержимого
-- (internal/cryptobox), как identity_ct релея.
CREATE TABLE receiver_certs (
	domain     text PRIMARY KEY,
	directory  text NOT NULL,
	chain_pem  text NOT NULL,
	key_ct     bytea NOT NULL,
	not_after  timestamptz NOT NULL,
	issued_at  timestamptz NOT NULL DEFAULT now()
);

-- Ключ аккаунта ACME на каждый каталог (боевой и staging Let's Encrypt):
-- ключ, который CA уже знает, сам находит свой аккаунт.
CREATE TABLE acme_accounts (
	directory  text PRIMARY KEY,
	key_ct     bytea NOT NULL,
	created_at timestamptz NOT NULL DEFAULT now()
);
