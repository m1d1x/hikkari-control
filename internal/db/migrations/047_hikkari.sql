-- Hikkari v1 is its own protocol on a domain of its own, beside Naive: the
-- same encrypted identity, grants, subscriber key usage and fleet machinery.
ALTER TABLE entry_boxes DROP CONSTRAINT entry_boxes_proto_check;
ALTER TABLE entry_boxes ADD CONSTRAINT entry_boxes_proto_check
 CHECK (proto = ANY (ARRAY['vless-reality-vision','hysteria2','vless-xhttp','amneziawg','naive','hikkari']));
ALTER TABLE entry_boxes DROP CONSTRAINT entry_boxes_domain_only_xhttp;
ALTER TABLE entry_boxes ADD CONSTRAINT entry_boxes_domain_only_xhttp
 CHECK (proto IN ('vless-xhttp','naive','hikkari') OR domain_ct IS NULL);
ALTER TABLE entry_fleet_pools DROP CONSTRAINT entry_fleet_targets_proto_check;
ALTER TABLE entry_fleet_pools ADD CONSTRAINT entry_fleet_targets_proto_check
 CHECK (proto = ANY (ARRAY['vless-reality-vision','hysteria2','vless-xhttp','amneziawg','naive','hikkari']));
