package api

import (
	"context"
	"errors"
	"log"
	"net/http"
	"time"
)

// NewHTTPServer applies timeouts that matter under load testing: without
// them a slow or stalled client can hold a connection open indefinitely and
// starve the server of file descriptors / goroutines.
func NewHTTPServer(addr string, handler http.Handler) *http.Server {
	return &http.Server{
		Addr:         addr,
		Handler:      handler,
		ReadTimeout:  5 * time.Second,
		WriteTimeout: 5 * time.Second,
		IdleTimeout:  60 * time.Second,
	}
}

// Run starts srv and blocks until ctx is cancelled, then shuts down
// gracefully (in-flight requests get up to 5s to finish).
func Run(ctx context.Context, srv *http.Server) error {
	errCh := make(chan error, 1)
	go func() {
		log.Printf("listening on %s", srv.Addr)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
	}()

	select {
	case err := <-errCh:
		return err
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		log.Println("shutting down gracefully...")
		return srv.Shutdown(shutdownCtx)
	}
}
