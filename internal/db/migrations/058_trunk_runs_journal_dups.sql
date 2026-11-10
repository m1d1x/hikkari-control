-- Ряды журнала входа, которые hub до миграции 057 записал дважды при своих
-- перезапусках: тот же момент журнала, тот же слой, те же данные. На hub их 8
-- (09.11 17:52 и 18:56, 10.11 19:20 UTC). Остаётся первый ряд, второй
-- удаляется. Удаление по слову владельца 11.11.
DELETE FROM trunk_runs a USING trunk_runs b
WHERE a.vantage = 'entry' AND a.layer IN ('entry', 'event', 'call')
  AND b.entry_box = a.entry_box AND b.layer = a.layer AND b.vantage = a.vantage
  AND b.target = a.target AND b.ts = a.ts AND b.data = a.data AND b.id < a.id;
