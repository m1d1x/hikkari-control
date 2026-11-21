-- Точки перехода (hikkari-client plans/2026-11-19-border-forwarders.md, G5): мозг входа раз в минуту говорит,
-- как стоит каждая точка, через которую магистраль идёт к ядру. Минута точки идёт в слой point: entry_box вход,
-- exit_box ядро, target имя точки, ok, если точка не молчала ни секунды; data: from, up и members (члены
-- магистрали через точку, сколько живы), proof_ms, silent_s (секунды минуты без единого живого члена), у стенда
-- path = pipe. Смены: слой event, kind point_silent, point_back, points_silent, points_back. Адресов нет, имена машин.
ALTER TABLE trunk_runs DROP CONSTRAINT trunk_runs_layer_check;
ALTER TABLE trunk_runs ADD CONSTRAINT trunk_runs_layer_check
    CHECK (layer IN ('entry', 'event', 'vps', 'path', 'trunk', 'protocol', 'verdict', 'call', 'wire', 'load', 'clients', 'egress', 'point'));
