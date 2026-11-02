-- The autopilot's pools may hold AmneziaWG. This only allows the row: a lane
-- starts when one is added, since a machine costs money the owner decides on.
ALTER TABLE entry_fleet_pools DROP CONSTRAINT entry_fleet_targets_proto_check;
ALTER TABLE entry_fleet_pools ADD CONSTRAINT entry_fleet_targets_proto_check
    CHECK (proto = ANY (ARRAY['vless-reality-vision', 'hysteria2', 'vless-xhttp', 'amneziawg']));
