-- 004 — a ws-ticket can name the room it was minted for.
--
-- The ticket has always bound an IDENTITY (account, session, username) and left
-- the question of which room that identity may enter to the shard. For a voice
-- channel the shard can answer it: membership is a row it can read. For a 1:1
-- call it cannot — a call is not a membership, it is a live negotiation, and
-- while the signaling lived on the SFU socket the shard kept its own registry
-- of calls to answer from.
--
-- That registry moved to the messaging gateway (internal/messaging/dmcall.go),
-- because the socket a client always holds is the one that can ring it. So the
-- permission travels with the ticket instead: the gateway checks the caller
-- against the live call at mint time, and the shard admits a `dm:<callId>` room
-- on the strength of the claim.
--
-- Empty is the honest default and also the common case: an ordinary voice
-- channel names no room, and the shard keeps checking membership for those.
-- Nothing needs backfilling — tickets live thirty seconds.
ALTER TABLE ws_tickets
    ADD COLUMN IF NOT EXISTS room text NOT NULL DEFAULT '';
