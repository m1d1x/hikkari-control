-- A ban is judged from Russia, by every point there is (eun16/eun17, 02.11
-- and 07.11).
--
-- in_russia: the point measures from inside Russia. Only such a point claims
-- an admission probe or a reprobe and only its rows count in the routine
-- verdicts: a "clean" from abroad says nothing about a block in Russia. The
-- stand's control outside Russia (lv-main, hikkari-main in Latvia) keeps its
-- bench runs.
ALTER TABLE entry_vantages ADD COLUMN IF NOT EXISTS in_russia boolean NOT NULL DEFAULT true;
UPDATE entry_vantages SET in_russia = false WHERE slot = 'lv-main';

-- not_by: a confirmation. One point's refusal (banned, spotted, cut) is asked
-- again of the other live points in Russia, up to ten in all, one at a time;
-- the points that already answered may not answer it again.
ALTER TABLE entry_probe_tasks ADD COLUMN IF NOT EXISTS not_by text[];
