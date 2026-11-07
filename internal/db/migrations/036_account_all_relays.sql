-- Тестерская подписка: аккаунт держит все живые личные релеи флота, а не
-- батч из пары (entry.Service.serve). Выдаётся вручную из админки; для
-- обычных подписчиков остаётся false и ничего не меняет.
ALTER TABLE hub_accounts ADD COLUMN all_relays boolean NOT NULL DEFAULT false;
