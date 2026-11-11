-- Стенд второго провода магистрали (hikkari-control edge/wirestand.go, пункт
-- 4.1 роадмапа): на входе по процессу на провод (quic, salamander, awg, udp).
-- Звонок провода идёт в слой call с target wire-<провод>, рядом с copies1 и
-- copies2; раз в 10 минут 1 МиБ по проводу (mode bulk: bytes, want, ms, end) и
-- раз в минуту CPU процесса и steal бокса (mode cpu: cpu_ms, steal_pct,
-- busy_pct) идут в слой wire, target = провод.
ALTER TABLE trunk_runs DROP CONSTRAINT trunk_runs_layer_check;
ALTER TABLE trunk_runs ADD CONSTRAINT trunk_runs_layer_check
    CHECK (layer IN ('entry', 'event', 'vps', 'path', 'trunk', 'protocol', 'verdict', 'call', 'wire'));
