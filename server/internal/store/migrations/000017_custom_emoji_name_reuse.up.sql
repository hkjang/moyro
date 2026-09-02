-- Custom emoji deletion is a soft delete: the row stays with `delete_at` set so
-- old posts can still resolve the shortcode they were written with. The v0.1
-- baseline nevertheless declared `emojis.name` as a plain column-level UNIQUE,
-- which spans deleted rows too. The practical effect was that deleting an emoji
-- permanently burned its name: the service's live-only collision probe found
-- nothing, the image was uploaded, and only the INSERT failed — so every retry
-- answered "emoji name already in use" for a name no live emoji held, and
-- orphaned another file on the way out.
--
-- Replace the table-wide constraint with a unique index restricted to live rows.
-- That is the invariant the service always documented as its race protection:
-- at most one undeleted emoji per name, with deleted rows free to repeat.
ALTER TABLE emojis DROP CONSTRAINT IF EXISTS emojis_name_key;

-- The baseline's non-unique partial index covered exactly the same live-name
-- lookups, so the unique index below replaces it outright rather than adding a
-- second structure over the same key.
DROP INDEX IF EXISTS emojis_live_idx;

CREATE UNIQUE INDEX IF NOT EXISTS emojis_live_name_idx
    ON emojis (name)
    WHERE delete_at = 0;
