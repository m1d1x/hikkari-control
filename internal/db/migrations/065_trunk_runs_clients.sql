-- Участок клиент → вход каскада (hikkari-control trunkentry/clients.go, слово
-- владельца 14.11: потери и пинг от клиента до РФ во «флоте»). Вход раз в
-- минуту пишет сумму по всем TCP-сокетам клиентов: сколько сегментов отправил,
-- сколько переотправил, rtt p50 и p90 по сокетам. Без адресов, потоков и
-- разбивки по людям (слепота входа 3.2). Строка идёт в слой clients, target tcp.
ALTER TABLE trunk_runs DROP CONSTRAINT trunk_runs_layer_check;
ALTER TABLE trunk_runs ADD CONSTRAINT trunk_runs_layer_check
    CHECK (layer IN ('entry', 'event', 'vps', 'path', 'trunk', 'protocol', 'verdict', 'call', 'wire', 'load', 'clients'));
