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

	for _, name := range []string{"0004_lemmy_account.up.sql", "0004_lemmy_account.down.sql", "0004_lemmy_account.up.sql"} {
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
	if err := store.RecordLogin(ctx, lemmyaccount.LoginResult{OK: true, At: time.Now()}); err != nil {
		t.Fatalf("RecordLogin without an account must be a no-op: %v", err)
	}

	at := time.Now().UTC().Truncate(time.Microsecond)
	first := lemmyaccount.Account{InstanceURL: "https://a.test", Username: "a", Password: "pw1"}
	if err := store.Save(ctx, first, lemmyaccount.LoginResult{OK: true, At: at}); err != nil {
		t.Fatal(err)
	}
	second := lemmyaccount.Account{InstanceURL: "https://b.test", Username: "b", Password: "pw2"}
	if err := store.Save(ctx, second, lemmyaccount.LoginResult{OK: true, At: at}); err != nil {
		t.Fatal(err)
	}
	var rows int
	if err := pool.QueryRow(ctx, "SELECT count(*) FROM lemmy_account").Scan(&rows); err != nil || rows != 1 {
		t.Fatalf("rows = %d, err = %v; want a singleton", rows, err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO lemmy_account (id, instance_url, username, password, last_login_ok, last_login_at) VALUES (false, 'x', 'x', 'x', true, now())`); err == nil {
		t.Fatal("a second row was accepted")
	}

	failedAt := at.Add(time.Minute)
	if err := store.RecordLogin(ctx, lemmyaccount.LoginResult{OK: false, Error: "bad", At: failedAt}); err != nil {
		t.Fatal(err)
	}
	got, err := store.Get(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if got.Account != second || got.LastLogin.OK || got.LastLogin.Error != "bad" || !got.LastLogin.At.Equal(failedAt) {
		t.Fatalf("Get = %+v", got)
	}
}
