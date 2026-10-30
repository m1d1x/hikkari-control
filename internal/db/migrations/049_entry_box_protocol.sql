-- 049 — a box speaks one protocol, and a voice box serves one region (voice
-- plan Ф3; owner's rule «машина = 1 IP = 1 протокол», 15.10, confirmed for the
-- TCP + UDP pair 20.10).
--
-- Until now every box ran nekko-edge as a Reality stream entry, so the
-- protocol was implied. A voice entry is a Hysteria2 relay in front of one
-- voice region's SFUs: it forwards datagrams to that region's media addresses
-- (sfu_nodes.media_*, 048) and nowhere else, and a client picks it by the
-- region its voice ticket names. The protocol is a property of the box, not
-- of each relay on it: a ban then tells "the address" from "the protocol",
-- and detecting one protocol never buries another on the same address.
--
-- Every box so far is a Reality box; the column says so explicitly and keeps
-- no default — a new box names what it is.

ALTER TABLE entry_boxes
    ADD COLUMN proto text NOT NULL DEFAULT 'vless-reality-vision'
        CHECK (proto IN ('vless-reality-vision', 'hysteria2')),
    ADD COLUMN region text CHECK (region ~ '^[a-z0-9-]{1,32}$'),
    ADD CONSTRAINT entry_boxes_voice_has_region CHECK ((proto = 'hysteria2') = (region IS NOT NULL));

ALTER TABLE entry_boxes ALTER COLUMN proto DROP DEFAULT;
