ALTER TABLE scheduled_posts DROP CONSTRAINT scheduled_posts_title_image_owner_fk;
ALTER TABLE scheduled_posts DROP COLUMN title_image_id;
DROP TABLE post_images;
