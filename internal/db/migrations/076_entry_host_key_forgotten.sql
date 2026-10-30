-- 076 — a forgotten host key is not a key never seen (audit 42 С6).
--
-- A box with no host key on record used to be one state, and any login could
-- take the first key it was shown. Two different things look like that: a
-- machine the hub rented and has not met yet (its first boot, an idle one
-- whose first deploy died before ssh), and a machine whose key the operator
-- dropped because it was reinstalled. The hub's own stand-up may meet the
-- first kind; only the operator's provisioning job may meet the second —
-- otherwise the first background job to reach :22 records whoever answers
-- there, a man in the middle included, and the deploy hands him the relays'
-- env.
--
-- Set by ForgetHostKey, cleared when a login records a key again.

ALTER TABLE entry_boxes ADD COLUMN host_key_forgotten_at timestamptz;
