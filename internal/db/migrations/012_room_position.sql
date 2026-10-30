-- 012 — a server room has a place in its category.
--
-- Rooms sorted by creation within their category, and nothing could change
-- that: the only thing a person could reorder was the categories themselves.
-- Moving a room — into another category, or up and down inside one — needs a
-- stored position, the same shape room_categories already has.
--
-- Positions are dense per (server, category) and renumbered by the move itself
-- (MoveServerRoom), so the column never carries gaps a client would have to
-- interpret. A room with no server (a standalone room) keeps 0: it has no list
-- to sit in.
--
-- Existing rooms are numbered in the order they already appear — by creation
-- within their category — so nobody's sidebar changes on deploy.
ALTER TABLE group_channels
    ADD COLUMN IF NOT EXISTS position integer NOT NULL DEFAULT 0;

UPDATE group_channels gc
   SET position = ranked.pos
  FROM (
        SELECT id,
               (row_number() OVER (
                    PARTITION BY server_id, category_id
                    ORDER BY created_at, id
               ) - 1)::integer AS pos
          FROM group_channels
         WHERE server_id IS NOT NULL
       ) ranked
 WHERE gc.id = ranked.id;

CREATE INDEX IF NOT EXISTS idx_group_channels_server_layout
    ON group_channels (server_id, category_id, position)
    WHERE server_id IS NOT NULL;
