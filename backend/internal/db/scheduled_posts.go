package db

import (
	"context"
	"errors"
	"fmt"

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
	return posts, nil
}

func (s scheduledPostStore) UpdateScheduledPost(ctx context.Context, post scheduledposts.ScheduledPost) (scheduledposts.ScheduledPost, error) {
	query := `UPDATE scheduled_posts SET
		title = $2, body = $3, url = $4, community = $5, nsfw = $6, status = $7,
		scheduled_at = $8, updated_at = now()
		WHERE id = $1 AND status IN ('draft', 'scheduled', 'paused', 'failed')
		RETURNING id, title, body, url, community, nsfw, status, scheduled_at, posted_at,
		          lemmy_post_url, created_at, updated_at`
	updated, err := scanScheduledPost(s.pool.QueryRow(ctx, query,
		post.ID, post.Title, post.Body, post.URL, post.Community, post.NSFW, string(post.Status), post.ScheduledAt,
	))
	if err == nil {
		return updated, nil
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
