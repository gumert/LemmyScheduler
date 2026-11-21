package lemmyclient

import (
	"errors"
	"fmt"
	"time"
)

var (
	// ErrBadCredentials means Lemmy rejected the username or password.
	ErrBadCredentials = errors.New("Lemmy rejected the username or password")
	// ErrTwoFactorUnsupported means the account requires a TOTP code, which this
	// app does not support.
	ErrTwoFactorUnsupported = errors.New("this account uses two-factor authentication, which is not supported")
	// ErrUnsupportedVersion means the instance does not expose a supported API.
	ErrUnsupportedVersion = errors.New("the instance needs Lemmy 0.19 or newer")
	// ErrRateLimited means the instance answered 429. Use RateLimitedError for
	// the retry delay.
	ErrRateLimited = errors.New("the instance is rate limiting requests")
	// ErrUnreachable covers network failures, timeouts and 5xx responses.
	ErrUnreachable = errors.New("the instance is unreachable")
	// ErrAccountUnusable means login was refused for a reason other than the
	// credentials, such as an unverified, banned or pending account.
	ErrAccountUnusable = errors.New("the account cannot log in")
	// ErrInvalidResponse means the instance answered, but not like Lemmy would.
	ErrInvalidResponse = errors.New("the instance returned an unexpected response")
	// ErrCommunityNotFound means the instance does not know the community.
	ErrCommunityNotFound = errors.New("community not found")
	// ErrNotImplemented marks endpoints that a later handoff implements.
	ErrNotImplemented = errors.New("not implemented")
)

// RateLimitedError wraps ErrRateLimited with the Retry-After delay, if given.
type RateLimitedError struct {
	RetryAfter time.Duration
}

func (e *RateLimitedError) Error() string {
	if e.RetryAfter > 0 {
		return fmt.Sprintf("%v; retry after %s", ErrRateLimited, e.RetryAfter)
	}
	return ErrRateLimited.Error()
}

func (e *RateLimitedError) Unwrap() error { return ErrRateLimited }

// APIError retains the code for classification but never includes it in the
// message: an untrusted server could echo a secret as its error code.
type APIError struct {
	Status int
	Code   string
}

func (e *APIError) Error() string {
	return fmt.Sprintf("Lemmy returned HTTP %d", e.Status)
}
