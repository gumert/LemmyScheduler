// Package lemmyclient is a small client for the Lemmy v3 and v4 APIs. It
// implements only the endpoints this project needs, with the standard library.
//
// Secrets: the password is sent only in the login request body, tokens only in
// the Authorization header, and no error or log line produced here contains
// either. Redirects are never followed, so credentials cannot be forwarded to
// another host.
package lemmyclient

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gumert/LemmyScheduler/backend/internal/lemmyaccount"
)

const (
	// DefaultTimeout bounds every request to the instance.
	DefaultTimeout   = 10 * time.Second
	maxResponseBytes = 16 << 20
)

// Community is a community the account follows. Name is qualified as
// name@instance.
type Community struct {
	ID    int64
	Name  string
	Title string
}

// NewPost and PostResult are placeholders for CreatePost (handoff 08b).
type NewPost struct {
	Title       string
	CommunityID int64
	URL         string
	Body        string
	NSFW        bool
}

type PostResult struct {
	ID  int64
	URL string
}

// Client is everything the project needs from Lemmy. Only Validate,
// FollowedCommunities, CommunityID and Reset are implemented in 07; the other
// calls return ErrNotImplemented until their handoffs.
type Client interface {
	// Validate checks that the instance runs Lemmy 0.19+ and that the account can
	// log in. It never touches the cached token or the stored login result.
	Validate(ctx context.Context, account lemmyaccount.Account) error
	// FollowedCommunities lists the communities the stored account follows.
	FollowedCommunities(ctx context.Context) ([]Community, error)
	// CommunityID resolves "name" or "name@instance" to a community id.
	CommunityID(ctx context.Context, name string) (int64, error)
	// CreatePost is implemented in handoff 08b.
	CreatePost(ctx context.Context, post NewPost) (PostResult, error)
	// UploadImage is implemented in handoff 10.
	UploadImage(ctx context.Context, contentType string, data []byte) (string, error)
	// Reset drops the cached token so the next call logs in again.
	Reset()
}

type httpClient struct {
	store lemmyaccount.Store
	http  *http.Client
	now   func() time.Time

	loginMu sync.Mutex // serialises logins
	mu      sync.Mutex // guards token, version and gen
	token   string
	version apiVersion
	account lemmyaccount.Account
	gen     uint64
}

type apiVersion string

const (
	v3 apiVersion = "/api/v3"
	v4 apiVersion = "/api/v4"
)

// New returns a Client for the account in store. A nil httpc gets a client with
// DefaultTimeout.
func New(store lemmyaccount.Store, httpc *http.Client) Client {
	c := &httpClient{store: store, now: time.Now}
	if httpc == nil {
		httpc = &http.Client{Timeout: DefaultTimeout}
	}
	// Copy so the redirect policy is never inherited from, or leaked to, the caller.
	copied := *httpc
	copied.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	c.http = &copied
	return c
}

func (c *httpClient) Reset() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.token = ""
	c.version = ""
	c.account = lemmyaccount.Account{}
	c.gen++
}

func (c *httpClient) CreatePost(context.Context, NewPost) (PostResult, error) {
	return PostResult{}, ErrNotImplemented
}

func (c *httpClient) UploadImage(context.Context, string, []byte) (string, error) {
	return "", ErrNotImplemented
}

func (c *httpClient) Validate(ctx context.Context, account lemmyaccount.Account) error {
	version, err := c.checkVersion(ctx, account.InstanceURL)
	if err != nil {
		return err
	}
	_, err = c.login(ctx, account, version)
	return err
}

func (c *httpClient) checkVersion(ctx context.Context, instanceURL string) (apiVersion, error) {
	var site struct {
		Version string `json:"version"`
	}
	version := v4
	err := c.do(ctx, http.MethodGet, instanceURL, version, "/site", nil, "", nil, &site)
	var apiErr *APIError
	if errors.As(err, &apiErr) && (apiErr.Status == http.StatusNotFound || apiErr.Status == http.StatusMethodNotAllowed) {
		version = v3
		err = c.do(ctx, http.MethodGet, instanceURL, version, "/site", nil, "", nil, &site)
		if errors.As(err, &apiErr) && (apiErr.Status == http.StatusNotFound || apiErr.Status == http.StatusMethodNotAllowed) {
			return "", ErrUnsupportedVersion
		}
	}
	if err != nil {
		return "", err
	}
	major, minor, ok := parseVersion(site.Version)
	if !ok {
		return "", ErrInvalidResponse
	}
	if (version == v4 && major < 1) || (version == v3 && (major != 0 || minor < 19)) {
		return "", ErrUnsupportedVersion
	}
	return version, nil
}

func parseVersion(version string) (int, int, bool) {
	parts := strings.Split(strings.TrimPrefix(strings.TrimSpace(version), "v"), ".")
	if len(parts) < 2 {
		return 0, 0, false
	}
	major, errMajor := strconv.Atoi(parts[0])
	minor, errMinor := strconv.Atoi(parts[1])
	return major, minor, errMajor == nil && errMinor == nil && major >= 0 && minor >= 0
}

// login performs one login and returns the token. It does not record the
// result; callers decide whether the attempt counts.
func (c *httpClient) login(ctx context.Context, account lemmyaccount.Account, version apiVersion) (string, error) {
	body, err := json.Marshal(map[string]string{
		"username_or_email": account.Username,
		"password":          account.Password,
	})
	if err != nil {
		return "", err
	}
	var res struct {
		JWT string `json:"jwt"`
	}
	path := "/account/auth/login"
	if version == v3 {
		path = "/user/login"
	}
	err = c.do(ctx, http.MethodPost, account.InstanceURL, version, path, nil, "", body, &res)
	if err != nil {
		return "", classifyLoginError(err)
	}
	if res.JWT == "" {
		return "", ErrAccountUnusable
	}
	return res.JWT, nil
}

func classifyLoginError(err error) error {
	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		return err
	}
	switch apiErr.Code {
	case "incorrect_login":
		return ErrBadCredentials
	case "missing_totp_token", "incorrect_totp_token":
		return ErrTwoFactorUnsupported
	case "email_not_verified", "registration_application_is_pending", "registration_denied",
		"site_ban", "banned_from_site", "person_is_banned_from_site", "deleted", "account_deleted":
		return ErrAccountUnusable
	}
	if apiErr.Status == http.StatusUnauthorized {
		return ErrBadCredentials
	}
	return err
}

// recordLogin stores the outcome of a login attempt. A failure to record is
// logged, not returned: it must not mask the login result.
func (c *httpClient) recordLogin(ctx context.Context, loginErr error) {
	result := lemmyaccount.LoginResult{OK: loginErr == nil, At: c.now().UTC()}
	if loginErr != nil {
		result.Error = loginErr.Error()
	}
	if err := c.store.RecordLogin(context.WithoutCancel(ctx), result); err != nil {
		log.Printf("lemmyclient: record login result: %v", err)
	}
}

// session returns a token for account, logging in when none is cached.
func (c *httpClient) session(ctx context.Context, account lemmyaccount.Account) (string, apiVersion, uint64, error) {
	c.loginMu.Lock()
	defer c.loginMu.Unlock()
	c.mu.Lock()
	token, version, gen := c.token, c.version, c.gen
	if c.account != account {
		token = ""
	}
	c.mu.Unlock()
	if token != "" {
		return token, version, gen, nil
	}
	version, err := c.checkVersion(ctx, account.InstanceURL)
	if err != nil {
		return "", "", gen, err
	}
	token, err = c.login(ctx, account, version)
	c.recordLogin(ctx, err)
	if err != nil {
		return "", "", gen, err
	}
	c.mu.Lock()
	if c.gen == gen {
		c.token = token
		c.version = version
		c.account = account
	}
	c.mu.Unlock()
	return token, version, gen, nil
}

func (c *httpClient) invalidate(gen uint64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.gen == gen {
		c.token = ""
		c.version = ""
		c.account = lemmyaccount.Account{}
		c.gen++
	}
}

// authedGet calls path with the cached token. On an authentication failure it
// logs in again once and retries the call once.
func (c *httpClient) authedGet(ctx context.Context, paths map[apiVersion]string, query url.Values, out any, validate func(apiVersion) error) error {
	status, err := c.store.Get(ctx)
	if err != nil {
		return err
	}
	account := status.Account
	for attempt := 0; ; attempt++ {
		token, version, gen, err := c.session(ctx, account)
		if err != nil {
			return err
		}
		err = c.do(ctx, http.MethodGet, account.InstanceURL, version, paths[version], query, token, nil, out)
		if err == nil && validate != nil {
			err = validate(version)
		}
		if attempt == 0 && isAuthFailure(err) {
			c.invalidate(gen)
			continue
		}
		return err
	}
}

func isAuthFailure(err error) bool {
	var apiErr *APIError
	return errors.As(err, &apiErr) && (apiErr.Status == http.StatusUnauthorized || apiErr.Code == "not_logged_in")
}

func (c *httpClient) FollowedCommunities(ctx context.Context) ([]Community, error) {
	var res struct {
		Follows []struct {
			Community communityJSON `json:"community"`
		} `json:"follows"`
		MyUser *struct {
			Follows []struct {
				Community communityJSON `json:"community"`
			} `json:"follows"`
		} `json:"my_user"`
	}
	if err := c.authedGet(ctx, map[apiVersion]string{v3: "/site", v4: "/account"}, nil, &res, func(version apiVersion) error {
		if version == v3 {
			if res.MyUser == nil {
				return &APIError{Status: http.StatusUnauthorized}
			}
			res.Follows = res.MyUser.Follows
		}
		return nil
	}); err != nil {
		return nil, err
	}
	communities := make([]Community, 0, len(res.Follows))
	for _, f := range res.Follows {
		communities = append(communities, f.Community.toCommunity())
	}
	return communities, nil
}

func (c *httpClient) CommunityID(ctx context.Context, name string) (int64, error) {
	var res struct {
		CommunityView struct {
			Community communityJSON `json:"community"`
		} `json:"community_view"`
	}
	err := c.authedGet(ctx, map[apiVersion]string{v3: "/community", v4: "/community"}, url.Values{"name": {name}}, &res, nil)
	var apiErr *APIError
	if errors.As(err, &apiErr) && (apiErr.Status == http.StatusNotFound || apiErr.Code == "couldnt_find_community") {
		return 0, ErrCommunityNotFound
	}
	if err != nil {
		return 0, err
	}
	return res.CommunityView.Community.ID, nil
}

type communityJSON struct {
	ID      int64  `json:"id"`
	Name    string `json:"name"`
	Title   string `json:"title"`
	APID    string `json:"ap_id"`
	ActorID string `json:"actor_id"`
}

func (c communityJSON) toCommunity() Community {
	name := c.Name
	actor := c.APID
	if actor == "" {
		actor = c.ActorID
	}
	if u, err := url.Parse(actor); err == nil && u.Host != "" {
		name += "@" + u.Host
	}
	return Community{ID: c.ID, Name: name, Title: c.Title}
}

var errorCode = regexp.MustCompile(`^[a-z0-9_]{1,64}$`)

// do sends one request to the selected API version and decodes a 2xx JSON response into
// out. Errors never include the request body or the response body.
func (c *httpClient) do(ctx context.Context, method, instanceURL string, version apiVersion, path string, query url.Values, token string, body []byte, out any) error {
	target := instanceURL + string(version) + path
	if len(query) > 0 {
		target += "?" + query.Encode()
	}
	var reader io.Reader
	if body != nil {
		reader = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, target, reader)
	if err != nil {
		return ErrUnreachable
	}
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}

	res, err := c.http.Do(req)
	if err != nil {
		return unreachable(err)
	}
	defer res.Body.Close()
	data, err := io.ReadAll(io.LimitReader(res.Body, maxResponseBytes))
	if err != nil {
		return unreachable(err)
	}

	switch {
	case res.StatusCode == http.StatusTooManyRequests:
		return &RateLimitedError{RetryAfter: retryAfter(res.Header.Get("Retry-After"))}
	case res.StatusCode >= 500:
		return &unreachableError{detail: "HTTP " + strconv.Itoa(res.StatusCode)}
	case res.StatusCode < 200 || res.StatusCode > 299:
		return &APIError{Status: res.StatusCode, Code: parseErrorCode(data)}
	}
	if err := json.Unmarshal(data, out); err != nil {
		return ErrInvalidResponse
	}
	return nil
}

func parseErrorCode(data []byte) string {
	var e struct {
		Error string `json:"error"`
	}
	if json.Unmarshal(data, &e) != nil || !errorCode.MatchString(e.Error) {
		return ""
	}
	return e.Error
}

func retryAfter(header string) time.Duration {
	seconds, err := strconv.Atoi(strings.TrimSpace(header))
	if err != nil || seconds <= 0 {
		return 0
	}
	return time.Duration(seconds) * time.Second
}

type unreachableError struct{ detail string }

func (e *unreachableError) Error() string { return ErrUnreachable.Error() + " (" + e.detail + ")" }
func (e *unreachableError) Unwrap() error { return ErrUnreachable }

// unreachable describes a transport failure without echoing the request URL.
func unreachable(err error) error {
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		return &unreachableError{detail: "timed out"}
	case errors.Is(err, context.Canceled):
		return context.Canceled
	}
	return &unreachableError{detail: "connection failed"}
}
