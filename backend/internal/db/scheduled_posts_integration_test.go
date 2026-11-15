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

	for _, name := range []string{"0001_initial.up.sql", "0002_scheduled_posts.up.sql"} {
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
}
