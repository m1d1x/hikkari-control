-- 052 — the second stream protocol: VLESS + XHTTP inside ordinary TLS on the
-- box's own domain (roadmap 42 П4в).
--
-- An XHTTP box is an HTTPS site with a real certificate for a throwaway
-- domain, not tied to the brand (П4б rule), and the tunnel under a secret path.
-- The domain belongs to the box: its A record points at the box's address, and
-- Let's Encrypt validates it on that address's :443. So it is recorded with
-- the box, sealed like the address (039) — a list of our domains in plain is
-- the same gift to a censor as a list of our addresses. Relays minted on the
-- box copy it into their own sealed identity, which is what the signed record
-- and the edge's env are built from.
--
-- A domain can die on its own while the address lives (the censor blocks the
-- name by SNI): `domain_blocked` is that death, cured by a new domain, not a
-- new machine — a label the abuse dataset must not file as ip_banned.

ALTER TABLE entry_boxes DROP CONSTRAINT entry_boxes_proto_check;
ALTER TABLE entry_boxes
    ADD CONSTRAINT entry_boxes_proto_check
        CHECK (proto IN ('vless-reality-vision', 'hysteria2', 'vless-xhttp')),
    ADD COLUMN domain_ct bytea,
    ADD CONSTRAINT entry_boxes_xhttp_has_domain CHECK ((proto = 'vless-xhttp') = (domain_ct IS NOT NULL));

ALTER TABLE entry_relays DROP CONSTRAINT entry_relays_died_reason_check;
ALTER TABLE entry_relays
    ADD CONSTRAINT entry_relays_died_reason_check CHECK (died_reason IN
        ('ip_banned', 'fingerprint', 'destination', 'provider_down', 'rotated', 'other', 'domain_blocked'));
