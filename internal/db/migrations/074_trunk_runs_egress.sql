-- Сторож выходов на приёмнике (hikkari-control internal/entry/egress.go, watch; H4 плана скрытого приёмника).
-- Минута сторожа по выходу идёт в слой egress: entry_box приёмник, exit_box и target выход, vantage его роль
-- (active везёт, standby запасной), data: from, tun_ok/tun_n (ответы через туннель на адрес выхода), rtt_ms,
-- у везущего net_ok/net_n (интернет через него). Уход и отказ уйти: слой event, kind egress_moved,
-- egress_stuck, egress_by_hub. Адресов в строках нет, только имена машин.
ALTER TABLE trunk_runs DROP CONSTRAINT trunk_runs_layer_check;
ALTER TABLE trunk_runs ADD CONSTRAINT trunk_runs_layer_check
    CHECK (layer IN ('entry', 'event', 'vps', 'path', 'trunk', 'protocol', 'verdict', 'call', 'wire', 'load', 'clients', 'egress'));
