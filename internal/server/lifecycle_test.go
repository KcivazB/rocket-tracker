package server

import (
	"context"
	"net"
	"net/http"
	"testing"
)

func TestHTTPShutdownJoinsActiveHandlers(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	entered := make(chan struct{})
	cancelled := make(chan struct{})
	release := make(chan struct{})
	finished := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		done <- serveUntilStopped(ctx, ln, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			close(entered)
			<-r.Context().Done()
			close(cancelled)
			<-release
			close(finished)
		}))
	}()
	requestDone := make(chan struct{})
	go func() {
		defer close(requestDone)
		resp, e := http.Get("http://" + ln.Addr().String())
		if e == nil {
			resp.Body.Close()
		}
	}()
	<-entered
	cancel()
	<-cancelled
	select {
	case e := <-done:
		t.Fatalf("server returned while writer was still active: %v", e)
	default:
	}
	close(release)
	if e := <-done; e != nil {
		t.Fatal(e)
	}
	select {
	case <-finished:
	default:
		t.Fatal("handler not joined")
	}
	<-requestDone
}
