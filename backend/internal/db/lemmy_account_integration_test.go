package db

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/gumert/LemmyScheduler/backend/internal/lemmyaccount"
	"github.com/gumert/LemmyScheduler/backend/migrations"
)

// SAFETY TEST: stale and transient login results cannot corrupt account identity or authentication health.
func TestLemmyAccountStorePostgres(t *testing.T) {
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
		t.Fatal(err)
	}
	schema := "lemmy_account_test_" + hex.EncodeToString(random[:])
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
		t.Fatal(err)
	}
	config.ConnConfig.RuntimeParams["search_path"] = schema
	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		t.Fatalf("connect test schema: %v", err)
	}
	defer pool.Close()

	for _, name := range []string{
		"0001_initial.up.sql", "0002_scheduled_posts.up.sql", "0003_post_images.up.sql",
		"0004_lemmy_account.up.sql", "0005_scheduler_safety.up.sql",
	} {
		migration, err := migrations.FS.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := pool.Exec(ctx, string(migration)); err != nil {
			t.Fatalf("apply %s: %v", name, err)
		}
	}

	store := NewLemmyAccountStore(pool)
	if _, err := store.Get(ctx); !errors.Is(err, lemmyaccount.ErrNotConfigured) {
		t.Fatalf("empty Get: %v", err)
	}
	if err := store.RecordLogin(ctx, 0, lemmyaccount.LoginResult{OK: true, At: time.Now()}); !errors.Is(err, lemmyaccount.ErrNotConfigured) {
		t.Fatalf("RecordLogin without an account = %v, want ErrNotConfigured", err)
	}
	if err := store.SetQueuePausedUntil(ctx, time.Now()); !errors.Is(err, lemmyaccount.ErrNotConfigured) {
		t.Fatalf("SetQueuePausedUntil without an account = %v, want ErrNotConfigured", err)
	}
	if err := store.ClearQueuePausedUntil(ctx); !errors.Is(err, lemmyaccount.ErrNotConfigured) {
		t.Fatalf("ClearQueuePausedUntil without an account = %v, want ErrNotConfigured", err)
	}

	at := time.Now().UTC().Truncate(time.Microsecond)
	first := lemmyaccount.Account{InstanceURL: "https://a.test", Username: "a", Password: "pw1"}
	if err := store.Save(ctx, first, lemmyaccount.LoginResult{OK: true, At: at}); err != nil {
		t.Fatal(err)
	}
	pauseUntil := at.Add(2 * time.Hour)
	if err := store.SetQueuePausedUntil(ctx, pauseUntil); err != nil {
		t.Fatal(err)
	}
	second := lemmyaccount.Account{InstanceURL: "https://b.test", Username: "b", Password: "pw2"}
	if err := store.Save(ctx, second, lemmyaccount.LoginResult{OK: true, At: at}); err != nil {
		t.Fatal(err)
	}
	configured, err := store.Get(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if configured.AccountGeneration < 2 {
		t.Fatalf("account generation = %d, want SetAccount to change identity", configured.AccountGeneration)
	}
	var rows int
	if err := pool.QueryRow(ctx, "SELECT count(*) FROM lemmy_account").Scan(&rows); err != nil || rows != 1 {
		t.Fatalf("rows = %d, err = %v; want a singleton", rows, err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO lemmy_account (id, instance_url, username, password, last_login_ok, last_login_at) VALUES (false, 'x', 'x', 'x', true, now())`); err == nil {
		t.Fatal("a second row was accepted")
	}

	transientAt := at.Add(30 * time.Second)
	if err := store.RecordLogin(ctx, configured.AccountGeneration, lemmyaccount.LoginResult{
		OK: false, Error: "network failure", At: transientAt,
	}); err != nil {
		t.Fatal(err)
	}
	got, err := store.Get(ctx)
	if err != nil || got.AuthFailed || got.LastLogin.Error != "network failure" {
		t.Fatalf("transient login incorrectly set auth failure: %+v, %v", got, err)
	}

	failedAt := at.Add(time.Minute)
	if err := store.RecordLogin(ctx, configured.AccountGeneration, lemmyaccount.LoginResult{
		OK: false, AuthFailed: true, Error: "bad", At: failedAt,
	}); err != nil {
		t.Fatal(err)
	}
	got, err = store.Get(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if got.Account != second || got.LastLogin.OK || got.LastLogin.Error != "bad" || !got.LastLogin.At.Equal(failedAt) || !got.AuthFailed {
		t.Fatalf("Get = %+v", got)
	}
	if err := store.RecordLogin(ctx, configured.AccountGeneration-1, lemmyaccount.LoginResult{
		OK: false, Error: "stale", At: failedAt.Add(time.Minute),
	}); err != nil {
		t.Fatalf("stale login result: %v", err)
	}
	got, err = store.Get(ctx)
	if err != nil || got.LastLogin.Error != "bad" || !got.AuthFailed {
		t.Fatalf("stale login changed account state: %+v, %v", got, err)
	}
	if err := store.SetQueuePausedUntil(ctx, at.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if err := store.SetQueuePausedUntil(ctx, at.Add(90*time.Minute)); err != nil {
		t.Fatal(err)
	}
	got, err = store.Get(ctx)
	if err != nil || got.QueuePausedUntil == nil || !got.QueuePausedUntil.Equal(pauseUntil) {
		t.Fatalf("queue pause shortened: %+v, %v", got.QueuePausedUntil, err)
	}
	if err := store.RecordLogin(ctx, configured.AccountGeneration, lemmyaccount.LoginResult{
		OK: false, Error: "transient", At: failedAt.Add(2 * time.Minute),
	}); err != nil {
		t.Fatal(err)
	}
	got, err = store.Get(ctx)
	if err != nil || !got.AuthFailed || got.LastLogin.Error != "transient" {
		t.Fatalf("transient login changed auth health: %+v, %v", got, err)
	}
	if err := store.RecordLogin(ctx, configured.AccountGeneration, lemmyaccount.LoginResult{
		OK: true, At: failedAt.Add(3 * time.Minute),
	}); err != nil {
		t.Fatal(err)
	}
	got, err = store.Get(ctx)
	if err != nil || got.AuthFailed || got.QueuePausedUntil == nil || !got.QueuePausedUntil.Equal(pauseUntil) {
		t.Fatalf("successful login did not clear auth failure or changed pause: %+v, %v", got, err)
	}
	if err := store.Save(ctx, first, lemmyaccount.LoginResult{OK: true, At: failedAt.Add(4 * time.Minute)}); err != nil {
		t.Fatal(err)
	}
	got, err = store.Get(ctx)
	if err != nil || got.AuthFailed || got.QueuePausedUntil == nil || !got.QueuePausedUntil.Equal(pauseUntil) {
		t.Fatalf("SetAccount did not clear auth failure or preserve pause: %+v, %v", got, err)
	}
}
