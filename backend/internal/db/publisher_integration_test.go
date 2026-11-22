package db

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/gumert/LemmyScheduler/backend/internal/lemmyaccount"
	"github.com/gumert/LemmyScheduler/backend/internal/scheduledposts"
	"github.com/gumert/LemmyScheduler/backend/migrations"
)

func publisherTestDB(t *testing.T) (context.Context, *pgxpool.Pool, *pgxpool.Pool) {
	t.Helper()
	databaseURL := os.Getenv("TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("TEST_DATABASE_URL is unset")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	t.Cleanup(cancel)
	admin, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		t.Fatalf("connect test database: %v", err)
	}
	t.Cleanup(admin.Close)
	schema := fmt.Sprintf("publisher_test_%d", time.Now().UnixNano())
	if _, err := admin.Exec(ctx, "CREATE SCHEMA "+schema); err != nil {
		t.Fatalf("create test schema: %v", err)
	}
	t.Cleanup(func() {
		if _, err := admin.Exec(context.Background(), "DROP SCHEMA "+schema+" CASCADE"); err != nil {
			t.Errorf("drop test schema: %v", err)
		}
	})
	config, err := pgxpool.ParseConfig(databaseURL)
	if err != nil {
		t.Fatalf("parse test database URL: %v", err)
	}
	config.ConnConfig.RuntimeParams["search_path"] = schema
	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		t.Fatalf("connect test schema: %v", err)
	}
	t.Cleanup(pool.Close)
	for _, name := range []string{
		"0001_initial.up.sql", "0002_scheduled_posts.up.sql", "0003_post_images.up.sql",
		"0004_lemmy_account.up.sql", "0005_scheduler_safety.up.sql",
	} {
		sql, err := migrations.FS.ReadFile(name)
		if err != nil {
			t.Fatalf("read migration %s: %v", name, err)
		}
		if _, err := pool.Exec(ctx, string(sql)); err != nil {
			t.Fatalf("apply migration %s: %v", name, err)
		}
	}
	return ctx, admin, pool
}

func insertSafetyPost(t *testing.T, ctx context.Context, pool *pgxpool.Pool, title string, status scheduledposts.Status, scheduledAt *time.Time, url string) int64 {
	t.Helper()
	var id int64
	err := pool.QueryRow(ctx, `INSERT INTO scheduled_posts (title, body, url, community, status, scheduled_at)
		VALUES ($1, '', $2, 'rust', $3, $4) RETURNING id`, title, url, string(status), scheduledAt).Scan(&id)
	if err != nil {
		t.Fatalf("insert %s post: %v", title, err)
	}
	return id
}

func insertStartedAttempt(t *testing.T, ctx context.Context, pool *pgxpool.Pool, postID int64, title string, started time.Time) int64 {
	t.Helper()
	var id int64
	if err := pool.QueryRow(ctx, `INSERT INTO post_attempts (post_id, title_snapshot, started_at)
		VALUES ($1, $2, $3) RETURNING id`, postID, title, started.UTC()).Scan(&id); err != nil {
		t.Fatalf("insert started attempt: %v", err)
	}
	return id
}

// SAFETY TEST: concurrent due-post claims create at most one attempt per post and skip locked candidates.
func TestPublisherConcurrentClaimsPostgres(t *testing.T) {
	ctx, _, pool := publisherTestDB(t)
	now := time.Now().UTC().Truncate(time.Microsecond)
	var dueIDs []int64
	for i := 0; i < 25; i++ {
		at := now.Add(-time.Duration(i+1) * time.Minute)
		dueIDs = append(dueIDs, insertSafetyPost(t, ctx, pool, fmt.Sprintf("due %d", i), scheduledposts.StatusScheduled, &at, ""))
	}
	future := now.Add(time.Hour)
	insertSafetyPost(t, ctx, pool, "not due", scheduledposts.StatusScheduled, &future, "")

	lockedTx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer lockedTx.Rollback(context.Background())
	var oldestID int64
	if err := lockedTx.QueryRow(ctx, `SELECT id FROM scheduled_posts WHERE status = 'scheduled'
		ORDER BY scheduled_at, id LIMIT 1 FOR UPDATE`).Scan(&oldestID); err != nil {
		t.Fatal(err)
	}
	publisher, err := AcquirePublisher(ctx, pool)
	if err != nil {
		t.Fatal(err)
	}
	defer publisher.Close()

	first, claimed, err := publisher.ClaimDue(ctx, now)
	if err != nil || !claimed || first.Post.ID == oldestID {
		t.Fatalf("claim skipped locked candidate: post=%d claimed=%t err=%v, locked=%d", first.Post.ID, claimed, err, oldestID)
	}
	if err := lockedTx.Commit(ctx); err != nil {
		t.Fatal(err)
	}

	const workers = 8
	type claimResult struct {
		postID int64
		ok     bool
		err    error
	}
	results := make(chan claimResult, workers*4)
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				post, ok, err := publisher.ClaimDue(ctx, now)
				results <- claimResult{postID: post.Post.ID, ok: ok, err: err}
				if err != nil || !ok {
					return
				}
			}
		}()
	}
	wg.Wait()
	close(results)
	claimedIDs := map[int64]bool{first.Post.ID: true}
	for result := range results {
		if result.err != nil {
			t.Fatalf("concurrent claim: %v", result.err)
		}
		if result.ok {
			if claimedIDs[result.postID] {
				t.Fatalf("post %d claimed twice", result.postID)
			}
			claimedIDs[result.postID] = true
		}
	}
	if len(claimedIDs) != len(dueIDs) {
		t.Fatalf("claimed %d of %d due posts", len(claimedIDs), len(dueIDs))
	}
	var attempts int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM post_attempts`).Scan(&attempts); err != nil || attempts != len(dueIDs) {
		t.Fatalf("attempt rows = %d, err=%v; want %d", attempts, err, len(dueIDs))
	}
}

// SAFETY TEST: a claim returns the locked post and image metadata, not an editable pre-claim snapshot.
func TestPublisherClaimCapturesImagesPostgres(t *testing.T) {
	ctx, _, pool := publisherTestDB(t)
	now := time.Now().UTC().Truncate(time.Microsecond)
	postID := insertSafetyPost(t, ctx, pool, "frozen content", scheduledposts.StatusScheduled, &now, "")
	images := NewImageStore(pool)
	title, err := images.PutTitleImage(ctx, postID, "image/png", []byte("title"))
	if err != nil {
		t.Fatal(err)
	}
	body, err := images.CreateBodyImage(ctx, postID, "image/png", []byte("body"))
	if err != nil {
		t.Fatal(err)
	}
	postStore := NewScheduledPostStore(pool)
	post, err := postStore.GetScheduledPost(ctx, postID)
	if err != nil {
		t.Fatal(err)
	}
	post.Body = fmt.Sprintf("image [[image:%d]]", body.ID)
	if _, err := postStore.UpdateScheduledPost(ctx, post); err != nil {
		t.Fatal(err)
	}

	publisher, err := AcquirePublisher(ctx, pool)
	if err != nil {
		t.Fatal(err)
	}
	defer publisher.Close()
	claimed, ok, err := publisher.ClaimDue(ctx, now)
	if err != nil || !ok {
		t.Fatalf("claim: %+v %t %v", claimed, ok, err)
	}
	if claimed.Post.ID != postID || claimed.Post.Status != scheduledposts.StatusPosting ||
		claimed.Post.TitleImage == nil || claimed.Post.TitleImage.ID != title.ID || len(claimed.Post.Images) != 2 {
		t.Fatalf("claimed frozen post = %+v", claimed.Post)
	}
}

// SAFETY TEST: only one process owns publishing, and releasing its session permits a new owner.
func TestPublisherOwnershipExclusivePostgres(t *testing.T) {
	ctx, _, pool := publisherTestDB(t)
	now := time.Now().UTC()
	postID := insertSafetyPost(t, ctx, pool, "still posting", scheduledposts.StatusPosting, nil, "")
	insertStartedAttempt(t, ctx, pool, postID, "still posting", now)
	first, err := AcquirePublisher(ctx, pool)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := AcquirePublisher(ctx, pool); !errors.Is(err, ErrPublisherBusy) {
		t.Fatalf("second owner = %v, want ErrPublisherBusy", err)
	}
	var status string
	if err := pool.QueryRow(ctx, `SELECT status FROM scheduled_posts WHERE id = $1`, postID).Scan(&status); err != nil || status != "posting" {
		t.Fatalf("recovery ran without ownership: status=%q err=%v", status, err)
	}
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}
	second, err := AcquirePublisher(ctx, pool)
	if err != nil {
		t.Fatalf("acquire after clean release: %v", err)
	}
	if err := second.Close(); err != nil {
		t.Fatal(err)
	}
}

// SAFETY TEST: startup recovery changes only posting rows, closes their attempts, and never shortens a pause.
func TestPublisherRecoveryPostgres(t *testing.T) {
	ctx, _, pool := publisherTestDB(t)
	now := time.Now().UTC().Truncate(time.Microsecond)
	reviewID := insertSafetyPost(t, ctx, pool, "posting", scheduledposts.StatusPosting, nil, "")
	attemptID := insertStartedAttempt(t, ctx, pool, reviewID, "title snapshot", now.Add(-time.Minute))
	scheduledAt := now.Add(time.Hour)
	scheduledID := insertSafetyPost(t, ctx, pool, "scheduled", scheduledposts.StatusScheduled, &scheduledAt, "")
	draftID := insertSafetyPost(t, ctx, pool, "draft", scheduledposts.StatusDraft, nil, "")
	accountStore := NewLemmyAccountStore(pool)
	if err := accountStore.Save(ctx, lemmyaccount.Account{
		InstanceURL: "https://lemmy.test", Username: "u", Password: "secret",
	}, lemmyaccount.LoginResult{OK: true, At: now}); err != nil {
		t.Fatal(err)
	}
	if err := accountStore.SetQueuePausedUntil(ctx, now.Add(30*time.Minute)); err != nil {
		t.Fatal(err)
	}
	publisher, err := AcquirePublisher(ctx, pool)
	if err != nil {
		t.Fatal(err)
	}
	defer publisher.Close()
	recovered, err := publisher.RecoverInterrupted(ctx, now)
	if err != nil || len(recovered) != 1 || recovered[0] != reviewID {
		t.Fatalf("recovered ids=%v err=%v", recovered, err)
	}
	var status, outcome, message string
	var finished time.Time
	if err := pool.QueryRow(ctx, `SELECT p.status, a.outcome, a.finished_at, a.message
		FROM scheduled_posts p JOIN post_attempts a ON a.post_id = p.id WHERE a.id = $1`, attemptID).
		Scan(&status, &outcome, &finished, &message); err != nil {
		t.Fatal(err)
	}
	if status != "needs_review" || outcome != "recovered_on_startup" || !finished.Equal(now) || message == "" {
		t.Fatalf("recovered post/attempt = %s/%s at %s message=%q", status, outcome, finished, message)
	}
	for _, id := range []int64{scheduledID, draftID} {
		var got string
		if err := pool.QueryRow(ctx, `SELECT status FROM scheduled_posts WHERE id = $1`, id).Scan(&got); err != nil || got == "needs_review" {
			t.Fatalf("non-posting post %d changed to %q, err=%v", id, got, err)
		}
	}
	account, err := accountStore.Get(ctx)
	if err != nil || account.QueuePausedUntil == nil || !account.QueuePausedUntil.Equal(now.Add(time.Hour)) {
		t.Fatalf("failure pause = %v, err=%v", account.QueuePausedUntil, err)
	}
	if _, err := pool.Exec(ctx, `UPDATE lemmy_account SET queue_paused_until = $1`, now.Add(2*time.Hour)); err != nil {
		t.Fatal(err)
	}
	nextID := insertSafetyPost(t, ctx, pool, "posting later", scheduledposts.StatusPosting, nil, "")
	insertStartedAttempt(t, ctx, pool, nextID, "posting later", now)
	if _, err := publisher.RecoverInterrupted(ctx, now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	account, err = accountStore.Get(ctx)
	if err != nil || account.QueuePausedUntil == nil || !account.QueuePausedUntil.Equal(now.Add(2*time.Hour)) {
		t.Fatalf("recovery shortened later pause: %v, err=%v", account.QueuePausedUntil, err)
	}
}

// SAFETY TEST: attempt finalization is atomic, requires a usable post identity, and stale results cannot undo recovery.
func TestPublisherFinalizationPostgres(t *testing.T) {
	ctx, _, pool := publisherTestDB(t)
	now := time.Now().UTC().Truncate(time.Microsecond)
	accountStore := NewLemmyAccountStore(pool)
	if err := accountStore.Save(ctx, lemmyaccount.Account{
		InstanceURL: "https://lemmy.test", Username: "u", Password: "secret",
	}, lemmyaccount.LoginResult{OK: true, At: now}); err != nil {
		t.Fatal(err)
	}
	publisher, err := AcquirePublisher(ctx, pool)
	if err != nil {
		t.Fatal(err)
	}
	defer publisher.Close()

	failureTime := now.Add(time.Minute)
	failedID := insertSafetyPost(t, ctx, pool, "failed", scheduledposts.StatusScheduled, &now, "")
	failed, ok, err := publisher.ClaimDue(ctx, now)
	if err != nil || !ok || failed.Post.ID != failedID {
		t.Fatalf("claim failure case: %+v %t %v", failed, ok, err)
	}
	if err := publisher.FinalizeAttempt(ctx, scheduledposts.FinalizeAttempt{
		PostID: failedID, AttemptID: failed.AttemptID, Outcome: scheduledposts.AttemptPosted,
		FinishedAt: failureTime, LemmyPostURL: "https://lemmy.test/post/41",
	}); err == nil {
		t.Fatal("posted outcome without a positive Lemmy post id was accepted")
	}
	if err := publisher.FinalizeAttempt(ctx, scheduledposts.FinalizeAttempt{
		PostID: failedID, AttemptID: failed.AttemptID, Outcome: scheduledposts.AttemptNeedsReview,
		Message: "The result is uncertain.", FinishedAt: failureTime,
	}); err != nil {
		t.Fatal(err)
	}
	var postStatus, outcome string
	var finished, paused time.Time
	if err := pool.QueryRow(ctx, `SELECT p.status, a.outcome, a.finished_at, l.queue_paused_until
		FROM scheduled_posts p JOIN post_attempts a ON a.post_id = p.id CROSS JOIN lemmy_account l
		WHERE p.id = $1`, failedID).Scan(&postStatus, &outcome, &finished, &paused); err != nil {
		t.Fatal(err)
	}
	if postStatus != "needs_review" || outcome != "needs_review" ||
		!finished.Equal(failureTime) || !paused.Equal(failureTime.Add(time.Hour)) {
		t.Fatalf("failed finalization = %s/%s at %s, paused=%s", postStatus, outcome, finished, paused)
	}

	if err := accountStore.SetQueuePausedUntil(ctx, now.Add(3*time.Hour)); err != nil {
		t.Fatal(err)
	}
	longPauseID := insertSafetyPost(t, ctx, pool, "long pause", scheduledposts.StatusScheduled, &now, "")
	longPause, ok, err := publisher.ClaimDue(ctx, now)
	if err != nil || !ok || longPause.Post.ID != longPauseID {
		t.Fatalf("claim long-pause case: %+v %t %v", longPause, ok, err)
	}
	if err := publisher.FinalizeAttempt(ctx, scheduledposts.FinalizeAttempt{
		PostID: longPauseID, AttemptID: longPause.AttemptID, Outcome: scheduledposts.AttemptNeedsReview,
		FinishedAt: now.Add(2 * time.Minute),
	}); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT queue_paused_until FROM lemmy_account`).Scan(&paused); err != nil || !paused.Equal(now.Add(3*time.Hour)) {
		t.Fatalf("failed attempt shortened later pause: %s err=%v", paused, err)
	}

	successID := insertSafetyPost(t, ctx, pool, "posted", scheduledposts.StatusScheduled, &now, "")
	success, ok, err := publisher.ClaimDue(ctx, now)
	if err != nil || !ok || success.Post.ID != successID {
		t.Fatalf("claim success case: %+v %t %v", success, ok, err)
	}
	successTime := now.Add(2 * time.Minute)
	if err := publisher.FinalizeAttempt(ctx, scheduledposts.FinalizeAttempt{
		PostID: successID, AttemptID: success.AttemptID, Outcome: scheduledposts.AttemptPosted,
		FinishedAt: successTime, LemmyPostID: 42, LemmyPostURL: "https://lemmy.test/post/42",
	}); err != nil {
		t.Fatal(err)
	}
	var postedID int64
	var postedURL string
	if err := pool.QueryRow(ctx, `SELECT status, lemmy_post_id, lemmy_post_url
		FROM scheduled_posts WHERE id = $1`, successID).Scan(&postStatus, &postedID, &postedURL); err != nil ||
		postStatus != "posted" || postedID != 42 || postedURL != "https://lemmy.test/post/42" {
		t.Fatalf("posted identity = %s/%d/%q err=%v", postStatus, postedID, postedURL, err)
	}

	staleID := insertSafetyPost(t, ctx, pool, "stale", scheduledposts.StatusScheduled, &now, "")
	stale, ok, err := publisher.ClaimDue(ctx, now)
	if err != nil || !ok || stale.Post.ID != staleID {
		t.Fatalf("claim stale case: %+v %t %v", stale, ok, err)
	}
	if _, err := publisher.RecoverInterrupted(ctx, successTime); err != nil {
		t.Fatal(err)
	}
	if err := publisher.FinalizeAttempt(ctx, scheduledposts.FinalizeAttempt{
		PostID: staleID, AttemptID: stale.AttemptID, Outcome: scheduledposts.AttemptPosted,
		FinishedAt: successTime, LemmyPostID: 43, LemmyPostURL: "https://lemmy.test/post/43",
	}); !errors.Is(err, ErrStaleAttempt) {
		t.Fatalf("stale finalization = %v, want ErrStaleAttempt", err)
	}
	var staleStatus string
	if err := pool.QueryRow(ctx, `SELECT status FROM scheduled_posts WHERE id = $1`, staleID).Scan(&staleStatus); err != nil || staleStatus != "needs_review" {
		t.Fatalf("stale finalization status=%q err=%v", staleStatus, err)
	}

	if _, err := pool.Exec(ctx, `CREATE FUNCTION reject_queue_pause() RETURNS trigger LANGUAGE plpgsql AS $$
		BEGIN
			IF NEW.queue_paused_until IS DISTINCT FROM OLD.queue_paused_until THEN
				RAISE EXCEPTION 'injected queue-pause failure';
			END IF;
			RETURN NEW;
		END $$`); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `CREATE TRIGGER reject_queue_pause BEFORE UPDATE OF queue_paused_until ON lemmy_account
		FOR EACH ROW EXECUTE FUNCTION reject_queue_pause()`); err != nil {
		t.Fatal(err)
	}
	atomicID := insertSafetyPost(t, ctx, pool, "atomic failure", scheduledposts.StatusScheduled, &now, "")
	atomic, ok, err := publisher.ClaimDue(ctx, now)
	if err != nil || !ok || atomic.Post.ID != atomicID {
		t.Fatalf("claim atomic failure case: %+v %t %v", atomic, ok, err)
	}
	if err := publisher.FinalizeAttempt(ctx, scheduledposts.FinalizeAttempt{
		PostID: atomicID, AttemptID: atomic.AttemptID, Outcome: scheduledposts.AttemptNeedsReview,
		FinishedAt: now.Add(5 * time.Hour),
	}); err == nil {
		t.Fatal("injected pause failure did not fail finalization")
	}
	var atomicPostStatus, atomicOutcome string
	if err := pool.QueryRow(ctx, `SELECT p.status, a.outcome FROM scheduled_posts p
		JOIN post_attempts a ON a.post_id = p.id WHERE p.id = $1`, atomicID).
		Scan(&atomicPostStatus, &atomicOutcome); err != nil {
		t.Fatal(err)
	}
	if atomicPostStatus != "posting" || atomicOutcome != "started" {
		t.Fatalf("partial finalization persisted: post=%s attempt=%s", atomicPostStatus, atomicOutcome)
	}
}

// SAFETY TEST: session loss terminates the old owner, allows recovery by a new owner, and fences old finalization.
func TestPublisherSessionLossPostgres(t *testing.T) {
	ctx, admin, pool := publisherTestDB(t)
	now := time.Now().UTC().Truncate(time.Microsecond)
	postID := insertSafetyPost(t, ctx, pool, "in flight", scheduledposts.StatusScheduled, &now, "")
	oldOwner, err := AcquirePublisher(ctx, pool)
	if err != nil {
		t.Fatal(err)
	}
	claimed, ok, err := oldOwner.ClaimDue(ctx, now)
	if err != nil || !ok || claimed.Post.ID != postID {
		t.Fatalf("claim: %+v %t %v", claimed, ok, err)
	}
	remoteStarted, finishRemote := make(chan struct{}), make(chan struct{})
	go func() {
		close(remoteStarted)
		<-finishRemote
	}()
	<-remoteStarted

	pid := oldOwner.conn.Conn().PgConn().PID()
	var terminated bool
	if err := admin.QueryRow(ctx, `SELECT pg_terminate_backend($1)`, pid).Scan(&terminated); err != nil || !terminated {
		t.Fatalf("terminate publisher backend: terminated=%t err=%v", terminated, err)
	}
	if err := oldOwner.Check(ctx); !errors.Is(err, ErrPublisherLost) {
		t.Fatalf("old owner check = %v, want ownership loss", err)
	}
	select {
	case <-oldOwner.Lost():
	case <-ctx.Done():
		t.Fatal("ownership loss was not latched")
	}
	newOwner, err := AcquirePublisher(ctx, pool)
	if err != nil {
		t.Fatalf("new owner could not acquire after session termination: %v", err)
	}
	if _, err := newOwner.RecoverInterrupted(ctx, now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	close(finishRemote)
	err = oldOwner.FinalizeAttempt(ctx, scheduledposts.FinalizeAttempt{
		PostID: postID, AttemptID: claimed.AttemptID, Outcome: scheduledposts.AttemptPosted,
		FinishedAt: now.Add(2 * time.Minute), LemmyPostID: 44, LemmyPostURL: "https://lemmy.test/post/44",
	})
	if !errors.Is(err, ErrPublisherLost) {
		t.Fatalf("old finalization = %v, want ownership loss", err)
	}
	var status string
	if err := pool.QueryRow(ctx, `SELECT status FROM scheduled_posts WHERE id = $1`, postID).Scan(&status); err != nil || status != "needs_review" {
		t.Fatalf("recovered post status=%q err=%v", status, err)
	}
	if err := newOwner.Close(); err != nil {
		t.Fatal(err)
	}
	if err := oldOwner.Close(); err == nil {
		t.Fatal("closing a lost owner should report ownership loss")
	}
}

// SAFETY TEST: attempt rows enforce one open attempt and consistent outcome/finish-time pairs.
func TestPostAttemptConstraintsPostgres(t *testing.T) {
	ctx, _, pool := publisherTestDB(t)
	now := time.Now().UTC()
	postID := insertSafetyPost(t, ctx, pool, "attempt constraints", scheduledposts.StatusPosting, nil, "")
	insertStartedAttempt(t, ctx, pool, postID, "title", now)
	_, err := pool.Exec(ctx, `INSERT INTO post_attempts (post_id, title_snapshot, started_at)
		VALUES ($1, 'duplicate', $2)`, postID, now)
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.Code != "23505" {
		t.Fatalf("second open attempt error = %v, want unique violation", err)
	}
	_, err = pool.Exec(ctx, `INSERT INTO post_attempts (post_id, title_snapshot, started_at, finished_at, outcome)
		VALUES ($1, 'inconsistent', $2, $2, 'started')`, postID, now)
	if !errors.As(err, &pgErr) || pgErr.Code != "23514" {
		t.Fatalf("inconsistent attempt error = %v, want check violation", err)
	}
}

// SAFETY TEST: user writes cannot change scheduler-owned rows, and deleting terminal posts preserves attempts.
func TestSchedulerOwnedStatusGuardsPostgres(t *testing.T) {
	ctx, _, pool := publisherTestDB(t)
	store := NewScheduledPostStore(pool)
	now := time.Now().UTC().Truncate(time.Microsecond)
	for _, status := range []scheduledposts.Status{
		scheduledposts.StatusPosting, scheduledposts.StatusPosted,
		scheduledposts.StatusNeedsReview, scheduledposts.StatusFailed,
	} {
		id := insertSafetyPost(t, ctx, pool, string(status), status, &now, "")
		attemptID := insertStartedAttempt(t, ctx, pool, id, string(status), now)
		if status != scheduledposts.StatusPosting {
			outcome := string(scheduledposts.AttemptNeedsReview)
			if status == scheduledposts.StatusPosted {
				outcome = string(scheduledposts.AttemptPosted)
			}
			if _, err := pool.Exec(ctx, `UPDATE post_attempts SET outcome = $2, finished_at = $3 WHERE id = $1`, attemptID, outcome, now); err != nil {
				t.Fatal(err)
			}
		}
		post, err := store.GetScheduledPost(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		post.Title = "mutated"
		if _, err := store.UpdateScheduledPost(ctx, post); !errors.Is(err, scheduledposts.ErrNotEditable) {
			t.Errorf("update %s post = %v, want ErrNotEditable", status, err)
		}
		for _, action := range []scheduledposts.BulkAction{
			scheduledposts.BulkPause, scheduledposts.BulkResume, scheduledposts.BulkDelay,
		} {
			request := scheduledposts.BulkRequest{IDs: []int64{id}, Action: action}
			if action == scheduledposts.BulkDelay {
				request.Delay = time.Minute
			}
			result, err := store.BulkUpdate(ctx, request)
			if err != nil || len(result.UpdatedIDs) != 0 {
				t.Errorf("bulk %s on %s = %+v, %v", action, status, result, err)
			}
		}
		if status == scheduledposts.StatusPosting {
			if err := store.DeleteScheduledPost(ctx, id); !errors.Is(err, scheduledposts.ErrNotDeletable) {
				t.Errorf("delete posting = %v, want ErrNotDeletable", err)
			}
			result, err := store.BulkUpdate(ctx, scheduledposts.BulkRequest{IDs: []int64{id}, Action: scheduledposts.BulkDelete})
			if err != nil || len(result.UpdatedIDs) != 0 {
				t.Errorf("bulk delete posting = %+v, %v", result, err)
			}
			continue
		}
		if status == scheduledposts.StatusNeedsReview {
			result, err := store.BulkUpdate(ctx, scheduledposts.BulkRequest{
				IDs: []int64{id}, Action: scheduledposts.BulkDelete,
			})
			if err != nil || len(result.UpdatedIDs) != 1 {
				t.Fatalf("bulk delete terminal post: %+v, %v", result, err)
			}
		} else if err := store.DeleteScheduledPost(ctx, id); err != nil {
			t.Fatalf("delete terminal %s post: %v", status, err)
		}
		var attemptCount int
		if err := pool.QueryRow(ctx, `SELECT count(*) FROM post_attempts WHERE id = $1`, attemptID).Scan(&attemptCount); err != nil || attemptCount != 1 {
			t.Fatalf("attempt for deleted %s post count=%d err=%v", status, attemptCount, err)
		}
	}
}

// SAFETY TEST: concurrent URL and title-image writes can never leave both values set.
func TestURLAndTitleImageExclusivePostgres(t *testing.T) {
	ctx, _, pool := publisherTestDB(t)
	store := NewScheduledPostStore(pool)
	images := NewImageStore(pool)
	post, err := store.CreateScheduledPost(ctx, scheduledposts.ScheduledPost{
		Title: "exclusive inputs", Community: "rust", Status: scheduledposts.StatusDraft,
	})
	if err != nil {
		t.Fatal(err)
	}
	start := make(chan struct{})
	results := make(chan error, 2)
	go func() {
		<-start
		_, err := store.UpdateScheduledPost(ctx, scheduledposts.ScheduledPost{
			ID: post.ID, Title: post.Title, Community: post.Community, Status: post.Status,
			URL: "https://example.test/article",
		})
		results <- err
	}()
	go func() {
		<-start
		_, err := images.PutTitleImage(ctx, post.ID, "image/png", []byte("image"))
		results <- err
	}()
	close(start)
	errA, errB := <-results, <-results
	successes := 0
	for _, err := range []error{errA, errB} {
		if err == nil {
			successes++
			continue
		}
		var validationErr *scheduledposts.ValidationError
		if !errors.As(err, &validationErr) {
			t.Fatalf("concurrent conflicting write = %v, want field validation error", err)
		}
	}
	if successes != 1 {
		t.Fatalf("successful writes = %d, want exactly one (errors: %v, %v)", successes, errA, errB)
	}
	var url string
	var titleImageID *int64
	if err := pool.QueryRow(ctx, `SELECT url, title_image_id FROM scheduled_posts WHERE id = $1`, post.ID).
		Scan(&url, &titleImageID); err != nil {
		t.Fatal(err)
	}
	if url != "" && titleImageID != nil {
		t.Fatalf("conflicting values persisted: url=%q title_image_id=%d", url, *titleImageID)
	}

	postID := insertSafetyPost(t, ctx, pool, "url image selection", scheduledposts.StatusDraft, nil, "https://example.test")
	var validationErr *scheduledposts.ValidationError
	if _, err := images.PutTitleImage(ctx, postID, "image/png", []byte("title")); !errors.As(err, &validationErr) || validationErr.Field != "title_image" {
		t.Fatalf("upload title image with URL = %v, want title_image field error", err)
	}
	image, err := images.CreateBodyImage(ctx, postID, "image/png", []byte("image"))
	if err != nil {
		t.Fatal(err)
	}
	if err := images.SelectTitleImage(ctx, postID, image.ID); !errors.As(err, &validationErr) || validationErr.Field != "title_image" {
		t.Fatalf("select title image with URL = %v, want title_image field error", err)
	}
}

// SAFETY TEST: a recovery with no configured account still marks uncertain posts for review.
func TestPublisherRecoveryWithoutAccountPostgres(t *testing.T) {
	ctx, _, pool := publisherTestDB(t)
	now := time.Now().UTC()
	postID := insertSafetyPost(t, ctx, pool, "no account", scheduledposts.StatusPosting, nil, "")
	insertStartedAttempt(t, ctx, pool, postID, "no account", now)
	publisher, err := AcquirePublisher(ctx, pool)
	if err != nil {
		t.Fatal(err)
	}
	defer publisher.Close()
	if _, err := publisher.RecoverInterrupted(ctx, now); err != nil {
		t.Fatal(err)
	}
	if _, err := NewLemmyAccountStore(pool).Get(ctx); !errors.Is(err, lemmyaccount.ErrNotConfigured) {
		t.Fatalf("recovery configured an account: %v", err)
	}
}
