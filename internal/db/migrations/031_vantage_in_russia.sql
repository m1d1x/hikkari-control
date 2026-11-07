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

-- testers: the subscribers' reports (downdetector) saw the machine blocked when
-- it was evicted. A ban needs two instances, a prober among them: one point in
-- Russia and the testers, or two points (vantage_confirm.go). Once evicted the
-- machine carries nobody and the testers stop reporting on it, so what they
-- said is kept with the suspicion.
ALTER TABLE entry_suspects ADD COLUMN IF NOT EXISTS testers boolean NOT NULL DEFAULT false;
