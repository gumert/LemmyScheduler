package api

import (
	"bytes"
	"context"
	"errors"
	"log"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"
	lemmyschedulerv1 "github.com/gumert/LemmyScheduler/backend/internal/gen/lemmyscheduler/v1"
	"github.com/gumert/LemmyScheduler/backend/internal/lemmyaccount"
	"github.com/gumert/LemmyScheduler/backend/internal/lemmyclient"
	"google.golang.org/protobuf/proto"
)

type fakeAccountStore struct {
	status *lemmyaccount.Status
	saves  int
}

func (s *fakeAccountStore) Get(context.Context) (lemmyaccount.Status, error) {
	if s.status == nil {
		return lemmyaccount.Status{}, lemmyaccount.ErrNotConfigured
	}
	return *s.status, nil
}

func (s *fakeAccountStore) Save(_ context.Context, a lemmyaccount.Account, r lemmyaccount.LoginResult) error {
	s.saves++
	s.status = &lemmyaccount.Status{Account: a, LastLogin: r}
	return nil
}

func (s *fakeAccountStore) RecordLogin(context.Context, int64, lemmyaccount.LoginResult) error {
	return nil
}
func (s *fakeAccountStore) SetQueuePausedUntil(context.Context, time.Time) error { return nil }
func (s *fakeAccountStore) ClearQueuePausedUntil(context.Context) error          { return nil }

type fakeLemmy struct {
	validateErr error
	listErr     error
	validated   []lemmyaccount.Account
	lists       int
	resets      int
	communities []lemmyclient.Community
	// onList runs inside FollowedCommunities, before it returns.
	onList func()
}

func (f *fakeLemmy) Validate(_ context.Context, a lemmyaccount.Account) error {
	f.validated = append(f.validated, a)
	return f.validateErr
}

func (f *fakeLemmy) FollowedCommunities(context.Context) ([]lemmyclient.Community, error) {
	f.lists++
	if f.onList != nil {
		f.onList()
	}
	return f.communities, f.listErr
}

func (f *fakeLemmy) Reset() { f.resets++ }

const secret = "s3cret-password"

func setReq(url, user, password string) *connect.Request[lemmyschedulerv1.SetAccountRequest] {
	return connect.NewRequest(&lemmyschedulerv1.SetAccountRequest{InstanceUrl: url, Username: user, Password: password})
}

func newAccountServer(store *fakeAccountStore, client *fakeLemmy) *LemmyAccountServer {
	s := NewLemmyAccountServer(store, client)
	s.now = func() time.Time { return time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC) }
	return s
}

func code(err error) connect.Code { return connect.CodeOf(err) }

func TestSetAccountSavesAfterValidation(t *testing.T) {
	store, client := &fakeAccountStore{}, &fakeLemmy{}
	s := newAccountServer(store, client)
	res, err := s.SetAccount(context.Background(), setReq(" https://lemmy.test/ ", " me ", secret))
	if err != nil {
		t.Fatal(err)
	}
	if got := store.status.Account; got != (lemmyaccount.Account{InstanceURL: "https://lemmy.test", Username: "me", Password: secret}) {
		t.Fatalf("stored %+v", got)
	}
	if !store.status.LastLogin.OK {
		t.Fatal("last login should be ok")
	}
	if !res.Msg.GetStatus().GetConfigured() || res.Msg.GetStatus().GetUsername() != "me" {
		t.Fatalf("status = %v", res.Msg.GetStatus())
	}
}

// SAFETY TEST: a failed validation never overwrites the saved account.
func TestSetAccountFailureLeavesRowUntouched(t *testing.T) {
	old := lemmyaccount.Status{Account: lemmyaccount.Account{InstanceURL: "https://old.test", Username: "old", Password: "oldpw"}}
	cases := map[string]struct {
		err  error
		want connect.Code
	}{
		"bad credentials": {lemmyclient.ErrBadCredentials, connect.CodeUnauthenticated},
		"two factor":      {lemmyclient.ErrTwoFactorUnsupported, connect.CodeFailedPrecondition},
		"old version":     {lemmyclient.ErrUnsupportedVersion, connect.CodeFailedPrecondition},
		"rate limited":    {&lemmyclient.RateLimitedError{RetryAfter: 30 * time.Second}, connect.CodeResourceExhausted},
		"unreachable":     {lemmyclient.ErrUnreachable, connect.CodeUnavailable},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			saved := old
			store, client := &fakeAccountStore{status: &saved}, &fakeLemmy{validateErr: tc.err}
			_, err := newAccountServer(store, client).SetAccount(context.Background(), setReq("https://new.test", "new", secret))
			if code(err) != tc.want {
				t.Fatalf("code = %v (%v), want %v", code(err), err, tc.want)
			}
			if store.saves != 0 || store.status.Account != old.Account {
				t.Fatal("a failed SetAccount changed the stored account")
			}
			if client.resets != 0 {
				t.Fatal("a failed SetAccount reset the client")
			}
		})
	}
}

func TestSetAccountRequiresFields(t *testing.T) {
	s := newAccountServer(&fakeAccountStore{}, &fakeLemmy{})
	for name, req := range map[string]*connect.Request[lemmyschedulerv1.SetAccountRequest]{
		"no scheme":   setReq("lemmy.test", "me", secret),
		"path":        setReq("https://lemmy.test/x", "me", secret),
		"no username": setReq("https://lemmy.test", " ", secret),
		"no password": setReq("https://lemmy.test", "me", ""),
	} {
		if _, err := s.SetAccount(context.Background(), req); code(err) != connect.CodeInvalidArgument {
			t.Errorf("%s: code = %v, want InvalidArgument", name, code(err))
		}
	}
}

func TestSuccessfulSetAccountResetsTokenAndCommunityCache(t *testing.T) {
	store := &fakeAccountStore{status: &lemmyaccount.Status{Account: lemmyaccount.Account{InstanceURL: "https://a.test", Username: "a", Password: "x"}}}
	client := &fakeLemmy{communities: []lemmyclient.Community{{Name: "a@a.test"}}}
	s := newAccountServer(store, client)
	ctx := context.Background()
	list := func() {
		t.Helper()
		if _, err := s.ListCommunities(ctx, connect.NewRequest(&lemmyschedulerv1.ListCommunitiesRequest{})); err != nil {
			t.Fatal(err)
		}
	}
	list()
	list()
	if client.lists != 1 {
		t.Fatalf("lists = %d, want the second call served from cache", client.lists)
	}
	if _, err := s.SetAccount(ctx, setReq("https://b.test", "b", secret)); err != nil {
		t.Fatal(err)
	}
	if client.resets != 1 {
		t.Fatalf("resets = %d, want the token dropped", client.resets)
	}
	list()
	if client.lists != 2 {
		t.Fatalf("lists = %d, want a fresh fetch after the account changed", client.lists)
	}
}

func TestStaleFetchDoesNotRepopulateCache(t *testing.T) {
	client := &fakeLemmy{communities: []lemmyclient.Community{{Name: "old@a.test"}}}
	s := newAccountServer(&fakeAccountStore{}, client)
	client.onList = func() {
		// The account changes while the old account's fetch is in flight.
		client.onList = nil
		s.resetCache()
	}
	if _, err := s.communities(context.Background()); err != nil {
		t.Fatal(err)
	}
	if s.cacheValid {
		t.Fatal("a fetch for the previous account populated the cache")
	}
}

func TestCacheExpires(t *testing.T) {
	client := &fakeLemmy{}
	s := newAccountServer(&fakeAccountStore{}, client)
	now := time.Now()
	s.now = func() time.Time { return now }
	_, _ = s.communities(context.Background())
	now = now.Add(communityCacheTTL + time.Second)
	_, _ = s.communities(context.Background())
	if client.lists != 2 {
		t.Fatalf("lists = %d, want a refetch after the TTL", client.lists)
	}
}

func TestListCommunitiesErrors(t *testing.T) {
	list := func(store *fakeAccountStore, client *fakeLemmy) error {
		_, err := newAccountServer(store, client).ListCommunities(context.Background(), connect.NewRequest(&lemmyschedulerv1.ListCommunitiesRequest{}))
		return err
	}
	if err := list(&fakeAccountStore{}, &fakeLemmy{listErr: lemmyaccount.ErrNotConfigured}); code(err) != connect.CodeNotFound {
		t.Errorf("no account: code = %v, want NotFound", code(err))
	}
	if err := list(&fakeAccountStore{}, &fakeLemmy{listErr: lemmyclient.ErrUnreachable}); code(err) != connect.CodeUnavailable {
		t.Errorf("unreachable: code = %v, want Unavailable", code(err))
	}
}

func TestGetAccountStatus(t *testing.T) {
	ctx := context.Background()
	empty, err := newAccountServer(&fakeAccountStore{}, &fakeLemmy{}).GetAccountStatus(ctx, connect.NewRequest(&lemmyschedulerv1.GetAccountStatusRequest{}))
	if err != nil || empty.Msg.GetStatus().GetConfigured() {
		t.Fatalf("empty: %v %v", empty, err)
	}
	store := &fakeAccountStore{status: &lemmyaccount.Status{
		Account:   lemmyaccount.Account{InstanceURL: "https://a.test", Username: "a", Password: secret},
		LastLogin: lemmyaccount.LoginResult{OK: false, Error: "Lemmy rejected the username or password", At: time.Unix(10, 0)},
	}}
	res, err := newAccountServer(store, &fakeLemmy{}).GetAccountStatus(ctx, connect.NewRequest(&lemmyschedulerv1.GetAccountStatusRequest{}))
	if err != nil {
		t.Fatal(err)
	}
	st := res.Msg.GetStatus()
	if !st.GetConfigured() || st.GetUsername() != "a" || st.GetLastLoginOk() || st.GetLastLoginError() == "" || st.GetLastLoginAt() == nil {
		t.Fatalf("status = %v", st)
	}
}

// SAFETY TEST: the password must not appear in any response, error or log line.
func TestPasswordNeverLeaks(t *testing.T) {
	var logs bytes.Buffer
	log.SetOutput(&logs)
	defer log.SetOutput(nil)

	ctx := context.Background()
	store := &fakeAccountStore{}
	client := &fakeLemmy{}
	s := newAccountServer(store, client)

	var outputs []string
	collect := func(m proto.Message, err error) {
		if m != nil {
			b, _ := proto.MarshalOptions{}.Marshal(m)
			outputs = append(outputs, string(b), m.(interface{ String() string }).String())
		}
		if err != nil {
			outputs = append(outputs, err.Error())
		}
	}
	res, err := s.SetAccount(ctx, setReq("https://a.test", "me", secret))
	collect(res.Msg, err)
	status, err := s.GetAccountStatus(ctx, connect.NewRequest(&lemmyschedulerv1.GetAccountStatusRequest{}))
	collect(status.Msg, err)

	for _, failure := range []error{
		lemmyclient.ErrBadCredentials, lemmyclient.ErrUnreachable,
		errors.New("boom"),
	} {
		client.validateErr = failure
		_, err = s.SetAccount(ctx, setReq("https://a.test", "me", secret))
		collect(nil, err)
	}
	for _, o := range append(outputs, logs.String()) {
		if strings.Contains(o, secret) {
			t.Fatalf("password leaked into %q", o)
		}
	}
}
