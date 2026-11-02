-- AmneziaWG: a fleet protocol of its own, with one relay per machine, and every
-- account's WireGuard key and tunnel address. Unlike the other credentials a key
-- pair needs code to make, so an account gets one when it first asks for a link
-- that carries it (internal/subscription/awg.go).
ALTER TABLE entry_boxes DROP CONSTRAINT entry_boxes_proto_check;
ALTER TABLE entry_boxes ADD CONSTRAINT entry_boxes_proto_check
    CHECK (proto = ANY (ARRAY['vless-reality-vision', 'hysteria2', 'vless-xhttp', 'amneziawg']));

-- The tunnel addresses: the Nth account takes the Nth host of 10.66.0.0/16,
-- host 1 being the vantage's on every relay.
CREATE SEQUENCE hub_awg_address_seq START WITH 2 MAXVALUE 64770 NO CYCLE;

ALTER TABLE hub_access_keys
    ADD COLUMN awg_private text UNIQUE,
    ADD COLUMN awg_public text UNIQUE,
    ADD COLUMN awg_address inet UNIQUE,
    ADD CONSTRAINT hub_access_keys_awg_whole CHECK (
        (awg_private IS NULL) = (awg_public IS NULL) AND (awg_private IS NULL) = (awg_address IS NULL));
