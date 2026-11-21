package db

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/gumert/LemmyScheduler/backend/internal/scheduledposts"
)

type scheduledPostStore struct {
	pool *pgxpool.Pool
}

func NewScheduledPostStore(pool *pgxpool.Pool) scheduledposts.Store {
	return scheduledPostStore{pool: pool}
}

func (s scheduledPostStore) CreateScheduledPost(ctx context.Context, post scheduledposts.ScheduledPost) (scheduledposts.ScheduledPost, error) {
	query := `INSERT INTO scheduled_posts
		(title, body, url, community, nsfw, status, scheduled_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7)
		RETURNING id, title, body, url, community, nsfw, status, scheduled_at, posted_at,
		          lemmy_post_url, created_at, updated_at`
	created, err := scanScheduledPost(s.pool.QueryRow(ctx, query,
		post.Title, post.Body, post.URL, post.Community, post.NSFW, string(post.Status), post.ScheduledAt,
	))
	if err != nil {
		return scheduledposts.ScheduledPost{}, fmt.Errorf("db: create scheduled post: %w", err)
	}
	return created, nil
}

func (s scheduledPostStore) GetScheduledPost(ctx context.Context, id int64) (scheduledposts.ScheduledPost, error) {
	post, err := scanScheduledPost(s.pool.QueryRow(ctx, `SELECT
		id, title, body, url, community, nsfw, status, scheduled_at, posted_at,
		lemmy_post_url, created_at, updated_at
		FROM scheduled_posts WHERE id = $1`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return scheduledposts.ScheduledPost{}, scheduledposts.ErrNotFound
	}
	if err != nil {
		return scheduledposts.ScheduledPost{}, fmt.Errorf("db: get scheduled post: %w", err)
	}
	posts := []scheduledposts.ScheduledPost{post}
	if err := s.loadImageMetadata(ctx, posts); err != nil {
		return scheduledposts.ScheduledPost{}, err
	}
	post = posts[0]
	return post, nil
}

func (s scheduledPostStore) ListScheduledPosts(ctx context.Context, options scheduledposts.ListOptions) ([]scheduledposts.ScheduledPost, error) {
	if options.Limit <= 0 {
		return nil, errors.New("db: scheduled post list limit must be positive")
	}
	statuses := make([]string, len(options.Statuses))
	for i, status := range options.Statuses {
		statuses[i] = string(status)
	}

	direction, comparison := "ASC", ">"
	if options.Descending {
		direction, comparison = "DESC", "<"
	}
	query := `SELECT id, title, body, url, community, nsfw, status, scheduled_at, posted_at,
		lemmy_post_url, created_at, updated_at
		FROM scheduled_posts
		WHERE (COALESCE(cardinality($1::text[]), 0) = 0 OR status = ANY($1::text[]))
		AND ($2::timestamptz IS NULL OR (COALESCE(scheduled_at, created_at), id) ` + comparison + ` ($2::timestamptz, $3::bigint))
		ORDER BY COALESCE(scheduled_at, created_at) ` + direction + `, id ` + direction + `
		LIMIT $4`

	var afterTime any
	var afterID int64
	if options.After != nil {
		afterTime = options.After.SortTime
		afterID = options.After.ID
	}
	rows, err := s.pool.Query(ctx, query, statuses, afterTime, afterID, options.Limit)
	if err != nil {
		return nil, fmt.Errorf("db: list scheduled posts: %w", err)
	}
	defer rows.Close()

	posts := make([]scheduledposts.ScheduledPost, 0, options.Limit)
	for rows.Next() {
		post, err := scanScheduledPost(rows)
		if err != nil {
			return nil, fmt.Errorf("db: scan scheduled post: %w", err)
		}
		posts = append(posts, post)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("db: list scheduled posts: %w", err)
	}
	rows.Close()
	if err := s.loadImageMetadata(ctx, posts); err != nil {
		return nil, err
	}
	return posts, nil
}

func (s scheduledPostStore) UpdateScheduledPost(ctx context.Context, post scheduledposts.ScheduledPost) (scheduledposts.ScheduledPost, error) {
	query := `UPDATE scheduled_posts SET
		title = $2, body = $3, url = $4, community = $5, nsfw = $6, status = $7,
		scheduled_at = $8, updated_at = now()
		WHERE id = $1 AND status IN ('draft', 'scheduled', 'paused', 'failed')
		RETURNING id, title, body, url, community, nsfw, status, scheduled_at, posted_at,
		          lemmy_post_url, created_at, updated_at`
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return scheduledposts.ScheduledPost{}, fmt.Errorf("db: begin update scheduled post: %w", err)
	}
	defer tx.Rollback(context.Background())
	updated, err := scanScheduledPost(tx.QueryRow(ctx, query,
		post.ID, post.Title, post.Body, post.URL, post.Community, post.NSFW, string(post.Status), post.ScheduledAt,
	))
	if err == nil {
		if err := reconcileBodyImages(ctx, tx, updated.ID, updated.Body); err != nil {
			return scheduledposts.ScheduledPost{}, err
		}
		if err := tx.Commit(ctx); err != nil {
			return scheduledposts.ScheduledPost{}, fmt.Errorf("db: commit update scheduled post: %w", err)
		}
		posts := []scheduledposts.ScheduledPost{updated}
		if err := s.loadImageMetadata(ctx, posts); err != nil {
			return scheduledposts.ScheduledPost{}, err
		}
		return posts[0], nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return scheduledposts.ScheduledPost{}, fmt.Errorf("db: update scheduled post: %w", err)
	}

	current, getErr := s.GetScheduledPost(ctx, post.ID)
	if getErr != nil {
		return scheduledposts.ScheduledPost{}, getErr
	}
	if !scheduledposts.CanEdit(current.Status) {
		return scheduledposts.ScheduledPost{}, scheduledposts.ErrNotEditable
	}
	return scheduledposts.ScheduledPost{}, errors.New("db: scheduled post update affected no row")
}

// reconcileBodyImages runs inside the update transaction, which holds the post
// row lock. It rejects body references to images the post does not own and
// deletes stored images that are neither referenced nor the title image.
func reconcileBodyImages(ctx context.Context, tx pgx.Tx, postID int64, body string) error {
	var titleImageID *int64
	if err := tx.QueryRow(ctx, `SELECT title_image_id FROM scheduled_posts WHERE id = $1`, postID).Scan(&titleImageID); err != nil {
		return fmt.Errorf("db: read title image: %w", err)
	}
	referenced := scheduledposts.BodyImageIDs(body)
	rows, err := tx.Query(ctx, `SELECT id FROM post_images WHERE scheduled_post_id = $1`, postID)
	if err != nil {
		return fmt.Errorf("db: list post images: %w", err)
	}
	owned := make(map[int64]struct{})
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return fmt.Errorf("db: scan post image: %w", err)
		}
		owned[id] = struct{}{}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return fmt.Errorf("db: list post images: %w", err)
	}
	for _, id := range referenced {
		if _, ok := owned[id]; !ok {
			return &scheduledposts.ValidationError{Field: "body", Message: fmt.Sprintf("references unknown image %d", id)}
		}
	}
	if referenced == nil {
		referenced = []int64{}
	}
	if _, err := tx.Exec(ctx, `DELETE FROM post_images
		WHERE scheduled_post_id = $1 AND id <> ALL($2::bigint[]) AND id IS DISTINCT FROM $3::bigint`,
		postID, referenced, titleImageID); err != nil {
		return fmt.Errorf("db: delete unreferenced images: %w", err)
	}
	return nil
}

func (s scheduledPostStore) loadImageMetadata(ctx context.Context, posts []scheduledposts.ScheduledPost) error {
	if len(posts) == 0 {
		return nil
	}
	ids := make([]int64, len(posts))
	byID := make(map[int64]*scheduledposts.ScheduledPost, len(posts))
	for i := range posts {
		ids[i] = posts[i].ID
		byID[posts[i].ID] = &posts[i]
	}
	rows, err := s.pool.Query(ctx, `SELECT i.scheduled_post_id, i.id, i.content_type, i.created_at,
		COALESCE(p.title_image_id = i.id, false)
		FROM post_images i JOIN scheduled_posts p ON p.id = i.scheduled_post_id
		WHERE i.scheduled_post_id = ANY($1::bigint[]) ORDER BY i.id`, ids)
	if err != nil {
		return fmt.Errorf("db: load image metadata: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var postID int64
		var title bool
		var image scheduledposts.ImageMetadata
		if err := rows.Scan(&postID, &image.ID, &image.ContentType, &image.CreatedAt, &title); err != nil {
			return fmt.Errorf("db: scan image metadata: %w", err)
		}
		byID[postID].Images = append(byID[postID].Images, image)
		if title {
			byID[postID].TitleImage = &image
		}
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("db: load image metadata: %w", err)
	}
	return nil
}

func (s scheduledPostStore) DeleteScheduledPost(ctx context.Context, id int64) error {
	var deletedID int64
	err := s.pool.QueryRow(ctx, `DELETE FROM scheduled_posts
		WHERE id = $1 AND status <> 'posting' RETURNING id`, id).Scan(&deletedID)
	if err == nil {
		return nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return fmt.Errorf("db: delete scheduled post: %w", err)
	}

	current, getErr := s.GetScheduledPost(ctx, id)
	if getErr != nil {
		return getErr
	}
	if current.Status == scheduledposts.StatusPosting {
		return scheduledposts.ErrNotDeletable
	}
	return errors.New("db: scheduled post delete affected no row")
}

func (s scheduledPostStore) BulkUpdate(ctx context.Context, request scheduledposts.BulkRequest) (scheduledposts.BulkResult, error) {
	statuses := make([]string, 0, 7)
	for _, status := range scheduledposts.BulkEligibleStatuses(request.Action) {
		statuses = append(statuses, string(status))
	}
	var query string
	args := []any{request.IDs, statuses}
	switch request.Action {
	case scheduledposts.BulkDelete:
		query = `DELETE FROM scheduled_posts WHERE id = ANY($1::bigint[]) AND status = ANY($2::text[]) RETURNING id`
	case scheduledposts.BulkPause:
		query = `UPDATE scheduled_posts SET status = 'paused', updated_at = now()
			WHERE id = ANY($1::bigint[]) AND status = ANY($2::text[]) RETURNING id`
	case scheduledposts.BulkResume:
		query = `UPDATE scheduled_posts SET status = 'scheduled', updated_at = now()
			WHERE id = ANY($1::bigint[]) AND status = ANY($2::text[]) AND scheduled_at > now() RETURNING id`
	case scheduledposts.BulkDelay:
		query = `UPDATE scheduled_posts SET scheduled_at = scheduled_at + $3::bigint * interval '1 second', updated_at = now()
			WHERE id = ANY($1::bigint[]) AND status = ANY($2::text[])
			AND scheduled_at + $3::bigint * interval '1 second' > now() RETURNING id`
		args = append(args, int64(request.Delay/time.Second))
	default:
		return scheduledposts.BulkResult{}, fmt.Errorf("db: unknown bulk action %q", request.Action)
	}

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return scheduledposts.BulkResult{}, fmt.Errorf("db: begin bulk update: %w", err)
	}
	defer tx.Rollback(context.Background())

	updated, err := scanIDs(tx.Query(ctx, query, args...))
	if err != nil {
		return scheduledposts.BulkResult{}, fmt.Errorf("db: bulk %s: %w", request.Action, err)
	}
	result := scheduledposts.BulkResult{UpdatedIDs: updated}
	done := make(map[int64]struct{}, len(updated))
	for _, id := range updated {
		done[id] = struct{}{}
	}
	var remaining []int64
	for _, id := range request.IDs {
		if _, ok := done[id]; !ok {
			remaining = append(remaining, id)
		}
	}

	if len(remaining) > 0 {
		rows, err := tx.Query(ctx, `SELECT id, status, scheduled_at, now() FROM scheduled_posts WHERE id = ANY($1::bigint[])`, remaining)
		if err != nil {
			return scheduledposts.BulkResult{}, fmt.Errorf("db: classify skipped posts: %w", err)
		}
		found := make(map[int64]scheduledposts.BulkSkip, len(remaining))
		for rows.Next() {
			var post scheduledposts.ScheduledPost
			var status string
			var scheduledAt pgtype.Timestamptz
			var now time.Time
			if err := rows.Scan(&post.ID, &status, &scheduledAt, &now); err != nil {
				rows.Close()
				return scheduledposts.BulkResult{}, fmt.Errorf("db: scan skipped post: %w", err)
			}
			post.Status = scheduledposts.Status(status)
			if scheduledAt.Valid {
				post.ScheduledAt = &scheduledAt.Time
			}
			skip, ok := scheduledposts.CheckBulk(request, post, now)
			if ok {
				// Changed after the write; the post no longer matches what we saw.
				skip = scheduledposts.BulkSkip{ID: post.ID, Reason: scheduledposts.SkipWrongStatus, Message: "post changed while the action was running"}
			}
			found[post.ID] = skip
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return scheduledposts.BulkResult{}, fmt.Errorf("db: classify skipped posts: %w", err)
		}
		for _, id := range remaining {
			skip, ok := found[id]
			if !ok {
				skip = scheduledposts.BulkSkip{ID: id, Reason: scheduledposts.SkipNotFound, Message: "scheduled post not found"}
			}
			result.Skipped = append(result.Skipped, skip)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return scheduledposts.BulkResult{}, fmt.Errorf("db: commit bulk update: %w", err)
	}
	return result, nil
}

func scanIDs(rows pgx.Rows, err error) ([]int64, error) {
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

func scanScheduledPost(row pgx.Row) (scheduledposts.ScheduledPost, error) {
	var post scheduledposts.ScheduledPost
	var status string
	var scheduledAt, postedAt pgtype.Timestamptz
	err := row.Scan(
		&post.ID, &post.Title, &post.Body, &post.URL, &post.Community, &post.NSFW, &status,
		&scheduledAt, &postedAt, &post.LemmyPostURL, &post.CreatedAt, &post.UpdatedAt,
	)
	if err != nil {
		return scheduledposts.ScheduledPost{}, err
	}
	post.Status = scheduledposts.Status(status)
	if scheduledAt.Valid {
		post.ScheduledAt = &scheduledAt.Time
	}
	if postedAt.Valid {
		post.PostedAt = &postedAt.Time
	}
	return post, nil
}
