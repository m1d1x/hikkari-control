-- The TLS fingerprint a subscriber's client presents on this node, when it is
-- not the default one: a knob of the logical node, so a replacement relay keeps
-- it. The relay never sees it; only the client's link carries it.
ALTER TABLE entry_nodes ADD COLUMN IF NOT EXISTS fingerprint text
  CHECK (fingerprint IN ('chrome', 'firefox', 'safari', 'edge', 'ios', 'android', 'randomized'));
