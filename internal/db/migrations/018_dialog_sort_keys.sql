-- 018 — the dialog list's sort key is STORED, not derived per request.
--
-- ListChats orders a person's chats by «newest message I can see, else when the
-- chat was born». Derived inside the list query, that key costs one index probe
-- per chat the person has, BEFORE the LIMIT — the page of 50 paid for all 3000
-- of a busy account's chats (scripts/bench/dialogs-bench.sh: 43–117 ms at 3000
-- DMs, linear in the count). Stored and indexed, the page is read off the index
-- and the per-chat work (head, unread, mentions) runs for the page only.
--
-- The keys are maintained by TRIGGERS, not by the Go write paths, on purpose:
-- messages are written and deleted from half a dozen places (send, mirror
-- anchors, delete, clear history, room and server teardown, the account purger)
-- and any future path that forgot to bump the key would silently misorder the
-- list. A trigger cannot be forgotten. dialog_sort_keys_test.go recomputes
-- every key from scratch after each kind of write and demands equality.
--
-- DM: per PARTICIPANT, because «what I can see» is per participant — clearing
-- history moves the chat down for the one who cleared, not for the other side.
-- Channel: per channel; nothing hides a channel's messages from one member.

-- DM ------------------------------------------------------------------------

-- The key exactly as the list used to derive it: newest message created after
-- this participant's cleared_at, else the conversation's own birth.
CREATE FUNCTION dialog_dm_sort_at(conv uuid, cleared timestamptz) RETURNS timestamptz
LANGUAGE sql STABLE AS $$
	SELECT COALESCE(
		(SELECT max(created_at) FROM messages
		 WHERE conversation_id = conv AND (cleared IS NULL OR created_at > cleared)),
		(SELECT created_at FROM conversations WHERE id = conv))
$$;

ALTER TABLE conversation_participants ADD COLUMN sort_at timestamptz;
UPDATE conversation_participants SET sort_at = dialog_dm_sort_at(conversation_id, cleared_at);
ALTER TABLE conversation_participants ALTER COLUMN sort_at SET NOT NULL;

CREATE INDEX idx_conv_participants_dialogs
	ON conversation_participants (user_id, sort_at DESC, conversation_id DESC);

-- A participant row gets its key on insert, and again whenever its watermark
-- moves (clear history).
CREATE FUNCTION dialog_dm_participant_key() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
	NEW.sort_at := dialog_dm_sort_at(NEW.conversation_id, NEW.cleared_at);
	RETURN NEW;
END
$$;

CREATE TRIGGER trg_dialog_dm_participant_key
	BEFORE INSERT OR UPDATE OF cleared_at ON conversation_participants
	FOR EACH ROW EXECUTE FUNCTION dialog_dm_participant_key();

-- New messages only ever move a key forward, and only for the participants who
-- can see them. Statement-level with a transition table so a bulk insert costs
-- one UPDATE, not one per row.
CREATE FUNCTION dialog_dm_messages_inserted() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
	UPDATE conversation_participants cp
	SET sort_at = n.max_at
	FROM (SELECT conversation_id, max(created_at) AS max_at FROM new_rows GROUP BY conversation_id) n
	WHERE cp.conversation_id = n.conversation_id
	  AND cp.sort_at < n.max_at
	  AND (cp.cleared_at IS NULL OR n.max_at > cp.cleared_at);
	RETURN NULL;
END
$$;

CREATE TRIGGER trg_dialog_dm_messages_inserted
	AFTER INSERT ON messages
	REFERENCING NEW TABLE AS new_rows
	FOR EACH STATEMENT EXECUTE FUNCTION dialog_dm_messages_inserted();

-- A removed message matters only to the participants whose key it was (or was
-- newer than, which cannot happen but costs nothing to cover): they are
-- recomputed. The EXISTS guard covers the conversation being deleted in the
-- same statement — its participants go with it and have no key to fix.
CREATE FUNCTION dialog_dm_messages_removed() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
	UPDATE conversation_participants cp
	SET sort_at = dialog_dm_sort_at(cp.conversation_id, cp.cleared_at)
	FROM (SELECT conversation_id, max(created_at) AS max_at FROM old_rows GROUP BY conversation_id) o
	WHERE cp.conversation_id = o.conversation_id
	  AND cp.sort_at <= o.max_at
	  AND EXISTS (SELECT 1 FROM conversations c WHERE c.id = cp.conversation_id);
	RETURN NULL;
END
$$;

CREATE TRIGGER trg_dialog_dm_messages_removed
	AFTER DELETE ON messages
	REFERENCING OLD TABLE AS old_rows
	FOR EACH STATEMENT EXECUTE FUNCTION dialog_dm_messages_removed();

-- No write path moves a message in time or between chats today. If one ever
-- does, both the chat it left and the one it entered are recomputed rather
-- than left wrong. Row-level with a WHEN, so an edit — the update that does
-- happen — never runs it (Postgres allows no column list on a trigger with
-- transition tables).
CREATE FUNCTION dialog_dm_message_moved() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
	UPDATE conversation_participants cp
	SET sort_at = dialog_dm_sort_at(cp.conversation_id, cp.cleared_at)
	WHERE cp.conversation_id IN (OLD.conversation_id, NEW.conversation_id);
	RETURN NULL;
END
$$;

CREATE TRIGGER trg_dialog_dm_message_moved
	AFTER UPDATE OF created_at, conversation_id ON messages
	FOR EACH ROW
	WHEN (OLD.created_at IS DISTINCT FROM NEW.created_at OR OLD.conversation_id IS DISTINCT FROM NEW.conversation_id)
	EXECUTE FUNCTION dialog_dm_message_moved();

-- Channel -------------------------------------------------------------------

-- Newest placement in the channel (thread replies and mirror anchors included,
-- exactly as the list read it), else the channel's birth.
CREATE FUNCTION dialog_channel_sort_at(chan uuid) RETURNS timestamptz
LANGUAGE sql STABLE AS $$
	SELECT COALESCE(
		(SELECT max(created_at) FROM channel_messages WHERE channel_id = chan),
		(SELECT created_at FROM group_channels WHERE id = chan))
$$;

ALTER TABLE group_channels ADD COLUMN sort_at timestamptz;
UPDATE group_channels SET sort_at = dialog_channel_sort_at(id);
ALTER TABLE group_channels ALTER COLUMN sort_at SET NOT NULL;

-- A channel is born with no messages: its key is its birth.
CREATE FUNCTION dialog_channel_born() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
	NEW.sort_at := NEW.created_at;
	RETURN NEW;
END
$$;

CREATE TRIGGER trg_dialog_channel_born
	BEFORE INSERT ON group_channels
	FOR EACH ROW EXECUTE FUNCTION dialog_channel_born();

CREATE FUNCTION dialog_channel_messages_inserted() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
	UPDATE group_channels gc
	SET sort_at = n.max_at
	FROM (SELECT channel_id, max(created_at) AS max_at FROM new_rows GROUP BY channel_id) n
	WHERE gc.id = n.channel_id AND gc.sort_at < n.max_at;
	RETURN NULL;
END
$$;

CREATE TRIGGER trg_dialog_channel_messages_inserted
	AFTER INSERT ON channel_messages
	REFERENCING NEW TABLE AS new_rows
	FOR EACH STATEMENT EXECUTE FUNCTION dialog_channel_messages_inserted();

CREATE FUNCTION dialog_channel_messages_removed() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
	UPDATE group_channels gc
	SET sort_at = dialog_channel_sort_at(gc.id)
	FROM (SELECT channel_id, max(created_at) AS max_at FROM old_rows GROUP BY channel_id) o
	WHERE gc.id = o.channel_id AND gc.sort_at <= o.max_at;
	RETURN NULL;
END
$$;

CREATE TRIGGER trg_dialog_channel_messages_removed
	AFTER DELETE ON channel_messages
	REFERENCING OLD TABLE AS old_rows
	FOR EACH STATEMENT EXECUTE FUNCTION dialog_channel_messages_removed();

-- Same rule as the DM side.
CREATE FUNCTION dialog_channel_message_moved() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
	UPDATE group_channels gc
	SET sort_at = dialog_channel_sort_at(gc.id)
	WHERE gc.id IN (OLD.channel_id, NEW.channel_id);
	RETURN NULL;
END
$$;

CREATE TRIGGER trg_dialog_channel_message_moved
	AFTER UPDATE OF created_at, channel_id ON channel_messages
	FOR EACH ROW
	WHEN (OLD.created_at IS DISTINCT FROM NEW.created_at OR OLD.channel_id IS DISTINCT FROM NEW.channel_id)
	EXECUTE FUNCTION dialog_channel_message_moved();
