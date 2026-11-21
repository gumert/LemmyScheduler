package db

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/gumert/LemmyScheduler/backend/internal/scheduledposts"
	"github.com/gumert/LemmyScheduler/backend/migrations"
)

func TestScheduledPostStorePostgres(t *testing.T) {
	databaseURL := os.Getenv("TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("TEST_DATABASE_URL is unset")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	adminPool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		t.Fatalf("connect test database: %v", err)
	}
	defer adminPool.Close()

	var random [8]byte
	if _, err := rand.Read(random[:]); err != nil {
		t.Fatalf("generate schema suffix: %v", err)
	}
	schema := "scheduled_posts_test_" + hex.EncodeToString(random[:])
	if _, err := adminPool.Exec(ctx, "CREATE SCHEMA "+schema); err != nil {
		t.Fatalf("create test schema: %v", err)
	}
	defer func() {
		if _, err := adminPool.Exec(context.Background(), "DROP SCHEMA "+schema+" CASCADE"); err != nil {
			t.Errorf("drop test schema: %v", err)
		}
	}()

	config, err := pgxpool.ParseConfig(databaseURL)
	if err != nil {
		t.Fatalf("parse test database URL: %v", err)
	}
	config.ConnConfig.RuntimeParams["search_path"] = schema
	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		t.Fatalf("connect test schema: %v", err)
	}
	defer pool.Close()

	for _, name := range []string{"0001_initial.up.sql", "0002_scheduled_posts.up.sql", "0003_post_images.up.sql"} {
		migration, err := migrations.FS.ReadFile(name)
		if err != nil {
			t.Fatalf("read migration %s: %v", name, err)
		}
		if _, err := pool.Exec(ctx, string(migration)); err != nil {
			t.Fatalf("apply migration %s: %v", name, err)
		}
	}

	store := NewScheduledPostStore(pool)
	future := time.Now().UTC().Add(time.Hour).Truncate(time.Microsecond)
	var scheduled []scheduledposts.ScheduledPost
	for i := 0; i < 3; i++ {
		post, err := store.CreateScheduledPost(ctx, scheduledposts.ScheduledPost{
			Title: fmt.Sprintf("scheduled %d", i), Community: "rust", Status: scheduledposts.StatusScheduled,
			ScheduledAt: &future,
		})
		if err != nil {
			t.Fatalf("create scheduled post %d: %v", i, err)
		}
		scheduled = append(scheduled, post)
	}

	firstPage, err := store.ListScheduledPosts(ctx, scheduledposts.ListOptions{
		Statuses: []scheduledposts.Status{scheduledposts.StatusScheduled}, Limit: 2,
	})
	if err != nil {
		t.Fatalf("list first page: %v", err)
	}
	if len(firstPage) != 2 || firstPage[0].ID != scheduled[0].ID || firstPage[1].ID != scheduled[1].ID {
		t.Fatalf("first page = %+v, want the first two equal-sort-key posts", firstPage)
	}
	nextPage, err := store.ListScheduledPosts(ctx, scheduledposts.ListOptions{
		Statuses: []scheduledposts.Status{scheduledposts.StatusScheduled}, Limit: 2,
		After: &scheduledposts.Cursor{SortTime: scheduledposts.SortTime(firstPage[1]), ID: firstPage[1].ID},
	})
	if err != nil {
		t.Fatalf("list next page: %v", err)
	}
	if len(nextPage) != 1 || nextPage[0].ID != scheduled[2].ID {
		t.Fatalf("next page = %+v, want the third equal-sort-key post", nextPage)
	}

	draft, err := store.CreateScheduledPost(ctx, scheduledposts.ScheduledPost{
		Title: "draft", Community: "rust", Status: scheduledposts.StatusDraft,
	})
	if err != nil {
		t.Fatalf("create draft: %v", err)
	}
	draft.Title = "updated"
	updated, err := store.UpdateScheduledPost(ctx, draft)
	if err != nil {
		t.Fatalf("update draft: %v", err)
	}
	if updated.Title != "updated" {
		t.Fatalf("updated title = %q, want %q", updated.Title, "updated")
	}

	statusFiltered, err := store.ListScheduledPosts(ctx, scheduledposts.ListOptions{
		Statuses: []scheduledposts.Status{scheduledposts.StatusDraft}, Limit: 10,
	})
	if err != nil {
		t.Fatalf("list by status: %v", err)
	}
	if len(statusFiltered) != 1 || statusFiltered[0].ID != draft.ID {
		t.Fatalf("status-filtered list = %+v, want only draft %d", statusFiltered, draft.ID)
	}
	if _, err := pool.Exec(ctx, `UPDATE scheduled_posts SET status = 'posting' WHERE id = $1`, draft.ID); err != nil {
		t.Fatalf("simulate scheduler claim: %v", err)
	}
	if _, err := store.UpdateScheduledPost(ctx, draft); !errors.Is(err, scheduledposts.ErrNotEditable) {
		t.Fatalf("update after scheduler claim = %v, want not editable", err)
	}

	for name, query := range map[string]string{
		"unknown status":         `INSERT INTO scheduled_posts (title, community, status) VALUES ('bad', 'rust', 'unknown')`,
		"scheduled without time": `INSERT INTO scheduled_posts (title, community, status) VALUES ('bad', 'rust', 'scheduled')`,
	} {
		_, err := pool.Exec(ctx, query)
		var pgErr *pgconn.PgError
		if !errors.As(err, &pgErr) || pgErr.Code != "23514" {
			t.Errorf("%s constraint error = %v, want check-constraint violation", name, err)
		}
	}

	t.Run("images", func(t *testing.T) {
		images := NewImageStore(pool)
		post, err := store.CreateScheduledPost(ctx, scheduledposts.ScheduledPost{
			Title: "images", Community: "rust", Status: scheduledposts.StatusDraft,
		})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := pool.Exec(ctx, `UPDATE scheduled_posts SET updated_at = '2000-01-01 UTC' WHERE id = $1`, post.ID); err != nil {
			t.Fatal(err)
		}
		title, err := images.PutTitleImage(ctx, post.ID, "image/png", []byte("first"))
		if err != nil {
			t.Fatal(err)
		}
		afterUpload, err := store.GetScheduledPost(ctx, post.ID)
		if err != nil || afterUpload.UpdatedAt.Year() <= 2000 {
			t.Fatalf("image upload did not update post timestamp: %+v, %v", afterUpload, err)
		}
		replacement, err := images.PutTitleImage(ctx, post.ID, "image/png", []byte("replacement"))
		if err != nil || replacement.ID == title.ID {
			t.Fatalf("replace title = %+v, %v", replacement, err)
		}
		got, err := images.GetImage(ctx, post.ID, 0, true)
		if err != nil || string(got.Data) != "replacement" {
			t.Fatalf("get title = %+v, %v", got, err)
		}
		one, err := images.CreateBodyImage(ctx, post.ID, "image/png", []byte("body"))
		if err != nil {
			t.Fatal(err)
		}
		post.Body = fmt.Sprintf("before [[image:%d]] after", one.ID)
		if _, err := store.UpdateScheduledPost(ctx, post); err != nil {
			t.Fatal(err)
		}
		if err := images.DeleteImage(ctx, post.ID, one.ID, false); !errors.Is(err, scheduledposts.ErrImageInUse) {
			t.Fatalf("delete referenced image = %v", err)
		}
		if _, err := images.GetImage(ctx, post.ID, one.ID, false); err != nil {
			t.Fatalf("referenced image was deleted: %v", err)
		}
		post.Body = ""
		if _, err := store.UpdateScheduledPost(ctx, post); err != nil {
			t.Fatal(err)
		}
		if _, err := images.GetImage(ctx, post.ID, one.ID, false); !errors.Is(err, scheduledposts.ErrImageNotFound) {
			t.Fatalf("unreferenced image survived body edit: %v", err)
		}
		two, err := images.CreateBodyImage(ctx, post.ID, "image/png", []byte("two"))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := pool.Exec(ctx, `UPDATE scheduled_posts SET updated_at = '2000-01-01 UTC' WHERE id = $1`, post.ID); err != nil {
			t.Fatal(err)
		}
		if err := images.DeleteImage(ctx, post.ID, two.ID, false); err != nil {
			t.Fatal(err)
		}
		if _, err := pool.Exec(ctx, `INSERT INTO post_images (scheduled_post_id, content_type, size_bytes, data)
			VALUES ($1, 'image/png', 1, 'oversize'::bytea)`, post.ID); err == nil {
			t.Fatal("mismatched image size was accepted")
		}
		afterDelete, err := store.GetScheduledPost(ctx, post.ID)
		if err != nil || afterDelete.UpdatedAt.Year() <= 2000 {
			t.Fatalf("image delete did not update post timestamp: %+v, %v", afterDelete, err)
		}
		if _, err := images.GetImage(ctx, post.ID, two.ID, false); !errors.Is(err, scheduledposts.ErrImageNotFound) {
			t.Fatalf("get deleted image = %v", err)
		}
		results := make(chan error, scheduledposts.MaxPostImages+1)
		for i := 0; i < cap(results); i++ {
			go func() {
				_, err := images.CreateBodyImage(ctx, post.ID, "image/png", []byte("body"))
				results <- err
			}()
		}
		var successes, limits int
		for i := 0; i < cap(results); i++ {
			err := <-results
			switch {
			case err == nil:
				successes++
			case errors.Is(err, scheduledposts.ErrImageLimit):
				limits++
			default:
				t.Fatalf("concurrent upload: %v", err)
			}
		}
		if successes != scheduledposts.MaxPostImages-1 || limits != 2 {
			t.Fatalf("concurrent uploads = %d successes, %d limits", successes, limits)
		}
		// Set directly: a body edit through the store would delete the unreferenced images.
		if _, err := pool.Exec(ctx, `UPDATE scheduled_posts SET body = $2 WHERE id = $1`, post.ID, fmt.Sprintf("[[image:%d]]", replacement.ID)); err != nil {
			t.Fatal(err)
		}
		if _, err := images.PutTitleImage(ctx, post.ID, "image/png", []byte("new title")); !errors.Is(err, scheduledposts.ErrImageLimit) {
			t.Fatalf("replace referenced title at image limit = %v", err)
		}
		unchanged, err := images.GetImage(ctx, post.ID, 0, true)
		if err != nil || unchanged.ID != replacement.ID {
			t.Fatalf("failed replacement lost title image: %+v, %v", unchanged, err)
		}
		loaded, err := store.GetScheduledPost(ctx, post.ID)
		if err != nil || loaded.TitleImage == nil || loaded.TitleImage.ID != replacement.ID || len(loaded.Images) != scheduledposts.MaxPostImages {
			t.Fatalf("get image metadata = %+v, %v", loaded, err)
		}
		list, err := store.ListScheduledPosts(ctx, scheduledposts.ListOptions{Limit: 20})
		if err != nil {
			t.Fatal(err)
		}
		found := false
		for _, item := range list {
			if item.ID == post.ID {
				found = item.TitleImage != nil && len(item.Images) == scheduledposts.MaxPostImages
			}
		}
		if !found {
			t.Fatal("list missing image metadata")
		}
		if err := store.DeleteScheduledPost(ctx, post.ID); err != nil {
			t.Fatal(err)
		}
		var count int
		if err := pool.QueryRow(ctx, `SELECT count(*) FROM post_images WHERE scheduled_post_id = $1`, post.ID).Scan(&count); err != nil || count != 0 {
			t.Fatalf("images remaining after post deletion = %d, %v", count, err)
		}
		if _, err := images.CreateBodyImage(ctx, post.ID, "image/png", []byte("body")); !errors.Is(err, scheduledposts.ErrNotFound) {
			t.Fatalf("upload after post deletion = %v", err)
		}
		if _, err := images.PutTitleImage(ctx, draft.ID, "image/png", []byte("body")); !errors.Is(err, scheduledposts.ErrNotEditable) {
			t.Fatalf("upload to posting post = %v", err)
		}
	})

	t.Run("body edit reconciles images", func(t *testing.T) {
		images := NewImageStore(pool)
		post, err := store.CreateScheduledPost(ctx, scheduledposts.ScheduledPost{
			Title: "reconcile", Community: "rust", Status: scheduledposts.StatusDraft,
		})
		if err != nil {
			t.Fatal(err)
		}
		title, err := images.PutTitleImage(ctx, post.ID, "image/png", []byte("title"))
		if err != nil {
			t.Fatal(err)
		}
		a, err := images.CreateBodyImage(ctx, post.ID, "image/png", []byte("a"))
		if err != nil {
			t.Fatal(err)
		}
		b, err := images.CreateBodyImage(ctx, post.ID, "image/png", []byte("b"))
		if err != nil {
			t.Fatal(err)
		}
		other, err := store.CreateScheduledPost(ctx, scheduledposts.ScheduledPost{
			Title: "other", Community: "rust", Status: scheduledposts.StatusDraft,
		})
		if err != nil {
			t.Fatal(err)
		}
		foreign, err := images.CreateBodyImage(ctx, other.ID, "image/png", []byte("foreign"))
		if err != nil {
			t.Fatal(err)
		}
		var validationErr *scheduledposts.ValidationError
		for _, id := range []int64{foreign.ID, a.ID + 100000} {
			post.Body = fmt.Sprintf("[[image:%d]] [[image:%d]]", a.ID, id)
			if _, err := store.UpdateScheduledPost(ctx, post); !errors.As(err, &validationErr) || validationErr.Field != "body" {
				t.Fatalf("body referencing image %d = %v", id, err)
			}
		}
		if got, err := store.GetScheduledPost(ctx, post.ID); err != nil || len(got.Images) != 3 || got.Body != "" {
			t.Fatalf("rejected update changed the post: %+v, %v", got, err)
		}
		post.Body = fmt.Sprintf("[[image:%d]] [[image:%d]]", a.ID, b.ID)
		if updated, err := store.UpdateScheduledPost(ctx, post); err != nil || len(updated.Images) != 3 {
			t.Fatalf("keep referenced images = %+v, %v", updated, err)
		}
		post.Body = fmt.Sprintf("[[image:%d]]", a.ID)
		updated, err := store.UpdateScheduledPost(ctx, post)
		if err != nil || len(updated.Images) != 2 || updated.TitleImage == nil || updated.TitleImage.ID != title.ID {
			t.Fatalf("drop unreferenced image, keep title = %+v, %v", updated, err)
		}
		if _, err := images.GetImage(ctx, post.ID, b.ID, false); !errors.Is(err, scheduledposts.ErrImageNotFound) {
			t.Fatalf("unreferenced image survived: %v", err)
		}
	})

	t.Run("shared title image", func(t *testing.T) {
		images := NewImageStore(pool)
		post, err := store.CreateScheduledPost(ctx, scheduledposts.ScheduledPost{
			Title: "shared", Community: "rust", Status: scheduledposts.StatusDraft,
		})
		if err != nil {
			t.Fatal(err)
		}
		image, err := images.CreateBodyImage(ctx, post.ID, "image/png", []byte("shared"))
		if err != nil {
			t.Fatal(err)
		}
		if err := images.SelectTitleImage(ctx, post.ID, image.ID); err != nil {
			t.Fatal(err)
		}
		if err := images.DeleteImage(ctx, post.ID, image.ID, false); !errors.Is(err, scheduledposts.ErrImageInUse) {
			t.Fatalf("delete title image through image endpoint = %v", err)
		}
		post.Body = fmt.Sprintf("before [[image:%d]] after", image.ID)
		if _, err := store.UpdateScheduledPost(ctx, post); err != nil {
			t.Fatal(err)
		}
		if err := images.SelectTitleImage(ctx, post.ID, image.ID); err != nil {
			t.Fatal(err)
		}
		if err := images.DeleteImage(ctx, post.ID, image.ID, false); !errors.Is(err, scheduledposts.ErrImageInUse) {
			t.Fatalf("delete selected image = %v", err)
		}
		if err := images.DeleteImage(ctx, post.ID, 0, true); err != nil {
			t.Fatal(err)
		}
		if _, err := images.GetImage(ctx, post.ID, image.ID, false); err != nil {
			t.Fatalf("clearing title deleted body image: %v", err)
		}
		if err := images.SelectTitleImage(ctx, post.ID, image.ID); err != nil {
			t.Fatal(err)
		}
		replacement, err := images.PutTitleImage(ctx, post.ID, "image/png", []byte("replacement"))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := images.GetImage(ctx, post.ID, image.ID, false); err != nil {
			t.Fatalf("replacing title deleted body image: %v", err)
		}
		loaded, err := store.GetScheduledPost(ctx, post.ID)
		if err != nil || loaded.TitleImage == nil || loaded.TitleImage.ID != replacement.ID || len(loaded.Images) != 2 {
			t.Fatalf("shared image metadata = %+v, %v", loaded, err)
		}
		other, err := store.CreateScheduledPost(ctx, scheduledposts.ScheduledPost{
			Title: "other", Community: "rust", Status: scheduledposts.StatusDraft,
		})
		if err != nil {
			t.Fatal(err)
		}
		if err := images.SelectTitleImage(ctx, other.ID, image.ID); !errors.Is(err, scheduledposts.ErrImageNotFound) {
			t.Fatalf("select another post's image = %v", err)
		}
		_, err = pool.Exec(ctx, `UPDATE scheduled_posts SET title_image_id = $1 WHERE id = $2`, image.ID, other.ID)
		var pgErr *pgconn.PgError
		if !errors.As(err, &pgErr) || pgErr.Code != "23503" {
			t.Fatalf("foreign title image constraint = %v", err)
		}
		if err := store.DeleteScheduledPost(ctx, post.ID); err != nil {
			t.Fatalf("cascade deletion with title reference: %v", err)
		}
		var count int
		if err := pool.QueryRow(ctx, `SELECT count(*) FROM post_images WHERE scheduled_post_id = $1`, post.ID).Scan(&count); err != nil || count != 0 {
			t.Fatalf("images remaining after post deletion = %d, %v", count, err)
		}
	})

	t.Run("bulk update", func(t *testing.T) {
		create := func(status scheduledposts.Status, at *time.Time) int64 {
			t.Helper()
			post, err := store.CreateScheduledPost(ctx, scheduledposts.ScheduledPost{
				Title: "bulk", Community: "rust", Status: scheduledposts.StatusDraft,
			})
			if err != nil {
				t.Fatalf("create bulk post: %v", err)
			}
			if _, err := pool.Exec(ctx, `UPDATE scheduled_posts SET status = $2, scheduled_at = $3 WHERE id = $1`,
				post.ID, string(status), at); err != nil {
				t.Fatalf("set bulk post state: %v", err)
			}
			return post.ID
		}
		state := func(id int64) (string, *time.Time, bool) {
			t.Helper()
			var status string
			var at *time.Time
			err := pool.QueryRow(ctx, `SELECT status, scheduled_at FROM scheduled_posts WHERE id = $1`, id).Scan(&status, &at)
			if errors.Is(err, pgx.ErrNoRows) {
				return "", nil, false
			}
			if err != nil {
				t.Fatalf("read bulk post: %v", err)
			}
			return status, at, true
		}
		skipReasons := func(result scheduledposts.BulkResult) map[int64]scheduledposts.SkipReason {
			reasons := make(map[int64]scheduledposts.SkipReason)
			for _, skip := range result.Skipped {
				reasons[skip.ID] = skip.Reason
			}
			return reasons
		}
		soon := time.Now().UTC().Add(time.Hour).Truncate(time.Microsecond)
		later := soon.Add(3 * time.Hour)
		past := time.Now().UTC().Add(-time.Hour).Truncate(time.Microsecond)
		const missing = int64(987654321)

		t.Run("mixed eligibility", func(t *testing.T) {
			scheduledID := create(scheduledposts.StatusScheduled, &soon)
			draftID := create(scheduledposts.StatusDraft, nil)
			pausedID := create(scheduledposts.StatusPaused, &soon)
			result, err := store.BulkUpdate(ctx, scheduledposts.BulkRequest{
				IDs: []int64{scheduledID, draftID, pausedID, missing}, Action: scheduledposts.BulkPause,
			})
			if err != nil {
				t.Fatalf("bulk pause: %v", err)
			}
			if len(result.UpdatedIDs) != 1 || result.UpdatedIDs[0] != scheduledID {
				t.Fatalf("updated = %v, want [%d]", result.UpdatedIDs, scheduledID)
			}
			reasons := skipReasons(result)
			if len(reasons) != 3 || reasons[draftID] != scheduledposts.SkipWrongStatus ||
				reasons[pausedID] != scheduledposts.SkipWrongStatus || reasons[missing] != scheduledposts.SkipNotFound {
				t.Fatalf("skipped = %+v", result.Skipped)
			}
			if status, at, _ := state(scheduledID); status != "paused" || !at.Equal(soon) {
				t.Fatalf("paused post = %s %v", status, at)
			}
		})

		t.Run("resume skips overdue", func(t *testing.T) {
			okID := create(scheduledposts.StatusPaused, &soon)
			overdueID := create(scheduledposts.StatusPaused, &past)
			result, err := store.BulkUpdate(ctx, scheduledposts.BulkRequest{
				IDs: []int64{okID, overdueID}, Action: scheduledposts.BulkResume,
			})
			if err != nil {
				t.Fatalf("bulk resume: %v", err)
			}
			if len(result.UpdatedIDs) != 1 || result.UpdatedIDs[0] != okID || skipReasons(result)[overdueID] != scheduledposts.SkipOverdue {
				t.Fatalf("result = %+v", result)
			}
			if status, _, _ := state(overdueID); status != "paused" {
				t.Fatalf("overdue status = %s, want paused", status)
			}
		})

		t.Run("delay preserves spacing", func(t *testing.T) {
			a := create(scheduledposts.StatusScheduled, &soon)
			b := create(scheduledposts.StatusPaused, &later)
			overdue := create(scheduledposts.StatusScheduled, &past)
			result, err := store.BulkUpdate(ctx, scheduledposts.BulkRequest{
				IDs: []int64{a, b, overdue}, Action: scheduledposts.BulkDelay, Delay: 30 * time.Minute,
			})
			if err != nil {
				t.Fatalf("bulk delay: %v", err)
			}
			if len(result.UpdatedIDs) != 2 || skipReasons(result)[overdue] != scheduledposts.SkipOverdue {
				t.Fatalf("result = %+v", result)
			}
			statusA, atA, _ := state(a)
			statusB, atB, _ := state(b)
			if statusA != "scheduled" || statusB != "paused" || !atA.Equal(soon.Add(30*time.Minute)) || !atB.Equal(later.Add(30*time.Minute)) {
				t.Fatalf("delayed = %s %v, %s %v", statusA, atA, statusB, atB)
			}
		})

		t.Run("delete", func(t *testing.T) {
			draftID := create(scheduledposts.StatusDraft, nil)
			postedID := create(scheduledposts.StatusPosted, nil)
			result, err := store.BulkUpdate(ctx, scheduledposts.BulkRequest{
				IDs: []int64{draftID, postedID, missing}, Action: scheduledposts.BulkDelete,
			})
			if err != nil {
				t.Fatalf("bulk delete: %v", err)
			}
			if len(result.UpdatedIDs) != 2 || skipReasons(result)[missing] != scheduledposts.SkipNotFound {
				t.Fatalf("result = %+v", result)
			}
			if _, _, ok := state(draftID); ok {
				t.Fatal("draft still exists")
			}
		})

		t.Run("posting rows are untouched", func(t *testing.T) {
			for _, request := range []scheduledposts.BulkRequest{
				{Action: scheduledposts.BulkDelete}, {Action: scheduledposts.BulkPause},
				{Action: scheduledposts.BulkResume}, {Action: scheduledposts.BulkDelay, Delay: time.Hour},
			} {
				id := create(scheduledposts.StatusPosting, &soon)
				request.IDs = []int64{id}
				result, err := store.BulkUpdate(ctx, request)
				if err != nil {
					t.Fatalf("bulk %s: %v", request.Action, err)
				}
				if len(result.UpdatedIDs) != 0 || skipReasons(result)[id] != scheduledposts.SkipWrongStatus {
					t.Fatalf("bulk %s result = %+v", request.Action, result)
				}
				if status, at, ok := state(id); !ok || status != "posting" || !at.Equal(soon) {
					t.Fatalf("bulk %s changed posting row: %s %v %v", request.Action, status, at, ok)
				}
			}
		})
	})

	t.Run("image migration down", func(t *testing.T) {
		post, err := store.CreateScheduledPost(ctx, scheduledposts.ScheduledPost{
			Title: "down migration", Community: "rust", Status: scheduledposts.StatusDraft,
		})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := NewImageStore(pool).PutTitleImage(ctx, post.ID, "image/png", []byte("image")); err != nil {
			t.Fatal(err)
		}
		migration, err := migrations.FS.ReadFile("0003_post_images.down.sql")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := pool.Exec(ctx, string(migration)); err != nil {
			t.Fatalf("apply image down migration: %v", err)
		}
		var exists bool
		if err := pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM information_schema.columns
			WHERE table_schema = current_schema() AND table_name = 'scheduled_posts' AND column_name = 'title_image_id')`).Scan(&exists); err != nil || exists {
			t.Fatalf("title image column after down migration: exists=%t, err=%v", exists, err)
		}
	})
}
