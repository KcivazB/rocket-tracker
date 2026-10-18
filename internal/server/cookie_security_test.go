package server

import (
	"context"
	"errors"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"rocket-tracker/internal/store"
)

func securityCookieContract(t *testing.T, c *http.Cookie, name string, secure bool) {
	t.Helper()
	if c.Name != name || c.Path != "/" || c.Domain != "" || c.Secure != secure || !c.HttpOnly || c.SameSite != http.SameSiteLaxMode {
		t.Fatalf("cookie contract %+v", c)
	}
}

func TestSecurityCookiePolicyExplicitLoopback(t *testing.T) {
	for _, tc := range []struct {
		url    string
		secure bool
	}{
		{"https://tracker.example.test", true}, {"http://127.0.0.1:8080", false}, {"http://[::1]:8080", false}, {"http://localhost:8080", false},
		{"http://tracker.example.test", true}, {"http://localhost.evil.invalid", true}, {"", true},
	} {
		t.Run(tc.url, func(t *testing.T) {
			h := &Hub{PublicURL: tc.url}
			prefix := ""
			if tc.secure {
				prefix = "__Host-"
			}
			for _, base := range []string{oauthCookie, sessionCookie} {
				for _, age := range []int{-1, 600} {
					securityCookieContract(t, h.securityCookie(base, "synthetic", age), prefix+base, tc.secure)
				}
			}
		})
	}
}

func TestSecurityCookieJarHostNamespaceAndBrowserLimit(t *testing.T) {
	jar, _ := cookiejar.New(nil)
	tracker, _ := url.Parse("https://tracker.example.test/auth/callback")
	sibling, _ := url.Parse("https://sibling.example.test/")
	h := &Hub{PublicURL: "https://tracker.example.test"}
	for _, base := range []string{oauthCookie, sessionCookie} {
		cookie := h.securityCookie(base, "tracker", 600)
		jar.SetCookies(tracker, []*http.Cookie{cookie})
		jar.SetCookies(sibling, []*http.Cookie{h.securityCookie(base, "sibling", 600)})
		jar.SetCookies(sibling, []*http.Cookie{{Name: base, Value: "legacy-injection", Domain: "example.test", Path: "/", Secure: true}})
	}
	req := httptest.NewRequest("GET", tracker.String(), nil)
	for _, c := range jar.Cookies(tracker) {
		req.AddCookie(c)
	}
	for _, base := range []string{oauthCookie, sessionCookie} {
		value, err := h.securityCookieValue(req, base)
		if err != nil || value != "tracker" {
			t.Fatalf("sibling replaced host cookie: value=%q err=%v", value, err)
		}
		// The legacy parent-domain cookie reaches the target, but is not selected.
		if c, err := req.Cookie(base); err != nil || c.Value != "legacy-injection" {
			t.Fatal("test did not deliver legacy injection")
		}
	}
	otherScheme, _ := url.Parse("http://tracker.example.test/auth/callback")
	for _, c := range jar.Cookies(otherScheme) {
		if strings.HasPrefix(c.Name, "__Host-") {
			t.Fatal("Secure cookie sent over HTTP")
		}
	}
	// Go's cookiejar does NOT enforce browser __Host- acceptance rules. Showing
	// that it accepts this invalid Domain cookie bounds the evidence above:
	// host-only scoping and server name selection are tested, real-browser
	// rejection of a sibling's prefixed Domain cookie remains qualification.
	limitedJar, _ := cookiejar.New(nil)
	limitedJar.SetCookies(sibling, []*http.Cookie{{Name: "__Host-rt_oauth", Value: "invalid-domain-cookie", Domain: "example.test", Path: "/", Secure: true}})
	if cs := limitedJar.Cookies(tracker); len(cs) != 1 {
		t.Fatalf("cookiejar behavior changed; reassess prefix qualification: %v", cs)
	}
	t.Log("cookiejar validates host scope, not __Host- browser acceptance; real browser sibling injection unqualified")
}

func TestSecurityHTTPSLoginRenewalLogout(t *testing.T) {
	idp := newSecurityIDP(t, securityIDPOptions{})
	e := newHub(t, &OIDCConfig{Issuer: idp.srv.URL, ClientID: "rt", ClientSecret: "s3cret"})
	e.ts.Close()
	e.ts = httptest.NewTLSServer(e.s.Handler())
	t.Cleanup(e.ts.Close)
	e.s.Hub.PublicURL = e.ts.URL
	e.s.Hub.OIDC.cfg.RedirectURL = e.ts.URL + "/auth/callback"
	c := e.ts.Client()
	c.Jar, _ = cookiejar.New(nil)
	c.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	u, _ := url.Parse(e.ts.URL)
	// HTTPS ignores legacy session namespace, even with a valid server token.
	user, err := e.st.UpsertOIDCUser(context.Background(), "old-sub", "old", "Old", "")
	if err != nil {
		t.Fatal(err)
	}
	legacy, err := e.st.CreateSession(context.Background(), user.ID, e.s.Hub.ttl())
	if err != nil {
		t.Fatal(err)
	}
	c.Jar.SetCookies(u, []*http.Cookie{{Name: sessionCookie, Value: legacy, Path: "/", Secure: true}})
	if code, _ := call(t, c, "GET", e.ts.URL+"/api/session", ""); code != 401 {
		t.Fatal("HTTPS accepted legacy session namespace")
	}
	cb, binding := securityAuthorization(t, e, c)
	securityCookieContract(t, binding, "__Host-rt_oauth", true)
	if binding.MaxAge != 600 || strings.Contains(binding.Value, "{") {
		t.Fatal("invalid opaque OAuth binding")
	}
	// A callback with only an unprefixed cookie cannot consume the transaction.
	req, _ := http.NewRequest("GET", cb, nil)
	req.AddCookie(&http.Cookie{Name: oauthCookie, Value: binding.Value})
	rawClient := *e.ts.Client()
	rawClient.Jar = nil
	rawClient.CheckRedirect = c.CheckRedirect
	r, err := rawClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	r.Body.Close()
	if r.StatusCode != 400 || idp.attempts.Load() != 0 {
		t.Fatal("legacy OAuth cookie used on HTTPS")
	}
	r = securityReadClose(t, c, cb)
	if r.StatusCode != 302 || r.Header.Get("Location") != "/#/players" {
		t.Fatalf("login %d location=%q", r.StatusCode, r.Header.Get("Location"))
	}
	var session *http.Cookie
	deletedOAuth := false
	for _, cookie := range r.Cookies() {
		switch cookie.Name {
		case "__Host-rt_oauth":
			securityCookieContract(t, cookie, cookie.Name, true)
			deletedOAuth = cookie.MaxAge < 0
		case "__Host-rt_session":
			securityCookieContract(t, cookie, cookie.Name, true)
			session = cookie
		}
	}
	if !deletedOAuth || session == nil {
		t.Fatal("login did not consume OAuth cookie and issue session")
	}
	signedIn, err := e.st.SessionUser(context.Background(), session.Value, e.s.Hub.ttl())
	if err != nil || signedIn == nil || signedIn.Handle != "verified" {
		t.Fatalf("session user=%+v err=%v", signedIn, err)
	}
	// Duplicate protected names are refused instead of taking the first cookie.
	req, _ = http.NewRequest("GET", e.ts.URL+"/api/session", nil)
	req.AddCookie(session)
	req.AddCookie(session)
	r, err = rawClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	r.Body.Close()
	if r.StatusCode != 401 {
		t.Fatal("ambiguous session accepted")
	}
	// Force a legitimate session into its refresh window without sleeping.
	old, err := e.st.CreateSession(context.Background(), signedIn.ID, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	c.Jar.SetCookies(u, []*http.Cookie{e.s.Hub.securityCookie(sessionCookie, old, 3600)})
	r = securityReadClose(t, c, e.ts.URL+"/api/session")
	if r.StatusCode != 200 || len(r.Cookies()) != 1 {
		t.Fatalf("renewal %d cookies=%d", r.StatusCode, len(r.Cookies()))
	}
	renewed := r.Cookies()[0]
	securityCookieContract(t, renewed, "__Host-rt_session", true)
	if renewed.Value != old || renewed.MaxAge != int(e.s.Hub.ttl().Seconds()) {
		t.Fatal("session did not slide")
	}
	// Cross-origin logout neither clears nor deletes a valid session.
	if code, _ := call(t, c, "POST", e.ts.URL+"/auth/logout", "", "Origin", "https://foreign.invalid"); code != 403 {
		t.Fatal("cross-origin logout accepted")
	}
	req, _ = http.NewRequest("POST", e.ts.URL+"/auth/logout", nil)
	req.Header.Set("Origin", e.ts.URL)
	r, err = c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	r.Body.Close()
	if r.StatusCode != 303 || len(r.Cookies()) != 1 {
		t.Fatalf("logout %d cookies=%d", r.StatusCode, len(r.Cookies()))
	}
	deleted := r.Cookies()[0]
	securityCookieContract(t, deleted, "__Host-rt_session", true)
	if deleted.MaxAge >= 0 {
		t.Fatal("logout did not delete cookie")
	}
	if _, err := e.st.SessionUser(context.Background(), old, e.s.Hub.ttl()); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("logout kept server session: %v", err)
	}
	if code, _ := call(t, c, "GET", e.ts.URL+"/api/session", ""); code != 401 {
		t.Fatal("logout kept browser session")
	}
}
