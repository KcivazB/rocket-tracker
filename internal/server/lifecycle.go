package server

import (
	"context"
	"errors"
	"net"
	"net/http"
	"sync"
	"time"
)

// serveUntilStopped joins admitted handlers before the composition can close
// SQLite. Shutdown returning ErrServerClosed alone does not establish this.
func serveUntilStopped(parent context.Context, ln net.Listener, h http.Handler) error {
	ctx, cancel := context.WithCancel(parent)
	defer cancel()
	var mu sync.Mutex
	var handlers sync.WaitGroup
	draining := false
	srv := &http.Server{ReadHeaderTimeout: 10 * time.Second, BaseContext: func(net.Listener) context.Context { return ctx }, Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		if draining {
			mu.Unlock()
			http.Error(w, "server stopping", http.StatusServiceUnavailable)
			return
		}
		handlers.Add(1)
		mu.Unlock()
		defer handlers.Done()
		h.ServeHTTP(w, r)
	})}
	stopped := make(chan struct{})
	go func() {
		defer close(stopped)
		<-ctx.Done()
		mu.Lock()
		draining = true
		mu.Unlock()
		grace, stop := context.WithTimeout(context.Background(), 3*time.Second)
		err := srv.Shutdown(grace)
		stop()
		if err != nil {
			_ = srv.Close()
		}
		handlers.Wait()
	}()
	err := srv.Serve(ln)
	cancel()
	<-stopped
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}
