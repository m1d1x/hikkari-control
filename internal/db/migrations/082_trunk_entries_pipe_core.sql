-- Ядро стенда и точки перехода (hikkari-client plans/2026-11-19-border-forwarders.md,
-- G2): стенд над hk-pipe на входе может идти не к выходу магистрали людей
-- (exit_box), а к своему ядру (pipe_core, entry-box trunk-pipe ... core=<box>),
-- через точки перехода (pipe_points, entry-box trunk-pipe-points): машины,
-- которые только пересылают порты магистрали на ядро (entry-box border-forward).
-- NULL и пусто: стенд к exit_box напрямую, как раньше.
ALTER TABLE trunk_entries ADD COLUMN pipe_core text REFERENCES entry_boxes(name);
ALTER TABLE trunk_entries ADD COLUMN pipe_points text[] NOT NULL DEFAULT '{}';
