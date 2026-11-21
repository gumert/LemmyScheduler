-- Singleton: the boolean primary key can only ever be true, so there is at most
-- one row. The password is stored in plain text by design (see AGENTS.md).
CREATE TABLE lemmy_account (
    id boolean PRIMARY KEY DEFAULT true CHECK (id),
    instance_url text NOT NULL,
    username text NOT NULL,
    password text NOT NULL,
    last_login_ok boolean NOT NULL,
    last_login_error text NOT NULL DEFAULT '',
    last_login_at timestamptz NOT NULL
);
