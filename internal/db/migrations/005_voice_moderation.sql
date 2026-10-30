-- 005 — a moderator can silence somebody in voice, and it outlives the socket.
--
-- Server mute and deafen are MODERATION, not the mute button: one person
-- silences another, and the silence stands until it is lifted. That is why it
-- is a row and not a flag on a live connection — a member who reconnects, or
-- who was offline when it was applied, must come back silenced. The SFU's own
-- `muted` flag is the opposite kind of thing: it lives in memory, it is the
-- member's own choice, and it dies with the socket.
--
-- Both columns sit on `server_members` rather than on a voice channel: the
-- sanction is server-wide, like a timeout, so moving between rooms does not
-- shed it. They are separate booleans rather than one enum because they are
-- separate permissions (MUTE_MEMBERS / DEAFEN_MEMBERS) — one moderator may hold
-- one and not the other.
--
-- Deafen implies mute in EFFECT (somebody who cannot hear the room has no
-- business speaking into it), but the columns stay independent: folding them
-- together here would make lifting a deafen silently lift a mute a different
-- moderator applied.
ALTER TABLE server_members
    ADD COLUMN IF NOT EXISTS voice_muted boolean NOT NULL DEFAULT false,
    ADD COLUMN IF NOT EXISTS voice_deafened boolean NOT NULL DEFAULT false;
