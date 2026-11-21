package api

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	"connectrpc.com/connect"
	lemmyschedulerv1 "github.com/gumert/LemmyScheduler/backend/internal/gen/lemmyscheduler/v1"
	"github.com/gumert/LemmyScheduler/backend/internal/gen/lemmyscheduler/v1/lemmyschedulerv1connect"
	"github.com/gumert/LemmyScheduler/backend/internal/lemmyaccount"
	"github.com/gumert/LemmyScheduler/backend/internal/lemmyclient"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// communityCacheTTL keeps the form from hitting the instance on every page load.
const communityCacheTTL = 5 * time.Minute

// LemmyClient is the part of lemmyclient.Client the account service uses.
type LemmyClient interface {
	Validate(ctx context.Context, account lemmyaccount.Account) error
	FollowedCommunities(ctx context.Context) ([]lemmyclient.Community, error)
	Reset()
}

type LemmyAccountServer struct {
	store  lemmyaccount.Store
	client LemmyClient
	now    func() time.Time

	mu         sync.Mutex
	cache      []lemmyclient.Community
	cachedAt   time.Time
	cacheValid bool
	// cacheGen changes whenever the account does, so a fetch that started for
	// the old account cannot repopulate the cache afterwards.
	cacheGen uint64
}

var _ lemmyschedulerv1connect.LemmyAccountServiceHandler = (*LemmyAccountServer)(nil)

func NewLemmyAccountServer(store lemmyaccount.Store, client LemmyClient) *LemmyAccountServer {
	return &LemmyAccountServer{store: store, client: client, now: time.Now}
}

func (s *LemmyAccountServer) SetAccount(ctx context.Context, req *connect.Request[lemmyschedulerv1.SetAccountRequest]) (*connect.Response[lemmyschedulerv1.SetAccountResponse], error) {
	instanceURL, err := lemmyaccount.NormalizeInstanceURL(req.Msg.GetInstanceUrl())
	if err != nil {
		return nil, invalidArgument(err.Error())
	}
	username := strings.TrimSpace(req.Msg.GetUsername())
	if username == "" {
		return nil, invalidArgument("username is required")
	}
	if req.Msg.GetPassword() == "" {
		return nil, invalidArgument("password is required")
	}
	account := lemmyaccount.Account{InstanceURL: instanceURL, Username: username, Password: req.Msg.GetPassword()}

	if err := s.client.Validate(ctx, account); err != nil {
		return nil, lemmyError(err)
	}
	result := lemmyaccount.LoginResult{OK: true, At: s.now().UTC()}
	if err := s.store.Save(ctx, account, result); err != nil {
		log.Printf("lemmy account: save: %v", err)
		return nil, connect.NewError(connect.CodeInternal, errors.New("could not save the account"))
	}
	s.client.Reset()
	s.resetCache()
	return connect.NewResponse(&lemmyschedulerv1.SetAccountResponse{
		Status: accountStatusToProto(lemmyaccount.Status{Account: account, LastLogin: result}),
	}), nil
}

func (s *LemmyAccountServer) GetAccountStatus(ctx context.Context, _ *connect.Request[lemmyschedulerv1.GetAccountStatusRequest]) (*connect.Response[lemmyschedulerv1.GetAccountStatusResponse], error) {
	status, err := s.store.Get(ctx)
	if errors.Is(err, lemmyaccount.ErrNotConfigured) {
		return connect.NewResponse(&lemmyschedulerv1.GetAccountStatusResponse{Status: &lemmyschedulerv1.AccountStatus{}}), nil
	}
	if err != nil {
		log.Printf("lemmy account: get: %v", err)
		return nil, connect.NewError(connect.CodeInternal, errors.New("could not read the account"))
	}
	return connect.NewResponse(&lemmyschedulerv1.GetAccountStatusResponse{Status: accountStatusToProto(status)}), nil
}

func (s *LemmyAccountServer) ListCommunities(ctx context.Context, _ *connect.Request[lemmyschedulerv1.ListCommunitiesRequest]) (*connect.Response[lemmyschedulerv1.ListCommunitiesResponse], error) {
	communities, err := s.communities(ctx)
	if err != nil {
		return nil, lemmyError(err)
	}
	out := make([]*lemmyschedulerv1.Community, 0, len(communities))
	for _, c := range communities {
		out = append(out, &lemmyschedulerv1.Community{Name: c.Name, Title: c.Title})
	}
	return connect.NewResponse(&lemmyschedulerv1.ListCommunitiesResponse{Communities: out}), nil
}

func (s *LemmyAccountServer) communities(ctx context.Context) ([]lemmyclient.Community, error) {
	s.mu.Lock()
	if s.cacheValid && s.now().Sub(s.cachedAt) < communityCacheTTL {
		cached := s.cache
		s.mu.Unlock()
		return cached, nil
	}
	gen := s.cacheGen
	s.mu.Unlock()

	fetched, err := s.client.FollowedCommunities(ctx)
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	if s.cacheGen == gen {
		s.cache, s.cachedAt, s.cacheValid = fetched, s.now(), true
	}
	s.mu.Unlock()
	return fetched, nil
}

func (s *LemmyAccountServer) resetCache() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cache, s.cacheValid = nil, false
	s.cacheGen++
}

// accountStatusToProto deliberately copies only the fields that are safe to return.
func accountStatusToProto(status lemmyaccount.Status) *lemmyschedulerv1.AccountStatus {
	return &lemmyschedulerv1.AccountStatus{
		Configured:     true,
		InstanceUrl:    status.Account.InstanceURL,
		Username:       status.Account.Username,
		LastLoginOk:    status.LastLogin.OK,
		LastLoginError: status.LastLogin.Error,
		LastLoginAt:    timestamppb.New(status.LastLogin.At),
	}
}

// lemmyError maps client errors to Connect errors with user-facing messages.
// Messages are fixed text, never the raw error, so nothing from a response body
// can reach the browser.
func lemmyError(err error) error {
	var rateLimited *lemmyclient.RateLimitedError
	switch {
	case errors.Is(err, lemmyaccount.ErrNotConfigured):
		return connect.NewError(connect.CodeNotFound, errors.New("No Lemmy account is configured."))
	case errors.Is(err, lemmyclient.ErrBadCredentials):
		return connect.NewError(connect.CodeUnauthenticated, errors.New("Lemmy rejected the username or password."))
	case errors.Is(err, lemmyclient.ErrTwoFactorUnsupported):
		return connect.NewError(connect.CodeFailedPrecondition, errors.New("This account uses two-factor authentication, which isn't supported. Use an account without it."))
	case errors.Is(err, lemmyclient.ErrUnsupportedVersion):
		return connect.NewError(connect.CodeFailedPrecondition, errors.New("This instance needs Lemmy 0.19 or newer."))
	case errors.Is(err, lemmyclient.ErrAccountUnusable):
		return connect.NewError(connect.CodeFailedPrecondition, errors.New("The instance refused the login. The account may be unverified, banned or awaiting approval."))
	case errors.Is(err, lemmyclient.ErrInvalidResponse):
		return connect.NewError(connect.CodeFailedPrecondition, errors.New("That address didn't answer like a Lemmy instance."))
	case errors.As(err, &rateLimited):
		message := "The instance is rate limiting requests. Try again later."
		if rateLimited.RetryAfter > 0 {
			message = fmt.Sprintf("The instance is rate limiting requests. Try again in %d seconds.", int(rateLimited.RetryAfter.Seconds()))
		}
		return connect.NewError(connect.CodeResourceExhausted, errors.New(message))
	case errors.Is(err, lemmyclient.ErrUnreachable):
		return connect.NewError(connect.CodeUnavailable, errors.New("The instance is unreachable."))
	case errors.Is(err, context.DeadlineExceeded), errors.Is(err, context.Canceled):
		return connect.NewError(connect.CodeDeadlineExceeded, errors.New("The request timed out."))
	}
	log.Printf("lemmy account: %v", err)
	return connect.NewError(connect.CodeInternal, errors.New("The Lemmy request failed."))
}
