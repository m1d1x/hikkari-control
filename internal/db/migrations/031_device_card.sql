-- 031 — the device card: model and system, as a person recognises a device
-- («iPhone 12 mini», «iOS 18.1»), on the sessions list and the «подключить
-- это устройство?» screen.
--
-- The client reports it with its device binding; the server keeps it
-- encrypted under its own key (cryptobox), as it keeps the address. Metadata
-- is the server's to read; the envelope only keeps a leaked dump from handing
-- it out. Content is the end-to-end part, and this is not content.
--
-- user_sessions rows that predate it carry NULL and show as an unknown device
-- until that device signs in again.
--
-- e2e_device_links carries the joining device's card until the link turns into
-- a session, and its address moves under the same envelope: new_ip held it in
-- the clear, beside a sessions table that never did. Link rows live minutes
-- (24 h once approved), so the plaintext column is dropped, not migrated.

ALTER TABLE user_sessions ADD COLUMN device_card_ct bytea;

ALTER TABLE e2e_device_links DROP COLUMN new_ip;
ALTER TABLE e2e_device_links ADD COLUMN new_ip_ct bytea;
ALTER TABLE e2e_device_links ADD COLUMN new_card_ct bytea;
