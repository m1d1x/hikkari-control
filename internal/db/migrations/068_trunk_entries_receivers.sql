-- Группа приёмников входа (hikkari-client plans/2026-11-16-second-receiver.md, P5):
-- кроме exit_box вход держит магистраль до этих машин и сам уходит с мёртвой
-- (EDGE_TRUNK_RECEIVERS), владелец переводит руками (entry-box trunk-entry-receiver).
-- Пусто: у входа один приёмник, exit_box.
ALTER TABLE trunk_entries ADD COLUMN receivers text[] NOT NULL DEFAULT '{}';
