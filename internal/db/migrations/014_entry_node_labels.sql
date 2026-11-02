-- The name a subscriber reads for a logical node: "рига-001". It belongs to the
-- slot, so a replacement machine in the same city keeps it.
ALTER TABLE entry_nodes ADD COLUMN label text UNIQUE;
