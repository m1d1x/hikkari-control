-- A ban is judged from Russia, by two points (eun16/eun17, 02.11 and 07.11).
--
-- in_russia: the point measures from inside Russia. Only such a point claims
-- an admission probe or a reprobe and only its rows count in the routine
-- verdicts: a "clean" from abroad says nothing about a block in Russia. The
-- stand's control outside Russia (lv-main, hikkari-main in Latvia) keeps its
-- bench runs.
ALTER TABLE entry_vantages ADD COLUMN IF NOT EXISTS in_russia boolean NOT NULL DEFAULT true;
UPDATE entry_vantages SET in_russia = false WHERE slot = 'lv-main';

-- not_by: a confirmation. One point's refusal (banned, spotted, cut) is asked
-- again of another, and the point that refused may not answer it.
ALTER TABLE entry_probe_tasks ADD COLUMN IF NOT EXISTS not_by text;

-- The routine verdicts take a block only when two points in Russia saw it,
-- where two are there to see: with one, confirmation would stop every
-- eviction instead of making it stricter (SaveDetectionPolicy's own rule).
UPDATE entry_detection_policy SET require_two_vantages = true
WHERE (SELECT count(*) FROM entry_vantages WHERE status = 'active' AND in_russia) >= 2;
