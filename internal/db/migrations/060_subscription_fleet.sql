-- Подписка из админки (просьба владельца 11.11): название, которое клиент
-- показывает у подписки (заголовок Profile-Title), и флот, собранный руками.
--
-- title: что видит человек в клиенте вместо «hikkari»; пусто, значит «hikkari».
ALTER TABLE hub_accounts ADD COLUMN title text CHECK (char_length(title) BETWEEN 1 AND 64);

-- Флот подписки: логические узлы (entry_nodes), которые владелец выбрал сам.
-- Есть хоть одна строка, значит аккаунт держит только эти узлы, а не батч
-- пула (entry.Service.serve): гранты только на релеях этих узлов, гранты на
-- остальных отзываются с причиной fleet. Узел переживает замену релея, так
-- что выбор не устаревает, когда релей под узлом меняется. У тестера
-- (all_relays) гранты на всём флоте как раньше, а ссылки только по выбору.
CREATE TABLE hub_account_nodes (
    account_id uuid NOT NULL REFERENCES hub_accounts(id) ON DELETE CASCADE,
    slug text NOT NULL REFERENCES entry_nodes(slug) ON DELETE CASCADE,
    added_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (account_id, slug)
);

ALTER TABLE entry_grants DROP CONSTRAINT entry_grants_revoke_reason_check;
ALTER TABLE entry_grants ADD CONSTRAINT entry_grants_revoke_reason_check
    CHECK (revoke_reason IN ('relay_dead', 'drained', 'suspect', 'fleet'));
ALTER TABLE entry_grants DROP CONSTRAINT entry_grants_trigger_check;
ALTER TABLE entry_grants ADD CONSTRAINT entry_grants_trigger_check
    CHECK (trigger IN ('initial', 'death_replacement', 'drain_replacement', 'fleet'));
