-- 074 — each observation point carries its own token (audit 42-autopilot К1, В1, В2).
--
-- Every vantage, cmd/sfu-prober and any fleet box put into a vantage slot used
-- to share one PROBE_INGEST_TOKEN, and the body of a request named the vantage
-- (`sourceRegion`, the claim's `region`). So "which observation point said
-- this" was the sender's word: a region nobody registered was counted, a claim
-- could be finished by anyone, and the dialling credentials of the whole pool
-- went to whoever held the one string.
--
-- Now the hub issues a token per slot and keeps only its SHA-256 here. A
-- request's slot is the one its token belongs to, read on the hub — never a
-- field of the request. The token is random (32 bytes), so a plain hash is
-- enough: there is nothing to guess that a slow hash would protect.
--
-- A retired slot has no token: retiring is what takes the machine's voice
-- away. Slots that exist before this migration have none either and stay
-- mute until the owner issues one (re-saving the slot, or
-- `cmd/server entry-vantage-token <slot>` on the hub).

ALTER TABLE entry_vantages
    ADD COLUMN token_hash bytea UNIQUE
        CHECK (token_hash IS NULL OR octet_length(token_hash) = 32),
    ADD CONSTRAINT entry_vantages_retired_mute
        CHECK (status = 'active' OR token_hash IS NULL);
