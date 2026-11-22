package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/gumert/LemmyScheduler/backend/internal/api"
	"github.com/gumert/LemmyScheduler/backend/internal/db"
	"github.com/gumert/LemmyScheduler/backend/internal/gen/lemmyscheduler/v1/lemmyschedulerv1connect"
	"github.com/gumert/LemmyScheduler/backend/internal/lemmyaccount"
	"github.com/gumert/LemmyScheduler/backend/internal/lemmyclient"
	"github.com/gumert/LemmyScheduler/backend/internal/scheduledposts"
)

// store is the part of the database connection the server manages. It is an
// interface so tests can substitute a fake.
type store interface {
	Close()
	AcquirePublisher(context.Context) (publisher, error)
	ScheduledPosts() scheduledposts.Store
	Images() scheduledposts.ImageStore
	LemmyAccount() lemmyaccount.Store
}

type publisher interface {
	scheduledposts.SchedulerStore
	RecoverInterrupted(context.Context, time.Time) ([]int64, error)
	Check(context.Context) error
	Lost() <-chan error
	Close() error
}

type openedStore struct {
	close          func()
	pool           *pgxpool.Pool
	scheduledPosts scheduledposts.Store
	images         scheduledposts.ImageStore
	lemmyAccount   lemmyaccount.Store
}

func (s openedStore) Close() {
	s.close()
}

func (s openedStore) AcquirePublisher(ctx context.Context) (publisher, error) {
	return db.AcquirePublisher(ctx, s.pool)
}

func (s openedStore) ScheduledPosts() scheduledposts.Store {
	return s.scheduledPosts
}

func (s openedStore) Images() scheduledposts.ImageStore {
	return s.images
}

func (s openedStore) LemmyAccount() lemmyaccount.Store {
	return s.lemmyAccount
}

// deps are the database operations run needs, injected so tests can fake them.
type deps struct {
	// migrate brings the schema up to date.
	migrate func(ctx context.Context, databaseURL string) error
	// open connects to the database and verifies the connection.
	open func(ctx context.Context, databaseURL string) (store, error)
}

// realDeps wires in the real golang-migrate and pgx implementations.
func realDeps() deps {
	return deps{
		migrate: db.Migrate,
		open: func(ctx context.Context, databaseURL string) (store, error) {
			pool, err := db.Open(ctx, databaseURL)
			if err != nil {
				// Return an untyped nil, not a nil *pgxpool.Pool in a non-nil interface.
				return nil, err
			}
			return openedStore{close: pool.Close, pool: pool, scheduledPosts: db.NewScheduledPostStore(pool), images: db.NewImageStore(pool), lemmyAccount: db.NewLemmyAccountStore(pool)}, nil
		},
	}
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	err := run(ctx, listenAddr(os.Getenv), os.Getenv, realDeps())
	stop()
	if err != nil {
		log.Fatal(err)
	}
}

// databaseURL returns the required DATABASE_URL. There is deliberately no
// default: a silent fallback to localhost could point a deployment at the
// wrong database.
func databaseURL(getenv func(string) string) (string, error) {
	dsn := getenv("DATABASE_URL")
	if dsn == "" {
		return "", errors.New("DATABASE_URL is not set")
	}
	return dsn, nil
}

// run migrates the database, connects to it, then serves HTTP on addr until
// ctx is cancelled, then drains in-flight requests and closes the database
// before returning. The server only starts listening after migrations succeed,
// so a passing readiness probe implies the schema is current. It returns nil
// on a clean shutdown.
func run(ctx context.Context, addr string, getenv func(string) string, d deps) (runErr error) {
	dsn, err := databaseURL(getenv)
	if err != nil {
		return err
	}
	if err := d.migrate(ctx, dsn); err != nil {
		return fmt.Errorf("migrate database: %w", err)
	}
	st, err := d.open(ctx, dsn)
	if err != nil {
		return fmt.Errorf("open database: %w", err)
	}
	// Deferred so the store closes after srv.Shutdown has drained requests, and
	// also when the server fails to start.
	defer st.Close()

	owner, err := st.AcquirePublisher(ctx)
	if err != nil {
		return fmt.Errorf("acquire publisher ownership: %w", err)
	}
	defer func() {
		if err := owner.Close(); err != nil {
			if runErr == nil {
				runErr = fmt.Errorf("release publisher ownership: %w", err)
			} else {
				log.Printf("release publisher ownership: %v", err)
			}
		}
	}()
	recovered, err := owner.RecoverInterrupted(ctx, time.Now().UTC())
	if err != nil {
		return fmt.Errorf("recover interrupted posts: %w", err)
	}
	if len(recovered) > 0 {
		log.Printf("recovered %d interrupted post(s), ids=%v", len(recovered), recovered)
	}

	srv := newServer(addr, getenv, st.ScheduledPosts(), st.Images(), st.LemmyAccount())

	errCh := make(chan error, 1)
	go func() {
		log.Printf("backend listening on %s", addr)
		errCh <- srv.ListenAndServe()
	}()

	monitorCtx, stopMonitor := context.WithCancel(context.Background())
	monitorDone := make(chan struct{})
	go func() {
		defer close(monitorDone)
		ticker := time.NewTicker(time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-monitorCtx.Done():
				return
			case <-ticker.C:
				checkCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
				err := owner.Check(checkCtx)
				cancel()
				if err != nil {
					return
				}
			}
		}
	}()
	defer func() {
		stopMonitor()
		<-monitorDone
	}()

	select {
	case err := <-errCh:
		return err
	case err := <-owner.Lost():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if shutdownErr := srv.Shutdown(shutdownCtx); shutdownErr != nil {
			stopMonitor()
			<-monitorDone
			return fmt.Errorf("publisher ownership lost (%v); server shutdown: %w", err, shutdownErr)
		}
		stopMonitor()
		<-monitorDone
		return fmt.Errorf("publisher ownership lost: %w", err)
	case <-ctx.Done():
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	shutdownErr := srv.Shutdown(shutdownCtx)
	stopMonitor()
	<-monitorDone
	select {
	case err := <-owner.Lost():
		return fmt.Errorf("publisher ownership lost during shutdown: %w", err)
	default:
	}
	return shutdownErr
}

// newServer builds the HTTP server with explicit timeouts so slow or stalled
// clients cannot hold connections open indefinitely.
func newServer(addr string, getenv func(string) string, scheduledPostStore scheduledposts.Store, imageStore scheduledposts.ImageStore, accountStore lemmyaccount.Store) *http.Server {
	return &http.Server{
		Addr:              addr,
		Handler:           newMux(getenv, scheduledPostStore, imageStore, accountStore),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      10 * time.Second,
		IdleTimeout:       60 * time.Second,
	}
}

// newMux registers every route the backend serves. Keeping this separate from
// main lets tests exercise routing and method matching.
func newMux(getenv func(string) string, scheduledPostStore scheduledposts.Store, imageStore scheduledposts.ImageStore, accountStore lemmyaccount.Store) *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", healthHandler)

	path, handler := lemmyschedulerv1connect.NewPingServiceHandler(api.PingServer{})
	mux.Handle(path, api.CORS(api.CORSAllowedOrigin(getenv), handler))
	path, handler = lemmyschedulerv1connect.NewScheduledPostServiceHandler(api.NewScheduledPostServer(scheduledPostStore))
	mux.Handle(path, api.CORS(api.CORSAllowedOrigin(getenv), handler))
	path, handler = lemmyschedulerv1connect.NewLemmyAccountServiceHandler(api.NewLemmyAccountServer(accountStore, lemmyclient.New(accountStore, nil)))
	mux.Handle(path, api.CORS(api.CORSAllowedOrigin(getenv), handler))
	mux.Handle("/scheduled-posts/", api.CORS(api.CORSAllowedOrigin(getenv), api.NewImageServer(imageStore).Handler()))
	return mux
}

// listenAddr resolves the backend's listen address from the LISTEN_ADDR
// environment variable, falling back to a sensible local default when it is
// unset or empty. The getenv function is injected so the fallback behavior
// can be tested without mutating process-wide environment state.
func listenAddr(getenv func(string) string) string {
	if addr := getenv("LISTEN_ADDR"); addr != "" {
		return addr
	}
	return ":8080"
}

func healthHandler(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	fmt.Fprint(w, `{"status":"ok"}`)
}
