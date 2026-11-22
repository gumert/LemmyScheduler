ALTER TABLE scheduled_posts
    DROP CONSTRAINT scheduled_posts_url_title_image_exclusive;

ALTER TABLE scheduled_posts
    DROP CONSTRAINT scheduled_posts_lemmy_post_id_positive,
    DROP COLUMN lemmy_post_id;

ALTER TABLE lemmy_account
    DROP COLUMN account_generation,
    DROP COLUMN auth_failed,
    DROP COLUMN queue_paused_until;

DROP TABLE post_attempts;
