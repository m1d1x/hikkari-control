-- 028 — a draft may be sealed by the client (roadmap 76.2.2.4, plan 76 §9).
--
-- A draft is text the account typed and has not sent. In an E2E chat that text
-- is exactly what the chat hides, so the client seals it under the account
-- history key (only the account's own devices hold it) and the server keeps
-- the blob: it relays it between the account's devices and cannot read it.
-- Beside a sealed blob there is no readable text or formatting — the CHECK is
-- the database's half of that rule.
--
-- New nullable column: existing drafts keep their server-key ciphertext.

ALTER TABLE drafts ADD COLUMN sealed bytea;

ALTER TABLE drafts ADD CONSTRAINT drafts_sealed_alone CHECK (
    sealed IS NULL OR (text_ct IS NULL AND entities_ct IS NULL)
);
