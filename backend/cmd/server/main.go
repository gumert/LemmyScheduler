package main

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/gumert/LemmyScheduler/backend/internal/api"
	"github.com/gumert/LemmyScheduler/backend/internal/gen/lemmyscheduler/v1/lemmyschedulerv1connect"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	err := run(ctx, listenAddr(os.Getenv), os.Getenv)
	stop()
	if err != nil {
		log.Fatal(err)
	}
}

// run serves HTTP on addr until ctx is cancelled, then drains in-flight
// requests before returning. It returns nil on a clean shutdown.
func run(ctx context.Context, addr string, getenv func(string) string) error {
	srv := newServer(addr, getenv)

	errCh := make(chan error, 1)
	go func() {
		log.Printf("backend listening on %s", addr)
		errCh <- srv.ListenAndServe()
	}()

	select {
	case err := <-errCh:
		return err
	case <-ctx.Done():
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return srv.Shutdown(shutdownCtx)
}

// newServer builds the HTTP server with explicit timeouts so slow or stalled
// clients cannot hold connections open indefinitely.
func newServer(addr string, getenv func(string) string) *http.Server {
	return &http.Server{
		Addr:              addr,
		Handler:           newMux(getenv),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      10 * time.Second,
		IdleTimeout:       60 * time.Second,
	}
}

// newMux registers every route the backend serves. Keeping this separate from
// main lets tests exercise routing and method matching.
func newMux(getenv func(string) string) *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", healthHandler)

	path, handler := lemmyschedulerv1connect.NewPingServiceHandler(api.PingServer{})
	mux.Handle(path, api.CORS(api.CORSAllowedOrigin(getenv), handler))
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
