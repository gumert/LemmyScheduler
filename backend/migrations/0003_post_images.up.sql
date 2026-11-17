CREATE TABLE post_images (
    id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    scheduled_post_id bigint NOT NULL REFERENCES scheduled_posts(id) ON DELETE CASCADE,
    content_type text NOT NULL,
    size_bytes integer NOT NULL CHECK (size_bytes > 0 AND size_bytes <= 10485760 AND size_bytes = octet_length(data)),
    data bytea NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE (scheduled_post_id, id)
);

ALTER TABLE scheduled_posts ADD COLUMN title_image_id bigint;
ALTER TABLE scheduled_posts ADD CONSTRAINT scheduled_posts_title_image_owner_fk
    FOREIGN KEY (id, title_image_id) REFERENCES post_images (scheduled_post_id, id);
