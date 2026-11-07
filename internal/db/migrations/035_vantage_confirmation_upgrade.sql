-- 031 shipped before confirmation exclusion became a list. Changing 031
-- does not upgrade an installed hub: preserve its scalar exclusions here.
DO $$
BEGIN
    IF EXISTS (
        SELECT 1 FROM information_schema.columns
        WHERE table_schema = 'public' AND table_name = 'entry_probe_tasks'
          AND column_name = 'not_by' AND data_type = 'text'
    ) THEN
        ALTER TABLE entry_probe_tasks ALTER COLUMN not_by TYPE text[]
            USING CASE WHEN not_by IS NULL OR not_by = '' THEN NULL ELSE ARRAY[not_by] END;
    END IF;
END $$;

-- The same already-applied 031 cannot add the tester evidence introduced
-- later. This remains additive on both fresh and previously deployed hubs.
ALTER TABLE entry_suspects ADD COLUMN IF NOT EXISTS testers boolean NOT NULL DEFAULT false;
