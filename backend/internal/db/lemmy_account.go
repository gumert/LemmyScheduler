package db

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/gumert/LemmyScheduler/backend/internal/lemmyaccount"
)

type lemmyAccountStore struct {
	pool *pgxpool.Pool
}

func NewLemmyAccountStore(pool *pgxpool.Pool) lemmyaccount.Store {
	return lemmyAccountStore{pool: pool}
}

func (s lemmyAccountStore) Get(ctx context.Context) (lemmyaccount.Status, error) {
	var st lemmyaccount.Status
	err := s.pool.QueryRow(ctx, `
		SELECT instance_url, username, password, last_login_ok, last_login_error, last_login_at,
		       account_generation, queue_paused_until, auth_failed
		FROM lemmy_account`).Scan(
		&st.Account.InstanceURL, &st.Account.Username, &st.Account.Password,
		&st.LastLogin.OK, &st.LastLogin.Error, &st.LastLogin.At,
		&st.AccountGeneration, &st.QueuePausedUntil, &st.AuthFailed)
	if errors.Is(err, pgx.ErrNoRows) {
		return lemmyaccount.Status{}, lemmyaccount.ErrNotConfigured
	}
	if err != nil {
		return lemmyaccount.Status{}, fmt.Errorf("db: get lemmy account: %w", err)
	}
	st.LastLogin.At = st.LastLogin.At.UTC()
	if st.QueuePausedUntil != nil {
		pausedUntil := st.QueuePausedUntil.UTC()
		st.QueuePausedUntil = &pausedUntil
	}
	return st, nil
}

func (s lemmyAccountStore) Save(ctx context.Context, account lemmyaccount.Account, result lemmyaccount.LoginResult) error {
	_, err := s.pool.Exec(ctx, `
		INSERT INTO lemmy_account (id, instance_url, username, password, last_login_ok, last_login_error, last_login_at,
		                           account_generation, auth_failed)
		VALUES (true, $1, $2, $3, $4, $5, $6, 1, false)
		ON CONFLICT (id) DO UPDATE SET
			instance_url = EXCLUDED.instance_url,
			username = EXCLUDED.username,
			password = EXCLUDED.password,
			last_login_ok = EXCLUDED.last_login_ok,
			last_login_error = EXCLUDED.last_login_error,
			last_login_at = EXCLUDED.last_login_at,
			account_generation = lemmy_account.account_generation + 1,
			auth_failed = false`,
		account.InstanceURL, account.Username, account.Password, result.OK, result.Error, result.At.UTC())
	if err != nil {
		return fmt.Errorf("db: save lemmy account: %w", err)
	}
	return nil
}

func (s lemmyAccountStore) RecordLogin(ctx context.Context, generation int64, result lemmyaccount.LoginResult) error {
	tag, err := s.pool.Exec(ctx, `
		UPDATE lemmy_account SET
			last_login_ok = $1, last_login_error = $2, last_login_at = $3,
			auth_failed = CASE WHEN $4 THEN $5 ELSE auth_failed END
		WHERE id = true AND account_generation = $6`,
		result.OK, result.Error, result.At.UTC(), result.OK || result.AuthFailed, result.AuthFailed, generation)
	if err != nil {
		return fmt.Errorf("db: record lemmy login: %w", err)
	}
	if tag.RowsAffected() == 0 {
		var exists bool
		if err := s.pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM lemmy_account WHERE id = true)`).Scan(&exists); err != nil {
			return fmt.Errorf("db: check lemmy account after login: %w", err)
		}
		if !exists {
			return lemmyaccount.ErrNotConfigured
		}
		// The login belongs to an account that has since been replaced.
	}
	return nil
}

func (s lemmyAccountStore) SetQueuePausedUntil(ctx context.Context, until time.Time) error {
	tag, err := s.pool.Exec(ctx, `
		UPDATE lemmy_account SET queue_paused_until = GREATEST(queue_paused_until, $1)
		WHERE id = true`, until.UTC())
	if err != nil {
		return fmt.Errorf("db: set queue pause: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return lemmyaccount.ErrNotConfigured
	}
	return nil
}

func (s lemmyAccountStore) ClearQueuePausedUntil(ctx context.Context) error {
	tag, err := s.pool.Exec(ctx, `UPDATE lemmy_account SET queue_paused_until = NULL WHERE id = true`)
	if err != nil {
		return fmt.Errorf("db: clear queue pause: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return lemmyaccount.ErrNotConfigured
	}
	return nil
}
