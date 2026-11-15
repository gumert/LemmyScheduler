-- Placeholder table: proves the migration and connection path end to end.
-- timestamptz stores an absolute instant (UTC internally), per AGENTS.md.
CREATE TABLE main (
    id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    text varchar NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now()
);
