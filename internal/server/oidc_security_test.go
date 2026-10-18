package server

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-jose/go-jose/v4"
	"github.com/go-jose/go-jose/v4/jwt"
	"golang.org/x/oauth2"
)

type securityIDPOptions struct {
	claims              map[string]any
	userinfo            map[string]any
	noProfile, noGroups bool
	tokenKind           string
}

type securityIDP struct {
	srv      *httptest.Server
	attempts atomic.Int64
	accepted atomic.Int64
	infos    atomic.Int64
}

// This oracle checks the actual S256 relation and consumes a code atomically.
// Options are fixed before serving; concurrent callback tests do not mutate it.
func newSecurityIDP(t *testing.T, opts securityIDPOptions) *securityIDP {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	signingKey := key
	if opts.tokenKind == "bad-signature" {
		signingKey, err = rsa.GenerateKey(rand.Reader, 2048)
		if err != nil {
			t.Fatal(err)
		}
	}
	p := &securityIDP{}
	type grant struct{ nonce, challenge, redirect string }
	var mu sync.Mutex
	codes := map[string]grant{}
	mux := http.NewServeMux()
	p.srv = httptest.NewServer(mux)
	t.Cleanup(p.srv.Close)
	mux.HandleFunc("/.well-known/openid-configuration", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"issuer": p.srv.URL, "authorization_endpoint": p.srv.URL + "/authorize", "token_endpoint": p.srv.URL + "/token", "jwks_uri": p.srv.URL + "/jwks", "userinfo_endpoint": p.srv.URL + "/userinfo", "id_token_signing_alg_values_supported": []string{"RS256"}})
	})
	mux.HandleFunc("/jwks", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(jose.JSONWebKeySet{Keys: []jose.JSONWebKey{{Key: &key.PublicKey, KeyID: "security-key", Algorithm: "RS256", Use: "sig"}}})
	})
	mux.HandleFunc("/authorize", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		if q.Get("code_challenge_method") != "S256" || len(q.Get("code_challenge")) != 43 || q.Get("state") == "" || q.Get("nonce") == "" || q.Get("client_id") != "rt" {
			http.Error(w, "invalid authorization", 400)
			return
		}
		code := fmt.Sprintf("code-%s", q.Get("state"))
		mu.Lock()
		codes[code] = grant{q.Get("nonce"), q.Get("code_challenge"), q.Get("redirect_uri")}
		mu.Unlock()
		http.Redirect(w, r, q.Get("redirect_uri")+"?"+url.Values{"code": {code}, "state": {q.Get("state")}}.Encode(), 302)
	})
	mux.HandleFunc("/token", func(w http.ResponseWriter, r *http.Request) {
		p.attempts.Add(1)
		_ = r.ParseForm()
		code := r.Form.Get("code")
		mu.Lock()
		g, ok := codes[code]
		delete(codes, code)
		mu.Unlock()
		id, secret, _ := r.BasicAuth()
		if !ok || id != "rt" || secret != "s3cret" || r.Form.Get("grant_type") != "authorization_code" || r.Form.Get("redirect_uri") != g.redirect || oauth2.S256ChallengeFromVerifier(r.Form.Get("code_verifier")) != g.challenge {
			http.Error(w, `{"error":"invalid_grant"}`, 400)
			return
		}
		p.accepted.Add(1)
		claims := map[string]any{"iss": p.srv.URL, "sub": "verified-subject", "aud": "rt", "exp": time.Now().Add(time.Minute).Unix(), "iat": time.Now().Unix(), "nonce": g.nonce}
		if !opts.noProfile {
			claims["preferred_username"] = "verified"
			claims["name"] = "Verified"
		}
		if !opts.noGroups {
			claims["groups"] = []string{"gamers"}
		}
		for k, v := range opts.claims {
			claims[k] = v
		}
		sig, e := jose.NewSigner(jose.SigningKey{Algorithm: jose.RS256, Key: signingKey}, (&jose.SignerOptions{}).WithHeader("kid", "security-key"))
		if e != nil {
			http.Error(w, "fixture signer", 500)
			return
		}
		raw, e := jwt.Signed(sig).Claims(claims).Serialize()
		if e != nil {
			http.Error(w, "fixture token", 500)
			return
		}
		switch opts.tokenKind {
		case "hs256":
			sig, e = jose.NewSigner(jose.SigningKey{Algorithm: jose.HS256, Key: []byte(strings.Repeat("s", 32))}, nil)
			if e != nil {
				http.Error(w, "fixture HMAC", 500)
				return
			}
			raw, e = jwt.Signed(sig).Claims(claims).Serialize()
			if e != nil {
				http.Error(w, "fixture HMAC token", 500)
				return
			}
		case "unsigned":
			b, _ := json.Marshal(claims)
			raw = base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"none"}`)) + "." + base64.RawURLEncoding.EncodeToString(b) + "."
		case "jwe":
			raw = base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"A128KW","enc":"A128GCM"}`)) + "..AAAAAAAAAAAAAAAA.AA.AAAAAAAAAAAAAAAAAAAAAA"
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"access_token": "synthetic", "token_type": "Bearer", "expires_in": 60, "id_token": raw})
	})
	mux.HandleFunc("/userinfo", func(w http.ResponseWriter, r *http.Request) {
		p.infos.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(opts.userinfo)
	})
	return p
}

func securityReadClose(t *testing.T, c *http.Client, address string) *http.Response {
	t.Helper()
	resp, err := c.Get(address)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	return resp
}

// Return a callback and the browser binding without consuming the callback.
func securityAuthorization(t *testing.T, e *hubEnv, c *http.Client) (string, *http.Cookie) {
	t.Helper()
	r := securityReadClose(t, c, e.ts.URL+"/auth/login?next="+url.QueryEscape("/#/players"))
	if r.StatusCode != 302 {
		t.Fatalf("login status %d", r.StatusCode)
	}
	var binding *http.Cookie
	for _, cookie := range r.Cookies() {
		if cookie.Name == e.s.Hub.cookieName(oauthCookie) {
			binding = cookie
		}
	}
	if binding == nil {
		t.Fatal("missing browser binding")
	}
	r = securityReadClose(t, c, r.Header.Get("Location"))
	if r.StatusCode != 302 {
		t.Fatalf("authorize status %d", r.StatusCode)
	}
	return r.Header.Get("Location"), binding
}

func TestSecurityUserInfoSubject(t *testing.T) {
	for _, tc := range []struct {
		name string
		sub  any
		want int
	}{{"equal", "verified-subject", 302}, {"different", "foreign-subject", 502}, {"empty", "", 502}, {"missing", nil, 502}} {
		for _, profileFallback := range []bool{true, false} {
			t.Run(fmt.Sprintf("%s/profile=%t", tc.name, profileFallback), func(t *testing.T) {
				info := map[string]any{"preferred_username": "fallback", "nickname": "FallbackNick", "name": "Fallback Name", "email": "fallback@example.test", "groups": []string{"gamers"}}
				if tc.sub != nil {
					info["sub"] = tc.sub
				}
				idp := newSecurityIDP(t, securityIDPOptions{userinfo: info, noProfile: profileFallback, noGroups: true})
				e := newHub(t, &OIDCConfig{Issuer: idp.srv.URL, ClientID: "rt", ClientSecret: "s3cret", AllowedGroups: []string{"gamers"}})
				c := e.browser(t)
				callback, _ := securityAuthorization(t, e, c)
				r := securityReadClose(t, c, callback)
				if r.StatusCode != tc.want || idp.accepted.Load() != 1 || idp.infos.Load() != 1 {
					t.Fatalf("callback %d accepted=%d userinfo=%d", r.StatusCode, idp.accepted.Load(), idp.infos.Load())
				}
				users, err := e.st.Users(context.Background())
				if err != nil {
					t.Fatal(err)
				}
				code, _ := call(t, c, "GET", e.ts.URL+"/api/session", "")
				if tc.want == 302 {
					if len(users) != 1 || code != 200 {
						t.Fatalf("successful identity absent: users=%d session=%d", len(users), code)
					}
					if profileFallback && (users[0].Handle != "fallback" || users[0].Name != "Fallback Name") {
						t.Fatalf("fallback profile not used: %+v", users[0])
					}
				} else if len(users) != 0 || code != 401 {
					t.Fatalf("foreign claims created account/session: users=%d session=%d", len(users), code)
				}
			})
		}
	}
}

func TestSecurityOIDCCryptoNegatives(t *testing.T) {
	for _, tc := range []struct {
		name, kind string
		claims     map[string]any
	}{
		{name: "issuer", claims: map[string]any{"iss": "https://foreign.invalid"}},
		{name: "audience", claims: map[string]any{"aud": "foreign-client"}},
		{name: "expiry", claims: map[string]any{"exp": time.Now().Add(-time.Hour).Unix()}},
		{name: "nonce", claims: map[string]any{"nonce": "foreign-nonce"}},
		{name: "empty-subject", claims: map[string]any{"sub": ""}},
		{name: "signature", kind: "bad-signature"}, {name: "HMAC", kind: "hs256"}, {name: "unsigned", kind: "unsigned"}, {name: "JWE", kind: "jwe"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			idp := newSecurityIDP(t, securityIDPOptions{claims: tc.claims, tokenKind: tc.kind})
			e := newHub(t, &OIDCConfig{Issuer: idp.srv.URL, ClientID: "rt", ClientSecret: "s3cret"})
			c := e.browser(t)
			cb, _ := securityAuthorization(t, e, c)
			r := securityReadClose(t, c, cb)
			if r.StatusCode != 400 && r.StatusCode != 502 {
				t.Fatalf("invalid identity status=%d", r.StatusCode)
			}
			users, err := e.st.Users(context.Background())
			if err != nil || len(users) != 0 {
				t.Fatalf("invalid token created users=%d err=%v", len(users), err)
			}
			if code, _ := call(t, c, "GET", e.ts.URL+"/api/session", ""); code != 401 {
				t.Fatal("invalid token created session")
			}
			if idp.infos.Load() != 0 {
				t.Fatal("userinfo used before token validation")
			}
		})
	}
}

func TestSecurityOAuthTransactionBindingAndConsumption(t *testing.T) {
	for _, scenario := range []string{"success-replay", "concurrent-replay", "wrong-state", "provider-error", "expired", "wrong-namespace", "missing-cookie", "other-browser", "forged-state-cookie"} {
		t.Run(scenario, func(t *testing.T) {
			idp := newSecurityIDP(t, securityIDPOptions{})
			e := newHub(t, &OIDCConfig{Issuer: idp.srv.URL, ClientID: "rt", ClientSecret: "s3cret"})
			c := e.browser(t)
			cb, binding := securityAuthorization(t, e, c)
			request := func(address string, cookie *http.Cookie) int {
				t.Helper()
				req, err := http.NewRequest("GET", address, nil)
				if err != nil {
					t.Fatal(err)
				}
				if cookie != nil {
					req.AddCookie(cookie)
				}
				resp, err := (&http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}).Do(req)
				if err != nil {
					t.Fatal(err)
				}
				resp.Body.Close()
				return resp.StatusCode
			}
			switch scenario {
			case "success-replay":
				if got := request(cb, binding); got != 302 {
					t.Fatalf("sign in %d", got)
				}
				if got := request(cb, binding); got != 400 {
					t.Fatalf("replay %d", got)
				}
			case "concurrent-replay":
				results := make(chan int, 2)
				var wg sync.WaitGroup
				for range 2 {
					wg.Add(1)
					go func() { defer wg.Done(); results <- request(cb, binding) }()
				}
				wg.Wait()
				close(results)
				counts := map[int]int{}
				for code := range results {
					counts[code]++
				}
				if counts[302] != 1 || counts[400] != 1 {
					t.Fatalf("concurrent callbacks: %v", counts)
				}
			case "wrong-state", "provider-error":
				u, _ := url.Parse(cb)
				q := u.Query()
				if scenario == "wrong-state" {
					q.Set("state", "foreign")
				} else {
					q.Set("error", "access_denied")
				}
				u.RawQuery = q.Encode()
				want := 400
				if scenario == "provider-error" {
					want = 403
				}
				if got := request(u.String(), binding); got != want {
					t.Fatalf("failed callback %d", got)
				}
				if got := request(cb, binding); got != 400 {
					t.Fatalf("failed transaction reused %d", got)
				}
			case "expired", "wrong-namespace":
				o := e.s.Hub.OIDC
				o.mu.Lock()
				tx := o.transactions[binding.Value]
				if scenario == "expired" {
					tx.expires = time.Now().Add(-time.Second)
				} else {
					tx.namespace = "https://other.invalid/__Host-rt_oauth"
				}
				o.transactions[binding.Value] = tx
				o.mu.Unlock()
				if got := request(cb, binding); got != 400 {
					t.Fatalf("invalid transaction %d", got)
				}
			case "missing-cookie", "other-browser", "forged-state-cookie":
				var bad *http.Cookie
				if scenario == "other-browser" {
					other := e.browser(t)
					_, bad = securityAuthorization(t, e, other)
				}
				if scenario == "forged-state-cookie" {
					raw, _ := json.Marshal(oauthState{State: "attacker-state", Nonce: "attacker-nonce", Verifier: oauth2.GenerateVerifier(), Next: "/"})
					bad = &http.Cookie{Name: binding.Name, Value: base64.RawURLEncoding.EncodeToString(raw)}
				}
				if got := request(cb, bad); got != 400 {
					t.Fatalf("unbound callback %d", got)
				}
				if idp.attempts.Load() != 0 {
					t.Fatal("unbound callback reached token endpoint")
				}
				if got := request(cb, binding); got != 302 {
					t.Fatalf("valid browser lost transaction %d", got)
				}
			}
			expected := int64(0)
			switch scenario {
			case "success-replay", "concurrent-replay", "missing-cookie", "other-browser", "forged-state-cookie":
				expected = 1
			}
			if idp.attempts.Load() != expected || idp.accepted.Load() != expected {
				t.Fatalf("token calls=%d accepted=%d want=%d", idp.attempts.Load(), idp.accepted.Load(), expected)
			}
		})
	}
}

func TestSecurityIDPOraclePKCEAndSingleUse(t *testing.T) {
	idp := newSecurityIDP(t, securityIDPOptions{})
	c := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	issue := func(state, verifier string) string {
		t.Helper()
		q := url.Values{"client_id": {"rt"}, "redirect_uri": {"http://127.0.0.1/auth/callback"}, "state": {state}, "nonce": {"synthetic-nonce"}, "code_challenge_method": {"S256"}, "code_challenge": {oauth2.S256ChallengeFromVerifier(verifier)}}
		r := securityReadClose(t, c, idp.srv.URL+"/authorize?"+q.Encode())
		if r.StatusCode != 302 {
			t.Fatalf("oracle authorization %d", r.StatusCode)
		}
		u, _ := url.Parse(r.Header.Get("Location"))
		return u.Query().Get("code")
	}
	exchange := func(code, verifier string) int {
		t.Helper()
		q := url.Values{"grant_type": {"authorization_code"}, "redirect_uri": {"http://127.0.0.1/auth/callback"}, "code": {code}, "code_verifier": {verifier}}
		req, _ := http.NewRequest("POST", idp.srv.URL+"/token", strings.NewReader(q.Encode()))
		req.SetBasicAuth("rt", "s3cret")
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		r, err := c.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		r.Body.Close()
		return r.StatusCode
	}
	verifier := oauth2.GenerateVerifier()
	code := issue("wrong-verifier", verifier)
	if exchange(code, oauth2.GenerateVerifier()) != 400 || exchange(code, verifier) != 400 {
		t.Fatal("oracle accepted wrong S256 or reused attempted code")
	}
	code = issue("valid", verifier)
	if exchange(code, verifier) != 200 || exchange(code, verifier) != 400 || idp.accepted.Load() != 1 {
		t.Fatal("oracle did not enforce successful code single-use")
	}
}

func TestSecurityOAuthNewLoginInvalidatesPrevious(t *testing.T) {
	idp := newSecurityIDP(t, securityIDPOptions{})
	e := newHub(t, &OIDCConfig{Issuer: idp.srv.URL, ClientID: "rt", ClientSecret: "s3cret"})
	c := e.browser(t)
	oldCallback, oldBinding := securityAuthorization(t, e, c)
	newCallback, _ := securityAuthorization(t, e, c)
	req, _ := http.NewRequest("GET", oldCallback, nil)
	req.AddCookie(oldBinding)
	raw := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := raw.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 400 || idp.attempts.Load() != 0 {
		t.Fatal("previous browser transaction survived new login")
	}
	if r := securityReadClose(t, c, newCallback); r.StatusCode != 302 {
		t.Fatalf("new login failed %d", r.StatusCode)
	}
}

func TestSecurityOAuthBadVerifierConsumesTransaction(t *testing.T) {
	idp := newSecurityIDP(t, securityIDPOptions{})
	e := newHub(t, &OIDCConfig{Issuer: idp.srv.URL, ClientID: "rt", ClientSecret: "s3cret"})
	c := e.browser(t)
	cb, binding := securityAuthorization(t, e, c)
	o := e.s.Hub.OIDC
	o.mu.Lock()
	tx := o.transactions[binding.Value]
	tx.state.Verifier = oauth2.GenerateVerifier()
	o.transactions[binding.Value] = tx
	o.mu.Unlock()
	if r := securityReadClose(t, c, cb); r.StatusCode != 502 {
		t.Fatalf("bad verifier %d", r.StatusCode)
	}
	attempts := idp.attempts.Load() // oauth2 may negotiate endpoint auth style on error.
	req, _ := http.NewRequest("GET", cb, nil)
	req.AddCookie(binding)
	r, err := c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	r.Body.Close()
	if r.StatusCode != 400 || attempts < 1 || idp.attempts.Load() != attempts || idp.accepted.Load() != 0 {
		t.Fatal("failed PKCE reused a transaction")
	}
}

func TestSecurityOAuthPendingBoundAndRecovery(t *testing.T) {
	o := NewOIDC(OIDCConfig{})
	h := &Hub{PublicURL: "https://tracker.example.test"}
	for range 4096 {
		if _, err := o.begin(h, "", oauthState{}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := o.begin(h, "", oauthState{}); err == nil {
		t.Fatal("pending sign-ins unbounded")
	}
	o.mu.Lock()
	for binding, tx := range o.transactions {
		tx.expires = time.Now().Add(-time.Second)
		o.transactions[binding] = tx
	}
	o.mu.Unlock()
	binding, err := o.begin(h, "", oauthState{State: "fresh"})
	if err != nil {
		t.Fatal("expired transactions did not release capacity")
	}
	st, err := o.consume(h, binding)
	if err != nil || st.State != "fresh" {
		t.Fatalf("recovery %+v err=%v", st, err)
	}
	if _, err := o.consume(h, binding); err == nil {
		t.Fatal("consumed transaction survived")
	}
}
