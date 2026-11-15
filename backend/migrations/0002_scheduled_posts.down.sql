DROP TABLE scheduled_posts;

CREATE TABLE main (
    id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    text varchar NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now()
);
