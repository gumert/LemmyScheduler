// Package lemmyaccount holds the stored Lemmy account and the seam used to
// persist it. The password is stored in plain text by design (see AGENTS.md)
// and must never leave the backend: no RPC, log line or error message may
// contain it.
package lemmyaccount

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"
)

// ErrNotConfigured means no account has been saved yet.
var ErrNotConfigured = errors.New("lemmyaccount: no account configured")

type Account struct {
	InstanceURL string
	Username    string
	Password    string
}

// LoginResult is the outcome of the latest login attempt. Error is a message
// safe to show to the user; it never contains a password or token.
type LoginResult struct {
	OK    bool
	Error string
	At    time.Time
}

type Status struct {
	Account   Account
	LastLogin LoginResult
}

// Store persists the singleton account.
type Store interface {
	// Get returns ErrNotConfigured when nothing is saved.
	Get(ctx context.Context) (Status, error)
	// Save replaces the account and records result as its latest login.
	Save(ctx context.Context, account Account, result LoginResult) error
	// RecordLogin updates the latest login result. It does nothing when no
	// account is saved.
	RecordLogin(ctx context.Context, result LoginResult) error
}

// NormalizeInstanceURL requires an http or https scheme and a host, rejects
// credentials, paths, queries and fragments, and strips a trailing slash. Plain
// http is allowed for local testing.
func NormalizeInstanceURL(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Hostname() == "" {
		return "", errors.New("instance URL must start with https:// (or http:// for local testing), for example https://lemmy.example")
	}
	if u.User != nil || (u.Path != "" && u.Path != "/") || u.RawQuery != "" || u.Fragment != "" || u.ForceQuery {
		return "", errors.New("instance URL must be just the site address, without credentials, a path, query or fragment")
	}
	return fmt.Sprintf("%s://%s", u.Scheme, u.Host), nil
}
