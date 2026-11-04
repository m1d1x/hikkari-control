-- chrome_155: the client core's (sing-box-lx) opt-in Chrome 155 preset. Its
-- "chrome" stays Chrome 133, the one cut from Russia by new connections
-- (hikkari-client plans/2026-11-04-relay-search-method.md: nekko-spb 04.11,
-- chrome 163/200 and chrome_155 196/200 in the same rounds). A node can now
-- ask for it, so an A/B against firefox reaches testers without a release.
ALTER TABLE entry_nodes DROP CONSTRAINT IF EXISTS entry_nodes_fingerprint_check;
ALTER TABLE entry_nodes ADD CONSTRAINT entry_nodes_fingerprint_check
  CHECK (fingerprint IN ('chrome', 'chrome_155', 'firefox', 'safari', 'edge', 'ios', 'android', 'randomized'));
