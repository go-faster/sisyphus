ALTER TABLE chunks ADD COLUMN IF NOT EXISTS search_vector tsvector GENERATED ALWAYS AS (
    setweight(to_tsvector('simple', coalesce(title, '')), 'A') ||
    setweight(to_tsvector('simple', coalesce(text, '')), 'B')
) STORED;

CREATE INDEX IF NOT EXISTS chunks_search_idx ON chunks USING gin (search_vector);
