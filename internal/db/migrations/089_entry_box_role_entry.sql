-- Роль entry: вход каскада в РФ вне пула релеев (hikkari-client plans/2026-11-24-second-entry-client-failover.md,
-- V2). Вход только принимает TCP клиентов и несёт UDP по магистрали, ключей людей на нём нет (слепота 3.2), поэтому
-- релея hub ему не выпускает никогда: кладёт свой ключ, закрывает машину, дальше на ней стоит только trunk-entry.
-- Без роли такая машина считалась бы машиной флота, и hub выпустил бы на ней релей в подписки.
ALTER TABLE entry_boxes DROP CONSTRAINT entry_boxes_role_check;
ALTER TABLE entry_boxes ADD CONSTRAINT entry_boxes_role_check CHECK (role IN ('egress', 'receiver', 'entry'));
