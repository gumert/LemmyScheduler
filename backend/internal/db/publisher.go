package db

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/gumert/LemmyScheduler/backend/internal/scheduledposts"
)

const (
	publisherLockKey int64 = 0x4c656d6d79536368
	failurePause           = time.Hour
	publisherMessage       = "The publishing attempt was interrupted or did not complete."
)

var (
	ErrPublisherBusy = errors.New("db: another publisher owns the scheduler")
	ErrPublisherLost = errors.New("db: publisher ownership was lost")
	ErrStaleAttempt  = errors.New("db: attempt is no longer open for this posting")
)

// Publisher owns a dedicated physical PostgreSQL session for the lifetime of
// the publisher. Its session-level advisory lock is never taken from the pool.
type Publisher struct {
	conn   *pgxpool.Conn
	guard  chan struct{}
	mu     sync.Mutex
	lost   error
	lostCh chan error
	closed bool
}

var _ scheduledposts.SchedulerStore = (*Publisher)(nil)

// AcquirePublisher pins one physical session and fails rather than sharing
// publisher ownership with another process.
func AcquirePublisher(ctx context.Context, pool *pgxpool.Pool) (*Publisher, error) {
	conn, err := pool.Acquire(ctx)
	if err != nil {
		return nil, fmt.Errorf("db: acquire publisher connection: %w", err)
	}
	var acquired bool
	if err := conn.QueryRow(ctx, `SELECT pg_try_advisory_lock($1)`, publisherLockKey).Scan(&acquired); err != nil {
		closePhysicalConnection(conn)
		return nil, fmt.Errorf("db: acquire publisher lock: %w", err)
	}
	if !acquired {
		conn.Release()
		return nil, ErrPublisherBusy
	}
	p := &Publisher{conn: conn, guard: make(chan struct{}, 1), lostCh: make(chan error, 1)}
	p.guard <- struct{}{}
	return p, nil
}

func closePhysicalConnection(conn *pgxpool.Conn) {
	raw := conn.Hijack()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_ = raw.Close(ctx)
}

// Lost is closed with the first ownership failure. The buffered error is
// delivered before closure.
func (p *Publisher) Lost() <-chan error { return p.lostCh }

func (p *Publisher) lock(ctx context.Context) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-p.guard:
	}
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		p.guard <- struct{}{}
		return ErrPublisherLost
	}
	if p.lost != nil {
		err := p.lost
		p.mu.Unlock()
		p.guard <- struct{}{}
		return fmt.Errorf("%w: %v", ErrPublisherLost, err)
	}
	p.mu.Unlock()
	return nil
}

func (p *Publisher) unlock() {
	p.guard <- struct{}{}
}

func (p *Publisher) markLost(err error) {
	p.mu.Lock()
	if p.lost == nil {
		p.lost = err
		p.lostCh <- err
		close(p.lostCh)
	}
	p.mu.Unlock()
}

// Check performs a bounded liveness check on the lock-holding session.
func (p *Publisher) Check(ctx context.Context) error {
	if err := p.lock(ctx); err != nil {
		p.markLost(err)
		return fmt.Errorf("%w: liveness check: %v", ErrPublisherLost, err)
	}
	defer p.unlock()
	if err := p.conn.Ping(ctx); err != nil {
		p.markLost(err)
		return fmt.Errorf("%w: liveness check: %v", ErrPublisherLost, err)
	}
	return nil
}

// RecoverInterrupted converts all durable in-flight posts into review items.
// The post changes, attempt closures, and account pause commit atomically.
func (p *Publisher) RecoverInterrupted(ctx context.Context, now time.Time) ([]int64, error) {
	if err := p.lock(ctx); err != nil {
		return nil, err
	}
	defer p.unlock()

	tx, err := p.conn.Begin(ctx)
	if err != nil {
		return nil, p.uncertain("begin startup recovery", err)
	}
	defer tx.Rollback(context.Background())

	rows, err := tx.Query(ctx, `SELECT id FROM scheduled_posts WHERE status = 'posting' ORDER BY id FOR UPDATE`)
	if err != nil {
		return nil, p.uncertain("list interrupted posts", err)
	}
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return nil, p.uncertain("scan interrupted post", err)
		}
		ids = append(ids, id)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, p.uncertain("list interrupted posts", err)
	}
	if len(ids) == 0 {
		if err := tx.Commit(ctx); err != nil {
			return nil, p.uncertain("commit empty startup recovery", err)
		}
		return ids, nil
	}

	if _, err := tx.Exec(ctx, `UPDATE scheduled_posts SET status = 'needs_review', updated_at = $2
		WHERE id = ANY($1::bigint[]) AND status = 'posting'`, ids, now.UTC()); err != nil {
		return nil, p.uncertain("recover interrupted posts", err)
	}
	if _, err := tx.Exec(ctx, `UPDATE post_attempts SET outcome = 'recovered_on_startup',
		finished_at = $2, message = $3
		WHERE post_id = ANY($1::bigint[]) AND outcome = 'started'`, ids, now.UTC(), publisherMessage); err != nil {
		return nil, p.uncertain("close recovered attempts", err)
	}
	if _, err := tx.Exec(ctx, `UPDATE lemmy_account
		SET queue_paused_until = GREATEST(queue_paused_until, $1)
		WHERE id = true`, now.UTC().Add(failurePause)); err != nil {
		return nil, p.uncertain("pause queue after recovery", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, p.uncertain("commit startup recovery", err)
	}
	return ids, nil
}

// ClaimDue atomically marks the oldest due scheduled post as posting and
// creates its sole open audit attempt.
func (p *Publisher) ClaimDue(ctx context.Context, now time.Time) (scheduledposts.ClaimedPost, bool, error) {
	if err := p.lock(ctx); err != nil {
		return scheduledposts.ClaimedPost{}, false, err
	}
	defer p.unlock()

	tx, err := p.conn.Begin(ctx)
	if err != nil {
		return scheduledposts.ClaimedPost{}, false, p.uncertain("begin scheduled-post claim", err)
	}
	defer tx.Rollback(context.Background())

	post, err := scanScheduledPost(tx.QueryRow(ctx, `SELECT id, title, body, url, community, nsfw, status,
		scheduled_at, posted_at, lemmy_post_id, lemmy_post_url, created_at, updated_at
		FROM scheduled_posts WHERE status = 'scheduled' AND scheduled_at <= $1
		ORDER BY scheduled_at, id LIMIT 1 FOR UPDATE SKIP LOCKED`, now.UTC()))
	if errors.Is(err, pgx.ErrNoRows) {
		if err := tx.Commit(ctx); err != nil {
			return scheduledposts.ClaimedPost{}, false, p.uncertain("commit empty scheduled-post claim", err)
		}
		return scheduledposts.ClaimedPost{}, false, nil
	}
	if err != nil {
		return scheduledposts.ClaimedPost{}, false, p.uncertain("select scheduled-post claim", err)
	}
	tag, err := tx.Exec(ctx, `UPDATE scheduled_posts SET status = 'posting', updated_at = $2
		WHERE id = $1 AND status = 'scheduled'`, post.ID, now.UTC())
	if err != nil {
		return scheduledposts.ClaimedPost{}, false, p.uncertain("mark scheduled post posting", err)
	}
	if tag.RowsAffected() != 1 {
		return scheduledposts.ClaimedPost{}, false, p.uncertain("mark scheduled post posting", errors.New("claim row disappeared"))
	}
	post.Status = scheduledposts.StatusPosting
	post.UpdatedAt = now.UTC()
	if err := loadClaimedImageMetadata(ctx, tx, &post); err != nil {
		return scheduledposts.ClaimedPost{}, false, p.uncertain("load claimed post images", err)
	}
	var attemptID int64
	if err := tx.QueryRow(ctx, `INSERT INTO post_attempts (post_id, title_snapshot, started_at)
		VALUES ($1, $2, $3) RETURNING id`, post.ID, post.Title, now.UTC()).Scan(&attemptID); err != nil {
		return scheduledposts.ClaimedPost{}, false, p.uncertain("create posting attempt", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return scheduledposts.ClaimedPost{}, false, p.uncertain("commit scheduled-post claim", err)
	}
	return scheduledposts.ClaimedPost{Post: post, AttemptID: attemptID}, true, nil
}

func loadClaimedImageMetadata(ctx context.Context, tx pgx.Tx, post *scheduledposts.ScheduledPost) error {
	rows, err := tx.Query(ctx, `SELECT i.id, i.content_type, i.created_at,
		COALESCE(p.title_image_id = i.id, false)
		FROM post_images i JOIN scheduled_posts p ON p.id = i.scheduled_post_id
		WHERE i.scheduled_post_id = $1 ORDER BY i.id`, post.ID)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var image scheduledposts.ImageMetadata
		var title bool
		if err := rows.Scan(&image.ID, &image.ContentType, &image.CreatedAt, &title); err != nil {
			return err
		}
		post.Images = append(post.Images, image)
		if title {
			titleImage := image
			post.TitleImage = &titleImage
		}
	}
	return rows.Err()
}

// FinalizeAttempt closes only the matching open attempt while the post is
// still posting. A lost or stale result can never overwrite startup recovery.
func (p *Publisher) FinalizeAttempt(ctx context.Context, result scheduledposts.FinalizeAttempt) error {
	if result.Outcome != scheduledposts.AttemptPosted && result.Outcome != scheduledposts.AttemptNeedsReview {
		return errors.New("db: final outcome must be posted or needs_review")
	}
	if result.PostID <= 0 || result.AttemptID <= 0 || result.FinishedAt.IsZero() {
		return errors.New("db: finalization requires positive post and attempt ids and a finish time")
	}
	if result.Outcome == scheduledposts.AttemptPosted &&
		(result.LemmyPostID <= 0 || !validPostIdentity(result.LemmyPostURL)) {
		return errors.New("db: posted outcome requires a valid Lemmy post identity")
	}
	if result.Outcome == scheduledposts.AttemptNeedsReview && result.LemmyPostURL != "" {
		return errors.New("db: unsuccessful outcome cannot have a Lemmy post URL")
	}
	if err := p.lock(ctx); err != nil {
		return err
	}
	defer p.unlock()

	tx, err := p.conn.Begin(ctx)
	if err != nil {
		return p.uncertain("begin attempt finalization", err)
	}
	defer tx.Rollback(context.Background())

	tag, err := tx.Exec(ctx, `UPDATE post_attempts SET outcome = $3, finished_at = $4, message = $5
		WHERE id = $1 AND post_id = $2 AND outcome = 'started'`,
		result.AttemptID, result.PostID, string(result.Outcome), result.FinishedAt.UTC(), result.Message)
	if err != nil {
		return p.uncertain("close posting attempt", err)
	}
	if tag.RowsAffected() != 1 {
		return ErrStaleAttempt
	}
	var postTag pgconn.CommandTag
	if result.Outcome == scheduledposts.AttemptPosted {
		postTag, err = tx.Exec(ctx, `UPDATE scheduled_posts SET status = 'posted', posted_at = $3,
			lemmy_post_id = $4, lemmy_post_url = $5, updated_at = $3
			WHERE id = $1 AND status = 'posting' AND EXISTS (
				SELECT 1 FROM post_attempts WHERE id = $2 AND outcome = 'posted')`,
			result.PostID, result.AttemptID, result.FinishedAt.UTC(), result.LemmyPostID, result.LemmyPostURL)
	} else {
		postTag, err = tx.Exec(ctx, `UPDATE scheduled_posts SET status = 'needs_review',
			posted_at = NULL, lemmy_post_id = NULL, lemmy_post_url = '', updated_at = $3
			WHERE id = $1 AND status = 'posting' AND EXISTS (
				SELECT 1 FROM post_attempts WHERE id = $2 AND outcome = 'needs_review')`,
			result.PostID, result.AttemptID, result.FinishedAt.UTC())
	}
	if err != nil {
		return p.uncertain("finalize scheduled post", err)
	}
	if postTag.RowsAffected() != 1 {
		return ErrStaleAttempt
	}
	if result.Outcome == scheduledposts.AttemptNeedsReview {
		if _, err := tx.Exec(ctx, `UPDATE lemmy_account SET
			queue_paused_until = GREATEST(queue_paused_until, $1) WHERE id = true`,
			result.FinishedAt.UTC().Add(failurePause)); err != nil {
			return p.uncertain("pause queue after failed attempt", err)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return p.uncertain("commit attempt finalization", err)
	}
	return nil
}

func validPostIdentity(value string) bool {
	u, err := url.Parse(value)
	return err == nil && (u.Scheme == "https" || u.Scheme == "http") &&
		u.Hostname() != "" && u.User == nil && u.Fragment == ""
}

func (p *Publisher) uncertain(operation string, err error) error {
	p.markLost(err)
	return fmt.Errorf("db: %s: %w", operation, err)
}

// Close releases ownership only after callers have stopped work and drained
// requests. A lost session cannot be unlocked; PostgreSQL releases it itself.
func (p *Publisher) Close() error {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-p.guard:
	}
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		p.guard <- struct{}{}
		return nil
	}
	if p.lost != nil {
		p.closed = true
		err := p.lost
		p.mu.Unlock()
		closePhysicalConnection(p.conn)
		p.guard <- struct{}{}
		return fmt.Errorf("%w: %v", ErrPublisherLost, err)
	}
	p.mu.Unlock()
	var unlocked bool
	err := p.conn.QueryRow(ctx, `SELECT pg_advisory_unlock($1)`, publisherLockKey).Scan(&unlocked)
	p.mu.Lock()
	p.closed = true
	p.mu.Unlock()
	p.guard <- struct{}{}
	if err != nil {
		closePhysicalConnection(p.conn)
		p.markLost(err)
		return fmt.Errorf("db: release publisher lock: %w", err)
	}
	p.conn.Release()
	if !unlocked {
		p.markLost(errors.New("publisher lock was not held"))
		return ErrPublisherLost
	}
	return nil
}
