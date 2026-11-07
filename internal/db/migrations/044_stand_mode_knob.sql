-- The stand varies the XHTTP mode (internal/stand Knobs["mode"]), on XHTTP
-- profiles only. The knob controller does not switch it: entry_nodes has no
-- mode column (internal/stand/controller.go nodeColumn), so the rows only feed
-- the dataset. stream-one is left out: nginx in front of the edge speaks
-- HTTP/1.1 to it and cannot carry a full-duplex request.
ALTER TABLE stand_knobs DROP CONSTRAINT IF EXISTS stand_knobs_knob_check;
ALTER TABLE stand_knobs ADD CONSTRAINT stand_knobs_knob_check
    CHECK (knob IN ('fp', 'sni', 'port', 'flow', 'alpn', 'burst', 'mode'));
INSERT INTO stand_knobs (knob, vals) VALUES ('mode', ARRAY['stream-up', 'packet-up'])
ON CONFLICT DO NOTHING;
