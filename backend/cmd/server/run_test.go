package main

import (
	"context"
	"errors"
	"net"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/gumert/LemmyScheduler/backend/internal/lemmyaccount"
	"github.com/gumert/LemmyScheduler/backend/internal/scheduledposts"
)

const testDatabaseURL = "postgres://fake@127.0.0.1:1/fake"

// testEnv is a getenv with DATABASE_URL set, the only required variable.
func testEnv(key string) string {
	if key == "DATABASE_URL" {
		return testDatabaseURL
	}
	return ""
}

// fakeStore records that it was closed.
type fakeStore struct{ f *fakeDeps }

func (s fakeStore) Close() {
	s.f.calls = append(s.f.calls, "close")
	if s.f.onClose != nil {
		s.f.onClose()
	}
}

func (fakeStore) ScheduledPosts() scheduledposts.Store { return nil }
func (fakeStore) Images() scheduledposts.ImageStore    { return nil }
func (fakeStore) LemmyAccount() lemmyaccount.Store     { return nil }
func (s fakeStore) AcquirePublisher(context.Context) (publisher, error) {
	s.f.calls = append(s.f.calls, "publisher")
	if s.f.acquireErr != nil {
		return nil, s.f.acquireErr
	}
	return fakePublisher{f: s.f, lost: make(chan error)}, nil
}

type fakePublisher struct {
	f    *fakeDeps
	lost chan error
}

func (p fakePublisher) ClaimDue(context.Context, time.Time) (scheduledposts.ClaimedPost, bool, error) {
	return scheduledposts.ClaimedPost{}, false, nil
}
func (p fakePublisher) FinalizeAttempt(context.Context, scheduledposts.FinalizeAttempt) error {
	return nil
}
func (p fakePublisher) RecoverInterrupted(context.Context, time.Time) ([]int64, error) {
	p.f.calls = append(p.f.calls, "recover")
	return nil, nil
}
func (p fakePublisher) Check(context.Context) error {
	if p.f.checkErr != nil {
		p.lost <- p.f.checkErr
		close(p.lost)
		return p.f.checkErr
	}
	return nil
}
func (p fakePublisher) Lost() <-chan error { return p.lost }
func (p fakePublisher) Close() error {
	p.f.calls = append(p.f.calls, "publisher-close")
	return nil
}

// fakeDeps records the order of calls and the URLs they received. run calls
// every method on its own goroutine, so tests read the fields only after run
// has returned.
type fakeDeps struct {
	calls      []string
	migrateURL string
	openURL    string
	migrateErr error
	openErr    error
	acquireErr error
	checkErr   error
	onMigrate  func()
	onOpen     func()
	onClose    func()
}

func (f *fakeDeps) deps() deps {
	return deps{
		migrate: func(_ context.Context, url string) error {
			f.calls = append(f.calls, "migrate")
			f.migrateURL = url
			if f.onMigrate != nil {
				f.onMigrate()
			}
			return f.migrateErr
		},
		open: func(_ context.Context, url string) (store, error) {
			f.calls = append(f.calls, "open")
			f.openURL = url
			if f.onOpen != nil {
				f.onOpen()
			}
			if f.openErr != nil {
				return nil, f.openErr
			}
			return fakeStore{f}, nil
		},
	}
}

// freeAddr returns a loopback address that nothing is listening on.
func freeAddr(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserve port: %v", err)
	}
	addr := ln.Addr().String()
	if err := ln.Close(); err != nil {
		t.Fatalf("release port: %v", err)
	}
	return addr
}

// listening reports whether something accepts connections on addr.
func listening(addr string) bool {
	conn, err := net.DialTimeout("tcp", addr, time.Second)
	if err != nil {
		return false
	}
	conn.Close()
	return true
}

// waitListening blocks until addr accepts connections.
func waitListening(t *testing.T, addr string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !listening(addr) {
		if time.Now().After(deadline) {
			t.Fatalf("nothing listening on %s after 5s", addr)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// startRun runs run on a goroutine and returns its result channel plus a
// cancel func.
func startRun(addr string, getenv func(string) string, d deps) (<-chan error, context.CancelFunc) {
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- run(ctx, addr, getenv, d) }()
	return done, cancel
}

func waitDone(t *testing.T, done <-chan error) error {
	t.Helper()
	select {
	case err := <-done:
		return err
	case <-time.After(5 * time.Second):
		t.Fatal("run() did not return")
		return nil
	}
}

func TestRunRequiresDatabaseURL(t *testing.T) {
	f := &fakeDeps{}

	err := run(context.Background(), freeAddr(t), noEnv, f.deps())

	if err == nil || !strings.Contains(err.Error(), "DATABASE_URL") {
		t.Fatalf("run() = %v, want an error naming DATABASE_URL", err)
	}
	if len(f.calls) != 0 {
		t.Fatalf("calls = %v, want none", f.calls)
	}
}

func TestRunPassesDatabaseURLToMigrateAndOpen(t *testing.T) {
	f := &fakeDeps{}
	done, cancel := startRun(freeAddr(t), testEnv, f.deps())
	cancel()
	if err := waitDone(t, done); err != nil {
		t.Fatalf("run() = %v, want nil", err)
	}

	if f.migrateURL != testDatabaseURL || f.openURL != testDatabaseURL {
		t.Fatalf("migrate got %q, open got %q, want %q for both", f.migrateURL, f.openURL, testDatabaseURL)
	}
}

// The listener must not exist while migrate and open run, and must exist once
// open has returned.
func TestRunMigratesThenOpensThenListens(t *testing.T) {
	addr := freeAddr(t)
	var listeningDuringMigrate, listeningDuringOpen bool
	f := &fakeDeps{
		onMigrate: func() { listeningDuringMigrate = listening(addr) },
		onOpen:    func() { listeningDuringOpen = listening(addr) },
	}
	done, cancel := startRun(addr, testEnv, f.deps())
	waitListening(t, addr)
	cancel()
	if err := waitDone(t, done); err != nil {
		t.Fatalf("run() = %v, want nil", err)
	}

	if want := []string{"migrate", "open", "publisher", "recover", "publisher-close", "close"}; !slices.Equal(f.calls, want) {
		t.Fatalf("calls = %v, want %v", f.calls, want)
	}
	if listeningDuringMigrate || listeningDuringOpen {
		t.Fatalf("server was listening before startup finished (migrate=%t, open=%t)", listeningDuringMigrate, listeningDuringOpen)
	}
}

func TestRunMigrateErrorAbortsStartup(t *testing.T) {
	addr := freeAddr(t)
	cause := errors.New("dirty database version 1")
	f := &fakeDeps{migrateErr: cause}

	err := run(context.Background(), addr, testEnv, f.deps())

	if !errors.Is(err, cause) {
		t.Fatalf("run() = %v, want it to wrap %v", err, cause)
	}
	if want := []string{"migrate"}; !slices.Equal(f.calls, want) {
		t.Fatalf("calls = %v, want %v (open must not run)", f.calls, want)
	}
	if listening(addr) {
		t.Fatal("server is listening after a migrate error")
	}
}

func TestRunOpenErrorAbortsStartup(t *testing.T) {
	addr := freeAddr(t)
	cause := errors.New("connection refused")
	f := &fakeDeps{openErr: cause}

	err := run(context.Background(), addr, testEnv, f.deps())

	if !errors.Is(err, cause) {
		t.Fatalf("run() = %v, want it to wrap %v", err, cause)
	}
	if want := []string{"migrate", "open"}; !slices.Equal(f.calls, want) {
		t.Fatalf("calls = %v, want %v", f.calls, want)
	}
	if listening(addr) {
		t.Fatal("server is listening after an open error")
	}
}

// Close runs once, after the listener has been shut down.
func TestRunClosesStoreAfterServerShutsDown(t *testing.T) {
	addr := freeAddr(t)
	var listeningAtClose bool
	f := &fakeDeps{onClose: func() { listeningAtClose = listening(addr) }}
	done, cancel := startRun(addr, testEnv, f.deps())
	waitListening(t, addr)
	cancel()
	if err := waitDone(t, done); err != nil {
		t.Fatalf("run() = %v, want nil", err)
	}

	if want := []string{"migrate", "open", "publisher", "recover", "publisher-close", "close"}; !slices.Equal(f.calls, want) {
		t.Fatalf("calls = %v, want %v", f.calls, want)
	}
	if listeningAtClose {
		t.Fatal("store was closed while the server was still listening")
	}
}

func TestRunClosesStoreWhenServerFailsToStart(t *testing.T) {
	// Occupy the port so ListenAndServe fails.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer ln.Close()
	f := &fakeDeps{}

	err = run(context.Background(), ln.Addr().String(), testEnv, f.deps())

	if err == nil {
		t.Fatal("run() = nil, want a listen error")
	}
	if want := []string{"migrate", "open", "publisher", "recover", "publisher-close", "close"}; !slices.Equal(f.calls, want) {
		t.Fatalf("calls = %v, want %v", f.calls, want)
	}
}

// SAFETY TEST: startup cannot recover or serve when publisher ownership is unavailable.
func TestRunOwnershipFailureAbortsBeforeRecovery(t *testing.T) {
	addr := freeAddr(t)
	cause := errors.New("publisher already active")
	f := &fakeDeps{acquireErr: cause}
	err := run(context.Background(), addr, testEnv, f.deps())
	if !errors.Is(err, cause) {
		t.Fatalf("run() = %v, want ownership error", err)
	}
	if want := []string{"migrate", "open", "publisher", "close"}; !slices.Equal(f.calls, want) {
		t.Fatalf("calls = %v, want %v", f.calls, want)
	}
	if listening(addr) {
		t.Fatal("server listened without publisher ownership")
	}
}

// SAFETY TEST: losing the publisher session shuts down the server and returns a fatal error.
func TestRunStopsWhenPublisherOwnershipIsLost(t *testing.T) {
	addr := freeAddr(t)
	cause := errors.New("publisher connection was lost")
	f := &fakeDeps{checkErr: cause}
	done, cancel := startRun(addr, testEnv, f.deps())
	defer cancel()
	waitListening(t, addr)
	err := waitDone(t, done)
	if !errors.Is(err, cause) {
		t.Fatalf("run() = %v, want wrapped ownership error", err)
	}
	if listening(addr) {
		t.Fatal("server kept listening after publisher ownership loss")
	}
	if want := []string{"migrate", "open", "publisher", "recover", "publisher-close", "close"}; !slices.Equal(f.calls, want) {
		t.Fatalf("calls = %v, want %v", f.calls, want)
	}
}
