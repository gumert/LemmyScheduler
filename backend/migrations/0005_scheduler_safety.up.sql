CREATE TABLE post_attempts (
    id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    post_id bigint NOT NULL,
    title_snapshot text NOT NULL,
    started_at timestamptz NOT NULL,
    finished_at timestamptz,
    outcome text NOT NULL DEFAULT 'started'
        CHECK (outcome IN ('started', 'posted', 'needs_review', 'recovered_on_startup')),
    message text NOT NULL DEFAULT '',
    CONSTRAINT post_attempts_outcome_finished_at_consistent
        CHECK ((outcome = 'started') = (finished_at IS NULL))
);

CREATE UNIQUE INDEX post_attempts_one_open_per_post_idx
    ON post_attempts (post_id) WHERE outcome = 'started';
CREATE INDEX post_attempts_post_started_idx
    ON post_attempts (post_id, started_at DESC, id DESC);

ALTER TABLE lemmy_account
    ADD COLUMN queue_paused_until timestamptz,
    ADD COLUMN auth_failed boolean NOT NULL DEFAULT false,
    ADD COLUMN account_generation bigint NOT NULL DEFAULT 0;

ALTER TABLE scheduled_posts
    ADD CONSTRAINT scheduled_posts_url_title_image_exclusive
    CHECK (url = '' OR title_image_id IS NULL);

ALTER TABLE scheduled_posts
    ADD COLUMN lemmy_post_id bigint,
    ADD CONSTRAINT scheduled_posts_lemmy_post_id_positive
    CHECK (lemmy_post_id IS NULL OR lemmy_post_id > 0);
