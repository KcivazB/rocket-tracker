package server

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/go-jose/go-jose/v4"
	"github.com/go-jose/go-jose/v4/jwt"

	"rocket-tracker/internal/store"
)

type hubEnv struct {
	ts *httptest.Server
	st *store.Store
	s  *Server
}

func newHub(t *testing.T, oidcCfg *OIDCConfig) *hubEnv {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "hub.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	s := &Server{Version: "test", Store: st, Static: fstest.MapFS{"index.html": {Data: []byte("<h1>dash</h1>")}, "style.css": {Data: []byte("body{}")}}}
	ts := httptest.NewUnstartedServer(nil)
	ts.Start()
	s.Hub = &Hub{PublicURL: ts.URL, DevLogin: oidcCfg == nil}
	if oidcCfg != nil {
		oidcCfg.RedirectURL = ts.URL + "/auth/callback"
		s.Hub.OIDC = NewOIDC(*oidcCfg)
	}
	ts.Config.Handler = s.Handler()
	t.Cleanup(ts.Close)
	return &hubEnv{ts: ts, st: st, s: s}
}

// browser is a cookie-keeping client that does not follow redirects.
func (e *hubEnv) browser(t *testing.T) *http.Client {
	jar, _ := cookiejar.New(nil)
	return &http.Client{Jar: jar, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
}

func (e *hubEnv) devLogin(t *testing.T, name string) *http.Client {
	t.Helper()
	c := e.browser(t)
	resp, err := c.Get(e.ts.URL + "/auth/dev?user=" + url.QueryEscape(name) + "&next=/%23/joueurs")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusFound || resp.Header.Get("Location") != "/#/joueurs" {
		t.Fatalf("dev login %d %q", resp.StatusCode, resp.Header.Get("Location"))
	}
	return c
}

// call sends a request as the dashboard would (same Origin).
func call(t *testing.T, c *http.Client, method, u, body string, hdr ...string) (int, string) {
	t.Helper()
	req, _ := http.NewRequest(method, u, strings.NewReader(body))
	if method != "GET" {
		pu, _ := url.Parse(u)
		req.Header.Set("Origin", pu.Scheme+"://"+pu.Host)
		req.Header.Set("Content-Type", "application/json")
	}
	for i := 0; i+1 < len(hdr); i += 2 {
		if hdr[i+1] == "" {
			req.Header.Del(hdr[i])
		} else {
			req.Header.Set(hdr[i], hdr[i+1])
		}
	}
	resp, err := c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	return resp.StatusCode, string(b)
}

func TestHubRequiresSignIn(t *testing.T) {
	e := newHub(t, nil)
	c := e.browser(t)
	if code, _ := call(t, c, "GET", e.ts.URL+"/api/matches", ""); code != 401 {
		t.Fatalf("api without session: %d", code)
	}
	resp, _ := c.Get(e.ts.URL + "/")
	if resp.StatusCode != 302 || !strings.HasPrefix(resp.Header.Get("Location"), "/auth/login?next=") {
		t.Fatalf("page without session: %d %s", resp.StatusCode, resp.Header.Get("Location"))
	}
	for _, p := range []string{"/healthz", "/style.css", "/auth/login"} {
		if code, _ := call(t, c, "GET", e.ts.URL+p, ""); code != 200 {
			t.Fatalf("%s: %d", p, code)
		}
	}
	// Sign-in pages follow the browser language.
	if _, body := call(t, c, "GET", e.ts.URL+"/auth/logged-out", "", "Accept-Language", "fr-FR,fr;q=0.9"); !strings.Contains(body, "Vous êtes déconnecté") || !strings.Contains(body, `lang="fr"`) {
		t.Fatalf("french page: %s", body)
	}
	if _, body := call(t, c, "GET", e.ts.URL+"/auth/logged-out", "", "Accept-Language", "en-US"); !strings.Contains(body, "You are signed out") {
		t.Fatalf("english page: %s", body)
	}
	// An open redirect through ?next= is refused.
	resp, _ = c.Get(e.ts.URL + "/auth/dev?user=x&next=//evil.example")
	if resp.Header.Get("Location") != "/" {
		t.Fatalf("next not sanitized: %s", resp.Header.Get("Location"))
	}
}

func TestHubAgentToDashboard(t *testing.T) {
	e := newHub(t, nil)
	alice := e.devLogin(t, "Alice")
	bob := e.devLogin(t, "Bob")
	base := e.ts.URL

	code, body := call(t, alice, "GET", base+"/api/session", "")
	if code != 200 || !strings.Contains(body, `"mode":"server"`) || !strings.Contains(body, `"handle":"alice"`) {
		t.Fatalf("session %d %s", code, body)
	}

	// CSRF: a state-changing request from another origin is refused.
	if code, _ := call(t, alice, "POST", base+"/api/devices", `{"name":"x"}`, "Origin", "https://evil.example"); code != 403 {
		t.Fatalf("cross-origin POST: %d", code)
	}
	if code, _ := call(t, alice, "POST", base+"/api/devices", `{"name":"x"}`, "Origin", "", "Sec-Fetch-Site", "cross-site"); code != 403 {
		t.Fatalf("cross-site POST without Origin: %d", code)
	}
	code, body = call(t, alice, "POST", base+"/api/devices", `{"name":"PC salon"}`)
	var created struct {
		Token  string       `json:"token"`
		Server string       `json:"server"`
		Device store.Device `json:"device"`
	}
	if code != 201 || json.Unmarshal([]byte(body), &created) != nil || !strings.HasPrefix(created.Token, "rtk_") || created.Server != base {
		t.Fatalf("create device %d %s", code, body)
	}
	agent := &http.Client{}
	auth := []string{"Authorization", "Bearer " + created.Token, "Origin", ""}

	// The device token only opens the agent API, the session only the rest.
	if code, _ := call(t, agent, "GET", base+"/api/matches", "", auth...); code != 401 {
		t.Fatalf("token on dashboard API: %d", code)
	}
	if code, _ := call(t, alice, "POST", base+"/api/agent/heartbeat", `{}`); code != 401 {
		t.Fatalf("session on agent API: %d", code)
	}
	if code, _ := call(t, agent, "POST", base+"/api/agent/heartbeat", `{}`, "Authorization", "Bearer rtk_nope"); code != 401 {
		t.Fatalf("bad token: %d", code)
	}

	hb := `{"version":"t","connected":true,"transport":"ws","live":{"mode":"2v2","team_score":1},"ini":{"found":true,"ok":true},
		"auto_identity":{"name":"AliceRL","primary_id":"Epic|a|0"}}`
	code, body = call(t, agent, "POST", base+"/api/agent/heartbeat", hb, auth...)
	if code != 200 || !strings.Contains(body, `"player_names":["AliceRL"]`) || !strings.Contains(body, `"default_tag":"ranked"`) {
		t.Fatalf("heartbeat %d %s", code, body)
	}
	code, body = call(t, alice, "GET", base+"/api/status", "")
	if code != 200 || !strings.Contains(body, `"in_match":true`) || !strings.Contains(body, `"online":true`) || !strings.Contains(body, `"devices":1`) ||
		!strings.Contains(body, `"ok":true`) {
		t.Fatalf("status %d %s", code, body)
	}

	t0 := time.Now().Add(-time.Hour).UTC().Format(time.RFC3339)
	t1 := time.Now().Add(-55 * time.Minute).UTC().Format(time.RFC3339)
	match := `{"key":"k1","match":{"guid":"local-1","online":true,"guid":"G1","started_at":"` + t0 + `","ended_at":"` + t1 +
		`","mode":"1v1","variant":"Soccar","result":"win","tag":"bogus","team_score":3,"opp_score":1,"me":{"name":"AliceRL","goals":2}}}`
	for i := 0; i < 2; i++ { // retried upload: still one match
		if code, body := call(t, agent, "POST", base+"/api/agent/matches", match, auth...); code != 200 {
			t.Fatalf("upload %d %s", code, body)
		}
	}
	if code, body := call(t, agent, "POST", base+"/api/agent/matches", `{"key":"k2","match":{"result":"draw"}}`, auth...); code != 400 {
		t.Fatalf("invalid match accepted: %d %s", code, body)
	}
	if code, body := call(t, agent, "PUT", base+"/api/agent/manual/2026-10-01/1v1", `{"games":4,"wins":3}`, auth...); code != 200 {
		t.Fatalf("manual %d %s", code, body)
	}
	var ms []store.Match
	_, body = call(t, alice, "GET", base+"/api/matches", "")
	if json.Unmarshal([]byte(body), &ms) != nil || len(ms) != 1 || ms[0].Tag != "ranked" {
		t.Fatalf("alice matches %s", body)
	}
	id := ms[0].ID

	// Bob sees Alice read-only, and cannot touch her matches.
	_, body = call(t, bob, "GET", base+"/api/players/alice/matches", "")
	if !strings.Contains(body, `"guid":"G1"`) {
		t.Fatalf("bob reading alice %s", body)
	}
	if _, body = call(t, bob, "GET", base+"/api/players/alice/manual", ""); !strings.Contains(body, `"games":4`) {
		t.Fatalf("alice manual %s", body)
	}
	if _, body = call(t, bob, "GET", base+"/api/players/alice/status", ""); !strings.Contains(body, `"in_match":true`) || strings.Contains(body, `"ok":true`) {
		t.Fatalf("alice status seen by bob %s", body)
	}
	if code, _ := call(t, bob, "DELETE", base+"/api/matches/"+itoa(int(id)), ""); code != 404 {
		t.Fatalf("bob deleted alice's match: %d", code)
	}
	if code, _ := call(t, bob, "PATCH", base+"/api/matches/"+itoa(int(id)), `{"tag":"casual"}`); code != 404 {
		t.Fatalf("bob tagged alice's match: %d", code)
	}
	if code, _ := call(t, bob, "DELETE", base+"/api/devices/"+itoa(int(created.Device.ID)), ""); code != 404 {
		t.Fatalf("bob revoked alice's device: %d", code)
	}
	if code, _ := call(t, bob, "GET", base+"/api/players/nobody/matches", ""); code != 404 {
		t.Fatalf("unknown player: %d", code)
	}

	_, body = call(t, bob, "GET", base+"/api/players", "")
	var ps []playerJSON
	if json.Unmarshal([]byte(body), &ps) != nil || len(ps) != 2 {
		t.Fatalf("players %s", body)
	}
	for _, p := range ps {
		if p.Handle == "alice" && (!p.Online || !p.InMatch || p.Matches != 1 || p.IsMe || len(p.GameNames) != 1) {
			t.Fatalf("alice entry %+v", p)
		}
		if p.Handle == "bob" && !p.IsMe {
			t.Fatalf("bob entry %+v", p)
		}
	}
	_, body = call(t, bob, "GET", base+"/api/leaderboard?mode=1v1&days=30", "")
	if !strings.Contains(body, `"handle":"alice"`) || !strings.Contains(body, `"winrate":1`) || !strings.Contains(body, `"goals_avg":2`) {
		t.Fatalf("leaderboard %s", body)
	}
	if code, _ := call(t, bob, "GET", base+"/api/leaderboard?mode=5v5", ""); code != 400 {
		t.Fatalf("bad mode: %d", code)
	}

	// Settings are per user.
	if code, body := call(t, bob, "PUT", base+"/api/config", `{"goal":{"mode":"2v2","daily":4}}`); code != 200 || !strings.Contains(body, `"daily":4`) {
		t.Fatalf("bob config %d %s", code, body)
	}
	if _, body := call(t, alice, "GET", base+"/api/config", ""); !strings.Contains(body, `"daily":10`) || !strings.Contains(body, `"AliceRL"`) {
		t.Fatalf("alice config %s", body)
	}
	if _, body := call(t, alice, "GET", base+"/api/players/bob/config", ""); body != `{"goal":{"mode":"2v2","daily":4,"season_start":"","season_end":""}}`+"\n" {
		t.Fatalf("bob public config %s", body)
	}

	// Revoking the device cuts the agent off.
	if code, _ := call(t, alice, "DELETE", base+"/api/devices/"+itoa(int(created.Device.ID)), ""); code != 204 {
		t.Fatalf("revoke %d", code)
	}
	if code, _ := call(t, agent, "POST", base+"/api/agent/heartbeat", `{}`, auth...); code != 401 {
		t.Fatalf("revoked token: %d", code)
	}
	if _, body := call(t, alice, "GET", base+"/api/status", ""); strings.Contains(body, `"in_match":true`) {
		t.Fatalf("revoked device still live: %s", body)
	}

	// Logout ends the session.
	if code, _ := call(t, alice, "POST", base+"/auth/logout", ""); code != 303 {
		t.Fatalf("logout %d", code)
	}
	if code, _ := call(t, alice, "GET", base+"/api/session", ""); code != 401 {
		t.Fatalf("after logout %d", code)
	}
}

// fakeIdP is a minimal OpenID provider: discovery, JWKS, authorize (instant
// consent), token endpoint signing RS256 ID tokens.
type fakeIdP struct {
	srv    *httptest.Server
	key    *rsa.PrivateKey
	codes  map[string]string // code -> nonce
	groups []string
	sub    string
}

func newFakeIdP(t *testing.T) *fakeIdP {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	p := &fakeIdP{key: key, codes: map[string]string{}, sub: "sub-123", groups: []string{"gamers"}}
	mux := http.NewServeMux()
	p.srv = httptest.NewServer(mux)
	t.Cleanup(p.srv.Close)
	iss := p.srv.URL
	mux.HandleFunc("/.well-known/openid-configuration", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"issuer": iss, "authorization_endpoint": iss + "/authorize", "token_endpoint": iss + "/token",
			"jwks_uri": iss + "/jwks", "userinfo_endpoint": iss + "/userinfo", "id_token_signing_alg_values_supported": []string{"RS256"},
		})
	})
	mux.HandleFunc("/jwks", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(jose.JSONWebKeySet{Keys: []jose.JSONWebKey{{Key: &key.PublicKey, KeyID: "k1", Algorithm: "RS256", Use: "sig"}}})
	})
	mux.HandleFunc("/authorize", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		if q.Get("code_challenge_method") != "S256" || q.Get("code_challenge") == "" {
			http.Error(w, "pkce required", 400)
			return
		}
		code := "code-" + q.Get("state")[:8]
		p.codes[code] = q.Get("nonce")
		http.Redirect(w, r, q.Get("redirect_uri")+"?code="+code+"&state="+url.QueryEscape(q.Get("state")), http.StatusFound)
	})
	mux.HandleFunc("/token", func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		nonce, ok := p.codes[r.Form.Get("code")]
		id, secret, _ := r.BasicAuth()
		if !ok || r.Form.Get("code_verifier") == "" || id != "rt" || secret != "s3cret" {
			http.Error(w, `{"error":"invalid_grant"}`, 400)
			return
		}
		sig, _ := jose.NewSigner(jose.SigningKey{Algorithm: jose.RS256, Key: key}, (&jose.SignerOptions{}).WithHeader("kid", "k1"))
		raw, _ := jwt.Signed(sig).Claims(map[string]any{
			"iss": iss, "sub": p.sub, "aud": "rt", "exp": time.Now().Add(time.Minute).Unix(), "iat": time.Now().Unix(),
			"nonce": nonce, "preferred_username": "virgile", "name": "Virgile", "email": "v@example.com", "groups": p.groups,
		}).Serialize()
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"access_token": "at", "token_type": "Bearer", "id_token": raw, "expires_in": 60})
	})
	return p
}

// follow runs the redirect chain browser-side, returning the final response.
func follow(t *testing.T, c *http.Client, u string) *http.Response {
	t.Helper()
	for i := 0; i < 10; i++ {
		resp, err := c.Get(u)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		loc := resp.Header.Get("Location")
		if resp.StatusCode != http.StatusFound || loc == "" {
			return resp
		}
		next, _ := resp.Request.URL.Parse(loc)
		u = next.String()
	}
	t.Fatal("redirect loop")
	return nil
}

func TestOIDCSignIn(t *testing.T) {
	idp := newFakeIdP(t)
	e := newHub(t, &OIDCConfig{Issuer: idp.srv.URL, ClientID: "rt", ClientSecret: "s3cret", AllowedGroups: []string{"gamers"}})
	c := e.browser(t)
	resp := follow(t, c, e.ts.URL+"/?from=test")
	if resp.StatusCode != 200 || resp.Request.URL.RequestURI() != "/?from=test" {
		t.Fatalf("landed on %d %s", resp.StatusCode, resp.Request.URL)
	}
	code, body := call(t, c, "GET", e.ts.URL+"/api/session", "")
	if code != 200 || !strings.Contains(body, `"handle":"virgile"`) || !strings.Contains(body, `"name":"Virgile"`) {
		t.Fatalf("session %d %s", code, body)
	}

	// A user outside the allowed groups is refused.
	idp.groups, idp.sub = []string{"family"}, "sub-456"
	c2 := e.browser(t)
	resp = follow(t, c2, e.ts.URL+"/")
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("outsider got %d", resp.StatusCode)
	}
	if code, _ := call(t, c2, "GET", e.ts.URL+"/api/session", ""); code != 401 {
		t.Fatal("outsider has a session")
	}
	if us, _ := e.st.Users(context.Background()); len(us) != 1 {
		t.Fatalf("users %d", len(us))
	}

	// A callback with a forged state is refused.
	c3 := e.browser(t)
	resp, _ = c3.Get(e.ts.URL + "/auth/callback?code=x&state=forged")
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("forged state: %d", resp.StatusCode)
	}
}

func TestHubTiltAlert(t *testing.T) {
	e := newHub(t, nil)
	alice := e.devLogin(t, "Alice")
	base := e.ts.URL
	code, body := call(t, alice, "POST", base+"/api/devices", `{"name":"PC"}`)
	var created struct {
		Token string `json:"token"`
	}
	if code != 201 || json.Unmarshal([]byte(body), &created) != nil {
		t.Fatalf("create device %d %s", code, body)
	}
	auth := []string{"Authorization", "Bearer " + created.Token, "Origin", ""}
	send := func(i int, result string) string {
		start := time.Now().Add(time.Duration(i*7-30) * time.Minute).UTC()
		m := fmt.Sprintf(`{"key":"k%d","match":{"guid":"G%d","online":true,"started_at":%q,"ended_at":%q,"mode":"1v1","result":%q,"team_score":1,"opp_score":2}}`,
			i, i, start.Format(time.RFC3339), start.Add(6*time.Minute).Format(time.RFC3339), result)
		code, body := call(t, &http.Client{}, "POST", base+"/api/agent/matches", m, auth...)
		if code != 200 {
			t.Fatalf("upload %d %s", code, body)
		}
		return body
	}
	send(0, "win")
	send(1, "loss")
	if body := send(2, "loss"); strings.Contains(body, `"alert"`) {
		t.Fatalf("alert after 2 losses: %s", body)
	}
	if body := send(3, "loss"); !strings.Contains(body, `"kind":"streak"`) || !strings.Contains(body, `"losses":3`) {
		t.Fatalf("no alert after 3 losses: %s", body)
	}
}

func TestHubSharedMatches(t *testing.T) {
	e := newHub(t, nil)
	alice, bob := e.devLogin(t, "Alice"), e.devLogin(t, "Bob")
	base := e.ts.URL
	token := func(c *http.Client) []string {
		_, body := call(t, c, "POST", base+"/api/devices", `{"name":"PC"}`)
		var created struct {
			Token string `json:"token"`
		}
		json.Unmarshal([]byte(body), &created)
		return []string{"Authorization", "Bearer " + created.Token, "Origin", ""}
	}
	start := time.Now().Add(-2 * time.Hour).UTC()
	send := func(auth []string, guid, name, pid string, team int, score int) {
		m := fmt.Sprintf(`{"key":"%s-%s","match":{"guid":%q,"online":true,"started_at":%q,"ended_at":%q,"mode":"2v2","result":"win","my_team":%d,
			"team_score":3,"opp_score":1,"me":{"name":%q,"primary_id":%q,"score":%d},"movement":{"avg_speed":1234}}}`,
			guid, name, guid, start.Format(time.RFC3339), start.Add(6*time.Minute).Format(time.RFC3339), team, name, pid, score)
		if code, body := call(t, &http.Client{}, "POST", base+"/api/agent/matches", m, auth...); code != 200 {
			t.Fatalf("upload %d %s", code, body)
		}
	}
	aa, ba := token(alice), token(bob)
	send(aa, "G1", "AliceRL", "Epic|a|0", 0, 500)
	send(ba, "G1", "BobRL", "Epic|b|0", 0, 300) // same team
	send(aa, "G2", "AliceRL", "Epic|a|0", 0, 200)

	code, body := call(t, alice, "GET", base+"/api/shared", "")
	var sh []struct {
		ID   int64 `json:"id"`
		With []struct {
			Handle    string `json:"handle"`
			ID        int64  `json:"id"`
			SameTeam  bool   `json:"same_team"`
			PrimaryID string `json:"primary_id"`
		} `json:"with"`
	}
	if code != 200 || json.Unmarshal([]byte(body), &sh) != nil || len(sh) != 1 || len(sh[0].With) != 1 ||
		sh[0].With[0].Handle != "bob" || !sh[0].With[0].SameTeam || sh[0].With[0].PrimaryID != "Epic|b|0" {
		t.Fatalf("shared %d %s", code, body)
	}
	// Another player's view, read-only.
	if code, body := call(t, alice, "GET", base+"/api/players/bob/shared", ""); code != 200 || !strings.Contains(body, `"handle":"alice"`) {
		t.Fatalf("bob's shared %d %s", code, body)
	}
	code, body = call(t, alice, "GET", fmt.Sprintf("%s/api/matches/%d/shared", base, sh[0].ID), "")
	if code != 200 || !strings.Contains(body, `"handle":"bob"`) || !strings.Contains(body, `"score":300`) || !strings.Contains(body, `"avg_speed":1234`) {
		t.Fatalf("copies %d %s", code, body)
	}
	code, body = call(t, alice, "GET", fmt.Sprintf("%s/api/players/bob/matches/%d/shared", base, sh[0].With[0].ID), "")
	if code != 200 || !strings.Contains(body, `"handle":"alice"`) || !strings.Contains(body, `"score":500`) {
		t.Fatalf("bob's copies %d %s", code, body)
	}
	if code, _ := call(t, bob, "GET", fmt.Sprintf("%s/api/matches/%d/shared", base, sh[0].ID), ""); code != 404 {
		t.Fatalf("someone else's match id: %d", code)
	}
}
