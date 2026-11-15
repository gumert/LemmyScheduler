CREATE TABLE scheduled_posts (
    id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    title text NOT NULL,
    body text NOT NULL DEFAULT '',
    url text NOT NULL DEFAULT '',
    community text NOT NULL,
    nsfw boolean NOT NULL DEFAULT false,
    status text NOT NULL DEFAULT 'draft'
        CHECK (status IN ('draft', 'scheduled', 'paused', 'posting', 'posted', 'needs_review', 'failed')),
    scheduled_at timestamptz,
    posted_at timestamptz,
    lemmy_post_url text NOT NULL DEFAULT '',
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT scheduled_posts_scheduled_at_required
        CHECK (status NOT IN ('scheduled', 'paused') OR scheduled_at IS NOT NULL)
);

CREATE INDEX scheduled_posts_status_scheduled_at_idx
    ON scheduled_posts (status, scheduled_at);

DROP TABLE main;
