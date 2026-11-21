package lemmyclient

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gumert/LemmyScheduler/backend/internal/lemmyaccount"
)

type fakeStore struct {
	mu      sync.Mutex
	status  *lemmyaccount.Status
	results []lemmyaccount.LoginResult
}

func (s *fakeStore) Get(context.Context) (lemmyaccount.Status, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.status == nil {
		return lemmyaccount.Status{}, lemmyaccount.ErrNotConfigured
	}
	return *s.status, nil
}

func (s *fakeStore) Save(context.Context, lemmyaccount.Account, lemmyaccount.LoginResult) error {
	return nil
}

func (s *fakeStore) RecordLogin(_ context.Context, r lemmyaccount.LoginResult) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.results = append(s.results, r)
	return nil
}

func (s *fakeStore) lastResult(t *testing.T) lemmyaccount.LoginResult {
	t.Helper()
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.results) == 0 {
		t.Fatal("no login result recorded")
	}
	return s.results[len(s.results)-1]
}

const secretPassword = "hunter2-secret"

// lemmy is a fake instance. Each handler can be replaced per test.
type lemmy struct {
	version      string
	logins       atomic.Int32
	accountCalls atomic.Int32
	loginStatus  int
	loginBody    string
	// validToken is the only token /account accepts.
	validToken atomic.Value
	redirectTo string
	rejectAll  atomic.Bool
}

func (l *lemmy) handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/v4/site", func(w http.ResponseWriter, r *http.Request) {
		if l.version == "" {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write([]byte(`{"version":"` + l.version + `"}`))
	})
	mux.HandleFunc("POST /api/v4/account/auth/login", func(w http.ResponseWriter, r *http.Request) {
		l.logins.Add(1)
		if l.redirectTo != "" {
			http.Redirect(w, r, l.redirectTo, http.StatusTemporaryRedirect)
			return
		}
		if l.loginStatus != 0 {
			w.WriteHeader(l.loginStatus)
			_, _ = w.Write([]byte(l.loginBody))
			return
		}
		var in struct {
			Username string `json:"username_or_email"`
			Password string `json:"password"`
		}
		_ = json.NewDecoder(r.Body).Decode(&in)
		if in.Password != secretPassword {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"error":"incorrect_login"}`))
			return
		}
		token := "jwt-" + string(rune('a'+l.logins.Load()))
		l.validToken.Store(token)
		_, _ = w.Write([]byte(`{"jwt":"` + token + `","registration_created":false,"verify_email_sent":false}`))
	})
	mux.HandleFunc("GET /api/v4/account", func(w http.ResponseWriter, r *http.Request) {
		l.accountCalls.Add(1)
		valid, _ := l.validToken.Load().(string)
		if l.rejectAll.Load() || valid == "" || r.Header.Get("Authorization") != "Bearer "+valid {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"error":"not_logged_in"}`))
			return
		}
		_, _ = w.Write([]byte(`{"follows":[{"community":{"id":7,"name":"rust","title":"Rust","ap_id":"https://other.example/c/rust"}},{"community":{"id":8,"name":"go","ap_id":"https://lemmy.test/c/go"}}]}`))
	})
	mux.HandleFunc("GET /api/v4/community", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("name") != "rust@other.example" {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"error":"couldnt_find_community"}`))
			return
		}
		_, _ = w.Write([]byte(`{"community_view":{"community":{"id":7,"name":"rust"}}}`))
	})
	return mux
}

func setup(t *testing.T) (Client, *lemmy, *fakeStore, lemmyaccount.Account) {
	t.Helper()
	l := &lemmy{version: "1.0.0"}
	srv := httptest.NewServer(l.handler())
	t.Cleanup(srv.Close)
	account := lemmyaccount.Account{InstanceURL: srv.URL, Username: "me", Password: secretPassword}
	store := &fakeStore{status: &lemmyaccount.Status{Account: account}}
	return New(store, nil), l, store, account
}

func TestValidate(t *testing.T) {
	ctx := context.Background()
	t.Run("ok", func(t *testing.T) {
		c, _, store, account := setup(t)
		if err := c.Validate(ctx, account); err != nil {
			t.Fatalf("Validate: %v", err)
		}
		if len(store.results) != 0 {
			t.Fatal("Validate must not record a login result")
		}
	})
	t.Run("wrong password", func(t *testing.T) {
		c, _, _, account := setup(t)
		account.Password = "nope"
		if err := c.Validate(ctx, account); !errors.Is(err, ErrBadCredentials) {
			t.Fatalf("got %v, want ErrBadCredentials", err)
		}
	})
	t.Run("old instance without v4", func(t *testing.T) {
		c, l, _, account := setup(t)
		l.version = ""
		if err := c.Validate(ctx, account); !errors.Is(err, ErrUnsupportedVersion) {
			t.Fatalf("got %v, want ErrUnsupportedVersion", err)
		}
		if l.logins.Load() != 0 {
			t.Fatal("credentials were sent to an unsupported instance")
		}
	})
	t.Run("version below 1.0", func(t *testing.T) {
		c, l, _, account := setup(t)
		l.version = "0.19.5"
		if err := c.Validate(ctx, account); !errors.Is(err, ErrUnsupportedVersion) {
			t.Fatalf("got %v, want ErrUnsupportedVersion", err)
		}
	})
	loginErrors := map[string]error{
		`{"error":"missing_totp_token"}`: ErrTwoFactorUnsupported,
		`{"error":"email_not_verified"}`: ErrAccountUnusable,
		`{"error":"site_ban"}`:           ErrAccountUnusable,
	}
	for body, want := range loginErrors {
		t.Run(body, func(t *testing.T) {
			c, l, _, account := setup(t)
			l.loginStatus, l.loginBody = http.StatusBadRequest, body
			if err := c.Validate(ctx, account); !errors.Is(err, want) {
				t.Fatalf("got %v, want %v", err, want)
			}
		})
	}
	t.Run("rate limited", func(t *testing.T) {
		c, l, _, account := setup(t)
		l.loginStatus = http.StatusTooManyRequests
		err := c.Validate(ctx, account)
		var rl *RateLimitedError
		if !errors.Is(err, ErrRateLimited) || !errors.As(err, &rl) {
			t.Fatalf("got %v, want rate limited", err)
		}
	})
	t.Run("unreachable", func(t *testing.T) {
		c, _, _, account := setup(t)
		account.InstanceURL = "http://127.0.0.1:1"
		if err := c.Validate(ctx, account); !errors.Is(err, ErrUnreachable) {
			t.Fatalf("got %v, want ErrUnreachable", err)
		}
	})
	t.Run("server error", func(t *testing.T) {
		c, l, _, account := setup(t)
		l.loginStatus = http.StatusBadGateway
		if err := c.Validate(ctx, account); !errors.Is(err, ErrUnreachable) {
			t.Fatalf("got %v, want ErrUnreachable", err)
		}
	})
}

func TestValidateTimesOut(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-time.After(5 * time.Second):
		}
	}))
	defer srv.Close()
	c := New(&fakeStore{}, &http.Client{Timeout: 50 * time.Millisecond})
	err := c.Validate(context.Background(), lemmyaccount.Account{InstanceURL: srv.URL})
	if !errors.Is(err, ErrUnreachable) {
		t.Fatalf("got %v, want ErrUnreachable", err)
	}
}

func TestLoginDoesNotFollowRedirects(t *testing.T) {
	var hit atomic.Bool
	other := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { hit.Store(true) }))
	defer other.Close()
	c, l, _, account := setup(t)
	l.redirectTo = other.URL
	if err := c.Validate(context.Background(), account); err == nil {
		t.Fatal("expected an error")
	}
	if hit.Load() {
		t.Fatal("login followed a redirect to another host")
	}
}

func TestFollowedCommunitiesLogsInLazilyAndRecords(t *testing.T) {
	c, l, store, _ := setup(t)
	ctx := context.Background()
	got, err := c.FollowedCommunities(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0] != (Community{ID: 7, Name: "rust@other.example", Title: "Rust"}) || got[1].Name != "go@lemmy.test" {
		t.Fatalf("communities = %+v", got)
	}
	if _, err := c.FollowedCommunities(ctx); err != nil {
		t.Fatal(err)
	}
	if l.logins.Load() != 1 {
		t.Fatalf("logins = %d, want the token to be reused", l.logins.Load())
	}
	if r := store.lastResult(t); !r.OK || r.At.IsZero() {
		t.Fatalf("login result = %+v", r)
	}
}

func TestReloginOnceOnUnauthorized(t *testing.T) {
	c, l, _, _ := setup(t)
	ctx := context.Background()
	if _, err := c.FollowedCommunities(ctx); err != nil {
		t.Fatal(err)
	}
	l.validToken.Store("rotated") // the instance invalidated our token
	if _, err := c.FollowedCommunities(ctx); err != nil {
		t.Fatalf("expected a transparent re-login, got %v", err)
	}
	if l.logins.Load() != 2 || l.accountCalls.Load() != 3 {
		t.Fatalf("logins=%d accountCalls=%d, want 2 and 3", l.logins.Load(), l.accountCalls.Load())
	}
}

func TestPersistent401IsNotRetriedForever(t *testing.T) {
	c, l, _, _ := setup(t)
	ctx := context.Background()
	if _, err := c.FollowedCommunities(ctx); err != nil {
		t.Fatal(err)
	}
	// Every login now yields a token the instance still rejects.
	l.rejectAll.Store(true)
	c.Reset()
	_, err := c.FollowedCommunities(ctx)
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.Status != http.StatusUnauthorized {
		t.Fatalf("got %v, want a 401 APIError", err)
	}
	if l.accountCalls.Load() != 3 {
		t.Fatalf("accountCalls = %d, want exactly one retry", l.accountCalls.Load())
	}
}

func TestFailedLazyLoginIsRecorded(t *testing.T) {
	c, _, store, _ := setup(t)
	store.status.Account.Password = "changed"
	if _, err := c.FollowedCommunities(context.Background()); !errors.Is(err, ErrBadCredentials) {
		t.Fatalf("got %v", err)
	}
	r := store.lastResult(t)
	if r.OK || r.Error == "" {
		t.Fatalf("login result = %+v", r)
	}
	if strings.Contains(r.Error, "changed") || strings.Contains(r.Error, secretPassword) {
		t.Fatalf("recorded error leaks the password: %q", r.Error)
	}
}

func TestResetDropsToken(t *testing.T) {
	c, l, _, _ := setup(t)
	ctx := context.Background()
	if _, err := c.FollowedCommunities(ctx); err != nil {
		t.Fatal(err)
	}
	c.Reset()
	if _, err := c.FollowedCommunities(ctx); err != nil {
		t.Fatal(err)
	}
	if l.logins.Load() != 2 {
		t.Fatalf("logins = %d, want a fresh login after Reset", l.logins.Load())
	}
}

func TestNoAccount(t *testing.T) {
	c := New(&fakeStore{}, nil)
	if _, err := c.FollowedCommunities(context.Background()); !errors.Is(err, lemmyaccount.ErrNotConfigured) {
		t.Fatalf("got %v", err)
	}
}

func TestCommunityID(t *testing.T) {
	c, _, _, _ := setup(t)
	ctx := context.Background()
	id, err := c.CommunityID(ctx, "rust@other.example")
	if err != nil || id != 7 {
		t.Fatalf("id=%d err=%v", id, err)
	}
	if _, err := c.CommunityID(ctx, "missing"); !errors.Is(err, ErrCommunityNotFound) {
		t.Fatalf("got %v, want ErrCommunityNotFound", err)
	}
}

func TestV3AccountAndCommunities(t *testing.T) {
	var logins, siteCalls, communityCalls atomic.Int32
	var rejectToken atomic.Bool
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/v3/site", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") == "" {
			_, _ = w.Write([]byte(`{"version":"0.19.12"}`))
			return
		}
		siteCalls.Add(1)
		if rejectToken.Swap(false) {
			// v3's optional authentication may return a site without my_user.
			_, _ = w.Write([]byte(`{"version":"0.19.12"}`))
			return
		}
		if !strings.HasPrefix(r.Header.Get("Authorization"), "Bearer jwt-") {
			t.Errorf("missing bearer token on v3 site")
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		_, _ = w.Write([]byte(`{"version":"0.19.12","my_user":{"follows":[{"community":{"id":7,"name":"rust","title":"Rust","actor_id":"https://other.example/c/rust"}}]}}`))
	})
	mux.HandleFunc("POST /api/v3/user/login", func(w http.ResponseWriter, r *http.Request) {
		var input struct {
			Username string `json:"username_or_email"`
			Password string `json:"password"`
		}
		if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
			t.Errorf("decode login: %v", err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		if input.Username != "me" || input.Password != secretPassword {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"error":"incorrect_login"}`))
			return
		}
		logins.Add(1)
		_, _ = w.Write([]byte(`{"jwt":"jwt-test"}`))
	})
	mux.HandleFunc("GET /api/v3/community", func(w http.ResponseWriter, r *http.Request) {
		communityCalls.Add(1)
		if r.Header.Get("Authorization") != "Bearer jwt-test" || r.URL.Query().Get("name") != "rust@other.example" {
			t.Errorf("unexpected v3 community request")
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		_, _ = w.Write([]byte(`{"community_view":{"community":{"id":7}}}`))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	account := lemmyaccount.Account{InstanceURL: srv.URL, Username: "me", Password: secretPassword}
	store := &fakeStore{status: &lemmyaccount.Status{Account: account}}
	client := New(store, nil)
	ctx := context.Background()
	if err := client.Validate(ctx, account); err != nil {
		t.Fatalf("v3 validation: %v", err)
	}
	wrong := account
	wrong.Password = "wrong"
	if err := client.Validate(ctx, wrong); !errors.Is(err, ErrBadCredentials) {
		t.Fatalf("v3 wrong password: %v", err)
	}
	got, err := client.FollowedCommunities(ctx)
	if err != nil || len(got) != 1 || got[0] != (Community{ID: 7, Name: "rust@other.example", Title: "Rust"}) {
		t.Fatalf("v3 follows: %+v, %v", got, err)
	}
	id, err := client.CommunityID(ctx, "rust@other.example")
	if err != nil || id != 7 || communityCalls.Load() != 1 {
		t.Fatalf("v3 community id: %d, %v", id, err)
	}
	rejectToken.Store(true)
	if _, err := client.FollowedCommunities(ctx); err != nil {
		t.Fatalf("v3 re-login: %v", err)
	}
	if logins.Load() != 3 || siteCalls.Load() != 3 {
		t.Fatalf("logins=%d authenticated site calls=%d", logins.Load(), siteCalls.Load())
	}
	if !store.lastResult(t).OK {
		t.Fatal("v3 re-login result was not recorded")
	}
}

func TestVersionDetectionDoesNotFallbackOnServerFailure(t *testing.T) {
	var v3Calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/api/v3/") {
			v3Calls.Add(1)
		}
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer srv.Close()
	err := New(&fakeStore{}, nil).Validate(context.Background(), lemmyaccount.Account{InstanceURL: srv.URL})
	if !errors.Is(err, ErrUnreachable) || v3Calls.Load() != 0 {
		t.Fatalf("error=%v, v3 calls=%d", err, v3Calls.Load())
	}
}

func TestV3UnsupportedVersionDoesNotSendCredentials(t *testing.T) {
	var loginCalls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v3/site":
			_, _ = w.Write([]byte(`{"version":"0.18.5"}`))
		case "/api/v3/user/login":
			loginCalls.Add(1)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	err := New(&fakeStore{}, nil).Validate(context.Background(), lemmyaccount.Account{InstanceURL: srv.URL, Password: secretPassword})
	if !errors.Is(err, ErrUnsupportedVersion) || loginCalls.Load() != 0 {
		t.Fatalf("error=%v, login calls=%d", err, loginCalls.Load())
	}
}

func TestV3SiteMissingUserDoesNotReturnEmptySuggestions(t *testing.T) {
	var logins, sites atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v3/site":
			if r.Header.Get("Authorization") != "" {
				sites.Add(1)
			}
			_, _ = w.Write([]byte(`{"version":"0.19.12"}`))
		case "/api/v3/user/login":
			logins.Add(1)
			_, _ = w.Write([]byte(`{"jwt":"jwt-test"}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	store := &fakeStore{status: &lemmyaccount.Status{Account: lemmyaccount.Account{InstanceURL: srv.URL}}}
	_, err := New(store, nil).FollowedCommunities(context.Background())
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.Status != http.StatusUnauthorized || logins.Load() != 2 || sites.Load() != 2 {
		t.Fatalf("error=%v, logins=%d authenticated sites=%d", err, logins.Load(), sites.Load())
	}
}

func TestAccountChangeRedetectsVersion(t *testing.T) {
	client, _, store, _ := setup(t)
	ctx := context.Background()
	if _, err := client.FollowedCommunities(ctx); err != nil {
		t.Fatal(err)
	}
	var v3Logins atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v3/site":
			if r.Header.Get("Authorization") == "" {
				_, _ = w.Write([]byte(`{"version":"0.19.12"}`))
			} else if r.Header.Get("Authorization") == "Bearer v3-token" {
				_, _ = w.Write([]byte(`{"version":"0.19.12","my_user":{"follows":[]}}`))
			} else {
				t.Error("old token sent to new account")
				w.WriteHeader(http.StatusUnauthorized)
			}
		case "/api/v3/user/login":
			v3Logins.Add(1)
			_, _ = w.Write([]byte(`{"jwt":"v3-token"}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	store.mu.Lock()
	store.status.Account = lemmyaccount.Account{InstanceURL: srv.URL, Username: "new", Password: "new"}
	store.mu.Unlock()
	client.Reset()
	if _, err := client.FollowedCommunities(ctx); err != nil || v3Logins.Load() != 1 {
		t.Fatalf("account change: %v, v3 logins=%d", err, v3Logins.Load())
	}
}

func TestErrorsNeverContainSecrets(t *testing.T) {
	c, l, _, account := setup(t)
	l.loginStatus = http.StatusBadRequest
	l.loginBody = `{"error":"` + secretPassword + `"}` // hostile or buggy server echoing a secret
	err := c.Validate(context.Background(), account)
	if err == nil || strings.Contains(err.Error(), secretPassword) {
		t.Fatalf("error %v leaks the password", err)
	}
	l.loginBody = `{"error":"password"}`
	if err := c.Validate(context.Background(), account); err == nil || strings.Contains(err.Error(), "password") {
		t.Fatalf("error %v leaks an error code that could be a secret", err)
	}
}

func TestUnimplementedEndpoints(t *testing.T) {
	c := New(&fakeStore{}, nil)
	if _, err := c.CreatePost(context.Background(), NewPost{}); !errors.Is(err, ErrNotImplemented) {
		t.Fatal("CreatePost should be unimplemented")
	}
	if _, err := c.UploadImage(context.Background(), "image/png", nil); !errors.Is(err, ErrNotImplemented) {
		t.Fatal("UploadImage should be unimplemented")
	}
}
