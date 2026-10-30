-- 011 — a voice note carries its duration and its waveform.
--
-- The recorder measures both at the moment of recording: the duration the
-- container reports (audio actually captured — a paused take is as long as what
-- was said), and one bar per slot, 0..100, taken at the output of the sender's
-- DSP chain. Neither had anywhere to live. The wire's Attachment carried a key,
-- a type, a name, a size and pixel dimensions; a voice note has no pixels and
-- the two facts it does have were dropped at the first seam they met.
--
-- What that looked like: the sender's own bubble showed «00:00» with no strip
-- (its optimistic row never received them either), and the recipient got a play
-- button over silence — a message whose entire content is audio, delivered with
-- nothing that describes the audio.
--
-- # Why columns and not the file's own bytes
--
-- Both are derivable by decoding the file. That is exactly what must not be
-- required: a chat list draws the strip for every note in view, and a reader
-- listens to a note on purpose, not on sight — so deriving them would mean
-- downloading (and decrypting, and decoding) every voice note anybody sent in
-- order to render a row. They are denormalised for the same reason an image's
-- width and height are: the bubble needs them BEFORE the bytes.
--
-- # Why smallint[] and not jsonb
--
-- The waveform is a fixed-range integer sequence and nothing else; an array
-- says that in the schema instead of in a comment, reads legibly in psql, and
-- costs two bytes a bar. `messages.attachment_meta` (a jsonb column from the
-- baseline that nothing has ever written) is deliberately left alone rather
-- than repurposed: a dead column with a new meaning is a trap for whoever finds
-- it next, and `channel_messages` never had it anyway.
--
-- Nullable, with no default: «this message is not a voice note» and «this note
-- was recorded before the client measured anything» are both honestly NULL, and
-- a zero-length array would claim a strip with no bars.

ALTER TABLE messages         ADD COLUMN voice_duration_ms integer;
ALTER TABLE messages         ADD COLUMN voice_waveform    smallint[];
ALTER TABLE channel_messages ADD COLUMN voice_duration_ms integer;
ALTER TABLE channel_messages ADD COLUMN voice_waveform    smallint[];
