package db

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"testing"
	"time"

	"github.com/golang-migrate/migrate/v4"
	migrateiofs "github.com/golang-migrate/migrate/v4/source/iofs"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/gumert/LemmyScheduler/backend/migrations"
)

func migrationRunner(t *testing.T, databaseURL string) *migrate.Migrate {
	t.Helper()
	migrateURL, err := toMigrateURL(databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	source, err := migrateiofs.New(migrations.FS, ".")
	if err != nil {
		t.Fatal(err)
	}
	runner, err := migrate.NewWithSourceInstance("iofs", source, migrateURL)
	if err != nil {
		_ = source.Close()
		t.Fatal(err)
	}
	return runner
}

// SAFETY TEST: a conflicting legacy row makes migration 0005 dirty without partial changes, then succeeds only after deliberate repair.
func TestSchedulerSafetyMigrationConflictRecoveryPostgres(t *testing.T) {
	databaseURL := os.Getenv("TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("TEST_DATABASE_URL is unset")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	admin, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		t.Fatalf("connect test database: %v", err)
	}
	defer admin.Close()

	schema := fmt.Sprintf("migration_recovery_test_%d", time.Now().UnixNano())
	if _, err := admin.Exec(ctx, "CREATE SCHEMA "+schema); err != nil {
		t.Fatalf("create test schema: %v", err)
	}
	defer func() {
		if _, err := admin.Exec(context.Background(), "DROP SCHEMA "+schema+" CASCADE"); err != nil {
			t.Errorf("drop test schema: %v", err)
		}
	}()
	config, err := pgxpool.ParseConfig(databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	config.ConnConfig.RuntimeParams["search_path"] = schema
	scopedURLParsed, err := url.Parse(databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	query := scopedURLParsed.Query()
	query.Set("search_path", schema)
	scopedURLParsed.RawQuery = query.Encode()
	scopedURL := scopedURLParsed.String()
	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		t.Fatalf("connect test schema: %v", err)
	}
	defer pool.Close()

	runner := migrationRunner(t, scopedURL)
	if err := runner.Migrate(4); err != nil {
		t.Fatalf("migrate through version 4: %v", err)
	}
	if _, err := runner.Close(); err != nil {
		t.Fatal(err)
	}
	var postID, imageID int64
	if err := pool.QueryRow(ctx, `INSERT INTO scheduled_posts (title, community, url)
		VALUES ('legacy conflict', 'rust', 'https://example.test') RETURNING id`).Scan(&postID); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `INSERT INTO post_images (scheduled_post_id, content_type, size_bytes, data)
		VALUES ($1, 'image/png', 1, 'x'::bytea) RETURNING id`, postID).Scan(&imageID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `UPDATE scheduled_posts SET title_image_id = $2 WHERE id = $1`, postID, imageID); err != nil {
		t.Fatal(err)
	}

	if err := Migrate(ctx, scopedURL); err == nil {
		t.Fatal("migration 0005 accepted conflicting legacy data")
	}
	runner = migrationRunner(t, scopedURL)
	version, dirty, err := runner.Version()
	if err != nil || version != 5 || !dirty {
		t.Fatalf("failed migration version=%d dirty=%t err=%v; want dirty version 5", version, dirty, err)
	}
	for _, check := range []struct {
		name  string
		query string
	}{
		{"attempt table", `SELECT to_regclass('post_attempts') IS NULL`},
		{"queue pause column", `SELECT NOT EXISTS (SELECT 1 FROM information_schema.columns WHERE table_schema = current_schema() AND table_name = 'lemmy_account' AND column_name = 'queue_paused_until')`},
		{"identity column", `SELECT NOT EXISTS (SELECT 1 FROM information_schema.columns WHERE table_schema = current_schema() AND table_name = 'lemmy_account' AND column_name = 'account_generation')`},
		{"Lemmy post identity column", `SELECT NOT EXISTS (SELECT 1 FROM information_schema.columns WHERE table_schema = current_schema() AND table_name = 'scheduled_posts' AND column_name = 'lemmy_post_id')`},
		{"exclusive constraint", `SELECT NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname = 'scheduled_posts_url_title_image_exclusive' AND connamespace = current_schema()::regnamespace)`},
	} {
		var rolledBack bool
		if err := pool.QueryRow(ctx, check.query).Scan(&rolledBack); err != nil || !rolledBack {
			t.Errorf("%s was not rolled back: value=%t err=%v", check.name, rolledBack, err)
		}
	}
	var url string
	var retainedImageID *int64
	if err := pool.QueryRow(ctx, `SELECT url, title_image_id FROM scheduled_posts WHERE id = $1`, postID).
		Scan(&url, &retainedImageID); err != nil || url != "https://example.test" ||
		retainedImageID == nil || *retainedImageID != imageID {
		t.Fatalf("failed migration changed legacy row: url=%q image=%v err=%v", url, retainedImageID, err)
	}

	if _, err := pool.Exec(ctx, `UPDATE scheduled_posts SET title_image_id = NULL WHERE id = $1`, postID); err != nil {
		t.Fatal(err)
	}
	if err := runner.Force(4); err != nil {
		t.Fatalf("deliberately restore version 4 after verifying rollback: %v", err)
	}
	if err := runner.Up(); err != nil && !errors.Is(err, migrate.ErrNoChange) {
		t.Fatalf("retry migration after repair: %v", err)
	}
	if _, err := runner.Close(); err != nil {
		t.Fatal(err)
	}
	runner = migrationRunner(t, scopedURL)
	defer runner.Close()
	version, dirty, err = runner.Version()
	if err != nil || version != 5 || dirty {
		t.Fatalf("recovered migration version=%d dirty=%t err=%v", version, dirty, err)
	}
	var validated bool
	if err := pool.QueryRow(ctx, `SELECT convalidated FROM pg_constraint
		WHERE conname = 'scheduled_posts_url_title_image_exclusive'
		AND connamespace = current_schema()::regnamespace`).Scan(&validated); err != nil || !validated {
		t.Fatalf("exclusive constraint validated=%t err=%v", validated, err)
	}
	if err := runner.Migrate(4); err != nil {
		t.Fatalf("apply migration 0005 down: %v", err)
	}
	version, dirty, err = runner.Version()
	if err != nil || version != 4 || dirty {
		t.Fatalf("down migration version=%d dirty=%t err=%v", version, dirty, err)
	}
	for _, check := range []struct {
		name  string
		query string
	}{
		{"attempt table", `SELECT to_regclass('post_attempts') IS NULL`},
		{"queue pause column", `SELECT NOT EXISTS (SELECT 1 FROM information_schema.columns WHERE table_schema = current_schema() AND table_name = 'lemmy_account' AND column_name = 'queue_paused_until')`},
		{"Lemmy post identity column", `SELECT NOT EXISTS (SELECT 1 FROM information_schema.columns WHERE table_schema = current_schema() AND table_name = 'scheduled_posts' AND column_name = 'lemmy_post_id')`},
		{"exclusive constraint", `SELECT NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname = 'scheduled_posts_url_title_image_exclusive' AND connamespace = current_schema()::regnamespace)`},
	} {
		var removed bool
		if err := pool.QueryRow(ctx, check.query).Scan(&removed); err != nil || !removed {
			t.Errorf("%s remains after down migration: value=%t err=%v", check.name, removed, err)
		}
	}
}
