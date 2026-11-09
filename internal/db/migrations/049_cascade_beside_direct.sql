-- Каскад рядом с прямыми ссылками (hikkari-client plans/2026-11-05-ru-cascade.md,
-- шаг 5): cascade_only = true, как раньше, подписка отдаёт только ссылки через вход
-- (подписчик друга мерит каскад и ничего больше); cascade_only = false добавляет
-- ссылку через вход к прямым ссылкам аккаунта, чтобы diag:load тестера мерил
-- оба пути в одном раунде.
ALTER TABLE entry_cascades ADD COLUMN cascade_only boolean NOT NULL DEFAULT true;
