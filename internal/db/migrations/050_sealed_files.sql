-- 050 — sealed files: the bytes of an E2E attachment, under a key the server
-- never holds (roadmap 76.3, plan 76 §2.4).
--
-- An upload into an E2E chat is sealed on the sending device with a random
-- key that travels to the reader inside the sealed message. The server is
-- told the size and nothing else: no name, no type, no content hash, no
-- dimensions. So a sealed row keeps only what the server has — the storage
-- key, the size, the uploader, the time — and the CHECK below makes «the
-- server holds nothing about what the file is» a property of the table rather
-- than of whoever writes to it.
--
-- `verified` is TRUE for a sealed row: there is no claimed hash to re-derive
-- (and no key to read the bytes with), so the verify worker has nothing to
-- check and must never pick one up.
--
-- The kind of attachment (image | voice | …) stays where it already is, on the
-- message (`messages.attachment_type`, migration 026, plan §1.29), where the
-- recipient's «who may send me voice notes» setting is checked.

ALTER TABLE files ADD COLUMN sealed boolean NOT NULL DEFAULT false;

ALTER TABLE files ALTER COLUMN sha256 DROP NOT NULL;
ALTER TABLE files ALTER COLUMN content_type DROP NOT NULL;
ALTER TABLE files ALTER COLUMN file_category DROP NOT NULL;

ALTER TABLE files ADD CONSTRAINT files_sealed_knows_nothing CHECK (
    CASE WHEN sealed THEN
        sha256 IS NULL AND original_sha256 IS NULL
        AND content_type IS NULL AND file_category IS NULL
        AND original_name_ct IS NULL
        AND width IS NULL AND height IS NULL AND duration_ms IS NULL
        AND thumb_s3_key IS NULL AND micro_s3_key IS NULL
        AND verified
    ELSE
        -- A server-keyed row is described as before.
        sha256 IS NOT NULL AND content_type IS NOT NULL AND file_category IS NOT NULL
    END
);
