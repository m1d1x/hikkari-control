-- The owner's own name for a machine on the fleet screen ("мейн хаб"): set by a
-- double click on the name, shown instead of it, the real name stays under it.
-- Only the screen reads it: commands, logs and the hub's own work keep the name.
ALTER TABLE entry_boxes ADD COLUMN IF NOT EXISTS label text;
