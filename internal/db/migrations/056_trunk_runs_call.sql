-- Звонок магистрали (hikkari-protocol trunk/call.go): вход раз в 20 мс шлёт
-- датаграмму на эхо выхода, по звонку с одной копией и с двумя, и раз в минуту
-- пишет строку "call copies=N from=<unix> sent= got= rtt_* gaps60= gaps150=
-- gap_max= silent_ms= worst=". Hub кладёт её слоем call: vantage entry, target
-- copies1 или copies2, ts = начало минуты.
ALTER TABLE trunk_runs DROP CONSTRAINT trunk_runs_layer_check;
ALTER TABLE trunk_runs ADD CONSTRAINT trunk_runs_layer_check
    CHECK (layer IN ('entry', 'event', 'vps', 'path', 'trunk', 'protocol', 'verdict', 'call'));
