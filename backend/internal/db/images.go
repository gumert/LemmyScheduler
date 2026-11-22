package db

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/gumert/LemmyScheduler/backend/internal/scheduledposts"
)

type imageStore struct {
	pool *pgxpool.Pool
}

func NewImageStore(pool *pgxpool.Pool) scheduledposts.ImageStore {
	return imageStore{pool: pool}
}

type lockedPost struct {
	body         string
	url          string
	titleImageID *int64
}

func lockEditablePost(ctx context.Context, tx pgx.Tx, postID int64) (lockedPost, error) {
	var status string
	var post lockedPost
	err := tx.QueryRow(ctx, `SELECT status, body, url, title_image_id FROM scheduled_posts WHERE id = $1 FOR UPDATE`, postID).Scan(&status, &post.body, &post.url, &post.titleImageID)
	if errors.Is(err, pgx.ErrNoRows) {
		return lockedPost{}, scheduledposts.ErrNotFound
	}
	if err != nil {
		return lockedPost{}, fmt.Errorf("db: lock scheduled post: %w", err)
	}
	if !scheduledposts.CanEdit(scheduledposts.Status(status)) {
		return lockedPost{}, scheduledposts.ErrNotEditable
	}
	return post, nil
}

func referencedInBody(body string, imageID int64) bool {
	return strings.Contains(body, "[[image:"+strconv.FormatInt(imageID, 10)+"]]")
}

func clearTitleImage(ctx context.Context, tx pgx.Tx, postID int64, post lockedPost) error {
	if post.titleImageID == nil {
		return nil
	}
	if _, err := tx.Exec(ctx, `UPDATE scheduled_posts SET title_image_id = NULL WHERE id = $1`, postID); err != nil {
		return fmt.Errorf("db: clear title image: %w", err)
	}
	if !referencedInBody(post.body, *post.titleImageID) {
		if _, err := tx.Exec(ctx, `DELETE FROM post_images WHERE scheduled_post_id = $1 AND id = $2`, postID, *post.titleImageID); err != nil {
			return fmt.Errorf("db: delete unused title image: %w", err)
		}
	}
	return nil
}

func (s imageStore) PutTitleImage(ctx context.Context, postID int64, contentType string, data []byte) (scheduledposts.ImageMetadata, error) {
	return s.createImage(ctx, postID, true, contentType, data)
}

func (s imageStore) CreateBodyImage(ctx context.Context, postID int64, contentType string, data []byte) (scheduledposts.ImageMetadata, error) {
	return s.createImage(ctx, postID, false, contentType, data)
}

func (s imageStore) createImage(ctx context.Context, postID int64, title bool, contentType string, data []byte) (scheduledposts.ImageMetadata, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return scheduledposts.ImageMetadata{}, fmt.Errorf("db: begin image upload: %w", err)
	}
	defer tx.Rollback(context.Background())
	post, err := lockEditablePost(ctx, tx, postID)
	if err != nil {
		return scheduledposts.ImageMetadata{}, err
	}
	if title {
		if post.url != "" {
			return scheduledposts.ImageMetadata{}, &scheduledposts.ValidationError{Field: "title_image", Message: "cannot be used with a URL"}
		}
		if err := clearTitleImage(ctx, tx, postID, post); err != nil {
			return scheduledposts.ImageMetadata{}, err
		}
	}
	var count int
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM post_images WHERE scheduled_post_id = $1`, postID).Scan(&count); err != nil {
		return scheduledposts.ImageMetadata{}, fmt.Errorf("db: count images: %w", err)
	}
	if count >= scheduledposts.MaxPostImages {
		return scheduledposts.ImageMetadata{}, scheduledposts.ErrImageLimit
	}
	var image scheduledposts.ImageMetadata
	err = tx.QueryRow(ctx, `INSERT INTO post_images (scheduled_post_id, content_type, size_bytes, data)
		VALUES ($1, $2, $3, $4) RETURNING id, content_type, created_at`,
		postID, contentType, len(data), data).Scan(&image.ID, &image.ContentType, &image.CreatedAt)
	if err != nil {
		return scheduledposts.ImageMetadata{}, fmt.Errorf("db: insert image: %w", err)
	}
	var newTitleID any
	if title {
		newTitleID = image.ID
	} else {
		newTitleID = post.titleImageID
	}
	if _, err := tx.Exec(ctx, `UPDATE scheduled_posts SET title_image_id = $2, updated_at = now() WHERE id = $1`, postID, newTitleID); err != nil {
		return scheduledposts.ImageMetadata{}, fmt.Errorf("db: update image timestamp: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return scheduledposts.ImageMetadata{}, fmt.Errorf("db: commit image upload: %w", err)
	}
	return image, nil
}

func (s imageStore) SelectTitleImage(ctx context.Context, postID, imageID int64) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("db: begin title image selection: %w", err)
	}
	defer tx.Rollback(context.Background())
	post, err := lockEditablePost(ctx, tx, postID)
	if err != nil {
		return err
	}
	if post.url != "" {
		return &scheduledposts.ValidationError{Field: "title_image", Message: "cannot be used with a URL"}
	}
	var exists bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM post_images WHERE scheduled_post_id = $1 AND id = $2)`, postID, imageID).Scan(&exists); err != nil {
		return fmt.Errorf("db: find title image: %w", err)
	}
	if !exists {
		return scheduledposts.ErrImageNotFound
	}
	if post.titleImageID != nil && *post.titleImageID == imageID {
		return nil
	}
	if err := clearTitleImage(ctx, tx, postID, post); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `UPDATE scheduled_posts SET title_image_id = $2, updated_at = now() WHERE id = $1`, postID, imageID); err != nil {
		return fmt.Errorf("db: select title image: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("db: commit title image selection: %w", err)
	}
	return nil
}

func (s imageStore) GetImage(ctx context.Context, postID, imageID int64, title bool) (scheduledposts.Image, error) {
	var image scheduledposts.Image
	err := s.pool.QueryRow(ctx, `SELECT i.id, i.content_type, i.created_at, i.data FROM post_images i
		JOIN scheduled_posts p ON p.id = i.scheduled_post_id
		WHERE i.scheduled_post_id = $1 AND
		(($3 AND i.id = p.title_image_id) OR (NOT $3 AND i.id = $2))`,
		postID, imageID, title).Scan(&image.ID, &image.ContentType, &image.CreatedAt, &image.Data)
	if errors.Is(err, pgx.ErrNoRows) {
		return scheduledposts.Image{}, scheduledposts.ErrImageNotFound
	}
	if err != nil {
		return scheduledposts.Image{}, fmt.Errorf("db: get image: %w", err)
	}
	return image, nil
}

func (s imageStore) DeleteImage(ctx context.Context, postID, imageID int64, title bool) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("db: begin image delete: %w", err)
	}
	defer tx.Rollback(context.Background())
	post, err := lockEditablePost(ctx, tx, postID)
	if err != nil {
		return err
	}
	if title {
		if post.titleImageID == nil {
			return scheduledposts.ErrImageNotFound
		}
		if err := clearTitleImage(ctx, tx, postID, post); err != nil {
			return err
		}
	} else {
		var foundID int64
		err := tx.QueryRow(ctx, `SELECT id FROM post_images WHERE scheduled_post_id = $1 AND id = $2`, postID, imageID).Scan(&foundID)
		if errors.Is(err, pgx.ErrNoRows) {
			return scheduledposts.ErrImageNotFound
		}
		if err != nil {
			return fmt.Errorf("db: find image: %w", err)
		}
		if (post.titleImageID != nil && *post.titleImageID == imageID) || referencedInBody(post.body, imageID) {
			return scheduledposts.ErrImageInUse
		}
		if _, err := tx.Exec(ctx, `DELETE FROM post_images WHERE scheduled_post_id = $1 AND id = $2`, postID, imageID); err != nil {
			return fmt.Errorf("db: delete image: %w", err)
		}
	}
	if _, err := tx.Exec(ctx, `UPDATE scheduled_posts SET updated_at = now() WHERE id = $1`, postID); err != nil {
		return fmt.Errorf("db: update image timestamp: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("db: commit image delete: %w", err)
	}
	return nil
}
