-- More knobs of the logical node, like fingerprint (021): the subscriber's
-- link carries them, so an A/B between two nodes of one machine needs no
-- client release (plans/2026-11-04-relay-search-method.md, stage 4).
--
-- sni: the Reality cover the link names. It has to be one of the relay's own
-- covers — the relay accepts only those — so a value the relay does not serve
-- is ignored by the hub and the link keeps its first cover. Not used on XHTTP:
-- there the name is the relay's certificate.
-- alpn: what the client offers in its ClientHello, comma-separated as links
-- write it.
--
-- The port is not a node knob: a relay listens on one port, and an A/B by port
-- is two relays of one box on different ports.
ALTER TABLE entry_nodes ADD COLUMN IF NOT EXISTS sni text
  CHECK (sni ~ '^[a-z0-9]([a-z0-9.-]{0,251}[a-z0-9])?$');
ALTER TABLE entry_nodes ADD COLUMN IF NOT EXISTS alpn text
  CHECK (alpn IN ('h2', 'http/1.1', 'h2,http/1.1', 'http/1.1,h2'));
