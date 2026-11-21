package db

import (
	"context"
	"errors"
	"fmt"

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
		SELECT instance_url, username, password, last_login_ok, last_login_error, last_login_at
		FROM lemmy_account`).Scan(
		&st.Account.InstanceURL, &st.Account.Username, &st.Account.Password,
		&st.LastLogin.OK, &st.LastLogin.Error, &st.LastLogin.At)
	if errors.Is(err, pgx.ErrNoRows) {
		return lemmyaccount.Status{}, lemmyaccount.ErrNotConfigured
	}
	if err != nil {
		return lemmyaccount.Status{}, fmt.Errorf("db: get lemmy account: %w", err)
	}
	st.LastLogin.At = st.LastLogin.At.UTC()
	return st, nil
}

func (s lemmyAccountStore) Save(ctx context.Context, account lemmyaccount.Account, result lemmyaccount.LoginResult) error {
	_, err := s.pool.Exec(ctx, `
		INSERT INTO lemmy_account (id, instance_url, username, password, last_login_ok, last_login_error, last_login_at)
		VALUES (true, $1, $2, $3, $4, $5, $6)
		ON CONFLICT (id) DO UPDATE SET
			instance_url = EXCLUDED.instance_url,
			username = EXCLUDED.username,
			password = EXCLUDED.password,
			last_login_ok = EXCLUDED.last_login_ok,
			last_login_error = EXCLUDED.last_login_error,
			last_login_at = EXCLUDED.last_login_at`,
		account.InstanceURL, account.Username, account.Password, result.OK, result.Error, result.At.UTC())
	if err != nil {
		return fmt.Errorf("db: save lemmy account: %w", err)
	}
	return nil
}

func (s lemmyAccountStore) RecordLogin(ctx context.Context, result lemmyaccount.LoginResult) error {
	_, err := s.pool.Exec(ctx, `
		UPDATE lemmy_account SET last_login_ok = $1, last_login_error = $2, last_login_at = $3`,
		result.OK, result.Error, result.At.UTC())
	if err != nil {
		return fmt.Errorf("db: record lemmy login: %w", err)
	}
	return nil
}
