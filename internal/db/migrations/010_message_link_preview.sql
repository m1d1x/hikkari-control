-- 010 — a message carries the card its link deserves.
--
-- Until now the card was asked for by the CLIENT, once per reader, when the
-- bubble that draws it first built (`get_link_preview`, op 76). Two costs
-- followed, and the second is the one people saw:
--
--   * the answer landed after the frame that needed it, so the card appeared
--     under a text bubble and grew it from text-width to card-width, shoving
--     every row above it up;
--   * every reader of a busy room asked again, and the only thing standing
--     between that and the third-party host was a server-side cache with a
--     TTL — a card that had expired cost the next reader a live fetch on the
--     path of a paint.
--
-- Resolving once, at the moment the message is stored, removes both: the card
-- travels WITH the message, so the bubble knows on its first frame whether
-- there is one, and no reader's render ever waits on a network.
--
-- # Why a ciphertext column and not the card in the clear
--
-- The card is derived from a link somebody sent to somebody. Its title and
-- description describe what a private conversation was about, as plainly as
-- the message body does — and that body is stored encrypted (content_ct). A
-- card next to it in the clear would hand a database dump the gist of every
-- conversation that ever contained a link, which is most of them. So it is
-- sealed with the same box and the same key rotation as the body, and carries
-- its own key version for the same reason the body does.
--
-- The payload is the JSON of linkpreview.Preview: url, title, description,
-- siteName, imageKey, isVideo. JSON rather than columns because it is ONE
-- value that is always read and written whole — the bubble needs all of it or
-- none of it — and because a shape that may gain a field (a favicon, a
-- published-at) should not cost a migration each time.
--
-- # Why nullable, with no backfill
--
-- Null means «not resolved», which covers three different facts the reader
-- cannot tell apart anyway: the message predates this column, the resolve is
-- still in flight, or it finished and the page had no card. The client renders
-- all three the same way — no card — which is also what it did before this
-- existed.
--
-- Messages written before this migration keep their null: re-resolving history
-- would dial every host anybody has ever linked, on a schedule nobody asked
-- for, to fill in cards under messages nobody is looking at.
ALTER TABLE messages
    ADD COLUMN IF NOT EXISTS link_preview_ct bytea,
    ADD COLUMN IF NOT EXISTS link_preview_key_version smallint;

ALTER TABLE channel_messages
    ADD COLUMN IF NOT EXISTS link_preview_ct bytea,
    ADD COLUMN IF NOT EXISTS link_preview_key_version smallint;
