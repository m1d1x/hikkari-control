-- Точки перехода входа людей (hikkari-client plans/2026-11-19-border-forwarders.md,
-- G3): магистраль людей идёт к ядру (exit_box, например aeznl1) не напрямую, а
-- через машины, которые только пересылают её порты на ядро (entry-box
-- border-forward). Пусто: вход набирает адрес exit_box сам, как раньше. Точки
-- стенда отдельно (pipe_points, миграция 082).
ALTER TABLE trunk_entries ADD COLUMN points text[] NOT NULL DEFAULT '{}';
