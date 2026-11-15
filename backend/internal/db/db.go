// Package db is a thin adapter over pgx and golang-migrate. It holds no
// application logic; callers inject it through small seams so they can be
// tested without a database.
package db

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"time"

	"github.com/golang-migrate/migrate/v4"
	// Registers the "pgx5" database driver with golang-migrate.
	_ "github.com/golang-migrate/migrate/v4/database/pgx/v5"
	"github.com/golang-migrate/migrate/v4/source/iofs"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/gumert/LemmyScheduler/backend/migrations"
)

// pingTimeout bounds the initial connection check so a dead or unreachable
// database fails startup quickly instead of hanging it.
const pingTimeout = 5 * time.Second

// Migrate applies every pending embedded migration. Already being up to date
// is success. Any other failure, including a dirty schema_migrations state, is
// returned with the schema version and cause; it never forces a version.
// Concurrent instances are safe: the driver holds a Postgres advisory lock
// while migrating.
//
// golang-migrate cannot be cancelled mid-migration, so ctx is only checked
// before starting.
func Migrate(ctx context.Context, databaseURL string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	migrateURL, err := toMigrateURL(databaseURL)
	if err != nil {
		return err
	}
	src, err := iofs.New(migrations.FS, ".")
	if err != nil {
		return fmt.Errorf("db: load embedded migrations: %w", err)
	}
	m, err := migrate.NewWithSourceInstance("iofs", src, migrateURL)
	if err != nil {
		_ = src.Close()
		return fmt.Errorf("db: connect for migrations: %w", err)
	}
	defer func() { _, _ = m.Close() }()

	if err := m.Up(); err != nil && !errors.Is(err, migrate.ErrNoChange) {
		version, dirty, verr := m.Version()
		if verr != nil && !errors.Is(verr, migrate.ErrNilVersion) {
			return fmt.Errorf("db: migrate up: %w", err)
		}
		return fmt.Errorf("db: migrate up (schema version %d, dirty=%t): %w", version, dirty, err)
	}
	return nil
}

// Open creates a connection pool and verifies it with a bounded Ping. The
// caller must Close the returned pool.
func Open(ctx context.Context, databaseURL string) (*pgxpool.Pool, error) {
	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		return nil, fmt.Errorf("db: create pool: %w", err)
	}
	pingCtx, cancel := context.WithTimeout(ctx, pingTimeout)
	defer cancel()
	if err := pool.Ping(pingCtx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("db: ping: %w", err)
	}
	return pool, nil
}

// toMigrateURL converts a standard postgres:// (or postgresql://) URL to the
// pgx5:// scheme golang-migrate's pgx driver registers under, keeping the
// rest, including query parameters such as sslmode, unchanged. This is the one
// place that conversion happens.
//
// Errors never echo the URL, because it may contain a password.
func toMigrateURL(databaseURL string) (string, error) {
	u, err := url.Parse(databaseURL)
	if err != nil {
		var uerr *url.Error
		if errors.As(err, &uerr) {
			err = uerr.Err
		}
		return "", fmt.Errorf("db: invalid database URL: %w", err)
	}
	switch u.Scheme {
	case "postgres", "postgresql":
		u.Scheme = "pgx5"
		return u.String(), nil
	default:
		return "", fmt.Errorf("db: unsupported database URL scheme %q, want postgres or postgresql", u.Scheme)
	}
}
