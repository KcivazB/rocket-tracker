package agent

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"rocket-tracker/internal/domain"
	"rocket-tracker/internal/store"
)

type securityRoundTrip func(*http.Request) (*http.Response, error)

func (f securityRoundTrip) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestSecurityUploadsRefuseRedirects(t *testing.T) {
	for _, code := range []int{301, 302, 303, 307, 308} {
		for _, target := range []string{"/same-origin", "https://other.invalid/ingest", "https://approved.invalid:9443/ingest", "http://approved.invalid/ingest"} {
			for _, training := range []bool{false, true} {
				t.Run(fmt.Sprintf("%d/%s/training=%t", code, target, training), func(t *testing.T) {
					calls, hookCalls := 0, 0
					hc := &http.Client{Timeout: time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { hookCalls++; return nil }, Transport: securityRoundTrip(func(r *http.Request) (*http.Response, error) {
						calls++
						if calls != 1 {
							t.Error("secondary request reached transport")
						}
						if r.Header.Get("Authorization") != "Bearer synthetic" {
							t.Error("initial auth missing")
						}
						body, err := io.ReadAll(r.Body)
						if err != nil || len(body) == 0 {
							t.Error("initial payload missing")
						}
						return &http.Response{StatusCode: code, Header: http.Header{"Location": {target}}, Body: io.NopCloser(strings.NewReader(`{}`)), Request: r}, nil
					})}
					c := &Client{Server: "https://approved.invalid", Token: "synthetic", HTTP: hc}
					var err error
					if training {
						err = c.SendTraining(context.Background(), "key", &store.Training{})
					} else {
						_, err = c.SendMatch(context.Background(), "key", &store.Match{})
					}
					if err == nil || calls != 1 || hookCalls != 0 {
						t.Fatalf("err=%v requests=%d redirect hooks=%d", err, calls, hookCalls)
					}
					// The per-request policy must not mutate the supplied client.
					_ = hc.CheckRedirect(nil, nil)
					if hookCalls != 1 || hc.Timeout != time.Second {
						t.Fatal("caller client mutated")
					}
				})
			}
		}
	}
}

func TestSecurityNonSuccessCannotAcknowledge(t *testing.T) {
	for _, code := range []int{100, 101, 103, 199, 300, 304, 305, 399, 500} {
		t.Run(fmt.Sprint(code), func(t *testing.T) {
			c := &Client{Server: "https://approved.invalid", HTTP: &http.Client{Transport: securityRoundTrip(func(r *http.Request) (*http.Response, error) {
				return &http.Response{StatusCode: code, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{}`)), Request: r}, nil
			})}}
			if err := c.SendTraining(context.Background(), "key", &store.Training{}); err == nil {
				t.Fatal("non-2xx accepted")
			}
		})
	}
	// 1xx informational responses are allowed before a real final 2xx; they do
	// not themselves acknowledge the upload or cause a second HTTP request.
	calls := 0
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.WriteHeader(103)
		w.WriteHeader(200)
		_, _ = io.WriteString(w, `{}`)
	}))
	defer ts.Close()
	if err := (&Client{Server: ts.URL}).SendTraining(context.Background(), "key", &store.Training{}); err != nil || calls != 1 {
		t.Fatalf("final 200 err=%v calls=%d", err, calls)
	}
}

func TestSecurityRedirectLeavesJournalPending(t *testing.T) {
	for _, training := range []bool{false, true} {
		t.Run(fmt.Sprint(training), func(t *testing.T) {
			ctx := context.Background()
			st, err := store.Open(filepath.Join(t.TempDir(), "local.db"))
			if err != nil {
				t.Fatal(err)
			}
			defer st.Close()
			d := domain.Destination{Server: "https://approved.invalid", ServerID: "synthetic-install", AccountID: 42}
			sc := st.User(store.LocalUser)
			if training {
				start := time.Now().Add(-time.Hour)
				err = sc.RecordTraining(ctx, &store.Training{Source: "game", StartedAt: start, EndedAt: start.Add(5 * time.Minute)}, &d)
			} else {
				err = sc.RecordResult(ctx, &store.Match{GUID: "synthetic", StartedAt: time.Now().Add(-time.Hour), EndedAt: time.Now(), Result: "win"}, &d)
			}
			if err != nil {
				t.Fatal(err)
			}
			calls := 0
			c := &Client{Server: d.Server, Token: "synthetic", HTTP: &http.Client{Transport: securityRoundTrip(func(r *http.Request) (*http.Response, error) {
				calls++
				header := make(http.Header)
				body := `{"server_id":"synthetic-install","user":{"id":42}}`
				code := 200
				if r.URL.Path != "/api/agent/heartbeat" {
					code = 307
					header.Set("Location", "https://other.invalid/ingest")
					body = `{}`
				}
				if r.URL.Host != "approved.invalid" {
					t.Error("foreign destination reached")
				}
				return &http.Response{StatusCode: code, Header: header, Body: io.NopCloser(strings.NewReader(body)), Request: r}, nil
			})}}
			a := New(&Config{Server: d.Server, Token: "synthetic", ServerID: d.ServerID, AccountID: d.AccountID}, Options{Client: c, Journal: sc})
			n, err := a.uploadPending(ctx)
			if err == nil || n != 0 || calls != 2 {
				t.Fatalf("sent=%d err=%v calls=%d", n, err, calls)
			}
			pending, err := sc.PendingDeliveries(ctx, d)
			if err != nil || len(pending) != 1 {
				t.Fatalf("pending=%d err=%v", len(pending), err)
			}
			status, err := sc.DeliveryStatus(ctx)
			if err != nil || status.Rejected != 0 {
				t.Fatalf("redirect marked permanently rejected: %+v err=%v", status, err)
			}
		})
	}
}
