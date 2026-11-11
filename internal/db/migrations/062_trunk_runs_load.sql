-- Синтетические пользователи каскада (hikkari-control internal/stand/load.go,
-- разбор plans/research/2026-11-11-entry-cpu-jitter-after-stage4.md): точка в
-- РФ по запросу оператора поднимает N ядер клиента на профиле через вход, у
-- каждого звонок 50 пак/с и игра game_pps пак/с на эхо за выходом. Строка на
-- поток (target userN/voice, userN/game) идёт в слой load: sent, got, паузы
-- больше 60 и 150 мс, самая длинная, rtt, jitter, паузы больше 60 мс по
-- минутам. Это симуляция, а не люди; звонок магистрали в те же минуты лежит в
-- слое call.
ALTER TABLE trunk_runs DROP CONSTRAINT trunk_runs_layer_check;
ALTER TABLE trunk_runs ADD CONSTRAINT trunk_runs_layer_check
    CHECK (layer IN ('entry', 'event', 'vps', 'path', 'trunk', 'protocol', 'verdict', 'call', 'wire', 'load'));
