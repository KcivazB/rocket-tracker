package server

import (
	"context"
	"errors"
	"fmt"
	"html"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	"golang.org/x/oauth2"

	"rocket-tracker/internal/i18n"
	"rocket-tracker/internal/store"
)

// OIDCConfig describes the OpenID Connect client registered at the identity
// provider (Authentik, Keycloak, Authelia...). Its redirect URI must be
// <public URL>/auth/callback.
type OIDCConfig struct {
	Issuer        string // e.g. https://auth.example.com/application/o/rocket-tracker/
	ClientID      string
	ClientSecret  string
	RedirectURL   string   // <public URL>/auth/callback
	AllowedGroups []string // empty: every user the provider lets through
}

// OIDC signs users in with the authorization code flow (PKCE, state, nonce).
// The provider is discovered on first use, so the server starts even while
// the identity provider is down.
type OIDC struct {
	cfg OIDCConfig

	mu           sync.Mutex
	provider     *oidc.Provider
	transactions map[string]oauthTransaction
}

// NewOIDC returns a client for cfg.
func NewOIDC(cfg OIDCConfig) *OIDC { return &OIDC{cfg: cfg} }

func (o *OIDC) setup() (*oidc.Provider, *oauth2.Config, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.provider == nil {
		// The context is kept by go-oidc to refresh the signing keys later:
		// it must not be cancelled.
		ctx := oidc.ClientContext(context.Background(), &http.Client{Timeout: 15 * time.Second})
		p, err := oidc.NewProvider(ctx, o.cfg.Issuer)
		if err != nil {
			return nil, nil, err
		}
		o.provider = p
	}
	return o.provider, &oauth2.Config{
		ClientID:     o.cfg.ClientID,
		ClientSecret: o.cfg.ClientSecret,
		RedirectURL:  o.cfg.RedirectURL,
		Endpoint:     o.provider.Endpoint(),
		Scopes:       []string{oidc.ScopeOpenID, "profile", "email"},
	}, nil
}

const oauthCookie = "rt_oauth"

// oauthState stays server-side; only an opaque browser binding travels in a cookie.
type oauthState struct {
	State    string `json:"s"`
	Nonce    string `json:"n"`
	Verifier string `json:"v"`
	Next     string `json:"next"`
}

const oauthLifetime = 10 * time.Minute

type oauthTransaction struct {
	state     oauthState
	namespace string
	expires   time.Time
}

func (o *OIDC) begin(h *Hub, previous string, st oauthState) (string, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	now := time.Now()
	for key, tx := range o.transactions {
		if !now.Before(tx.expires) {
			delete(o.transactions, key)
		}
	}
	if o.transactions == nil {
		o.transactions = make(map[string]oauthTransaction)
	}
	delete(o.transactions, previous)
	if len(o.transactions) >= 4096 {
		return "", errors.New("too many pending sign-ins")
	}
	binding := store.NewToken("")
	o.transactions[binding] = oauthTransaction{state: st, namespace: h.origin() + "/" + h.cookieName(oauthCookie), expires: now.Add(oauthLifetime)}
	return binding, nil
}

// consume removes the transaction before any provider call, including failures.
func (o *OIDC) consume(h *Hub, binding string) (oauthState, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	tx, ok := o.transactions[binding]
	delete(o.transactions, binding)
	if !ok || !time.Now().Before(tx.expires) || tx.namespace != h.origin()+"/"+h.cookieName(oauthCookie) {
		return oauthState{}, errors.New("expired or unbound sign-in")
	}
	return tx.state, nil
}

// safeNext keeps only local paths (no open redirect). Browsers drop tabs and
// newlines from URLs and read "\" as "/", so "/\t/evil.com" would lead away:
// control characters and backslashes are refused anywhere.
func safeNext(next string) string {
	if len(next) > 2048 || !strings.HasPrefix(next, "/") || strings.HasPrefix(next, "//") || strings.HasPrefix(next, "/auth/") ||
		strings.ContainsFunc(next, func(c rune) bool { return c < 0x20 || c == 0x7f || c == '\\' }) {
		return "/"
	}
	if u, err := url.Parse(next); err != nil || u.Scheme != "" || u.Host != "" {
		return "/"
	}
	// Query parsing may return a substring backed by the entire request query.
	return strings.Clone(next)
}

func (s *Server) login(w http.ResponseWriter, r *http.Request) {
	next := safeNext(r.URL.Query().Get("next"))
	t := pageText(r)
	if s.Hub.OIDC == nil {
		if s.Hub.DevLogin {
			authPage(w, r, http.StatusOK, t("auth.devTitle"),
				`<form action="/auth/dev" method="get"><input type="hidden" name="next" value="`+html.EscapeString(next)+`">`+
					`<p><label>`+t("auth.devPlayer")+` <input name="user" required autofocus></label> <button>`+t("auth.devSubmit")+`</button></p></form>`+
					`<p class="muted">`+t("auth.devNote")+`</p>`)
			return
		}
		authPage(w, r, http.StatusInternalServerError, t("auth.unavailable"), "<p>"+t("auth.noProvider")+"</p>")
		return
	}
	_, oc, err := s.Hub.OIDC.setup()
	if err != nil {
		s.Log.Error("oidc discovery failed", "issuer", s.Hub.OIDC.cfg.Issuer, "err", err)
		authPage(w, r, http.StatusBadGateway, t("auth.idpDown"),
			`<p>`+t("auth.idpDownText")+`</p><p><a class="btn" href="/auth/login?next=`+
				html.EscapeString(next)+`">`+t("auth.retry")+`</a></p>`)
		return
	}
	st := oauthState{State: store.NewToken(""), Nonce: store.NewToken(""), Verifier: oauth2.GenerateVerifier(), Next: next}
	previous, _ := s.Hub.securityCookieValue(r, oauthCookie)
	binding, err := s.Hub.OIDC.begin(s.Hub, previous, st)
	if err != nil {
		authPage(w, r, http.StatusServiceUnavailable, t("auth.unavailable"), "<p>"+t("auth.retry")+"</p>")
		return
	}
	http.SetCookie(w, s.Hub.securityCookie(oauthCookie, binding, int(oauthLifetime.Seconds())))
	http.Redirect(w, r, oc.AuthCodeURL(st.State, oidc.Nonce(st.Nonce), oauth2.S256ChallengeOption(st.Verifier)), http.StatusFound)
}

// idClaims are the claims read from the ID token (and userinfo as fallback).
type idClaims struct {
	PreferredUsername string   `json:"preferred_username"`
	Nickname          string   `json:"nickname"`
	Name              string   `json:"name"`
	Email             string   `json:"email"`
	Groups            []string `json:"groups"`
}

func (s *Server) callback(w http.ResponseWriter, r *http.Request) {
	t := pageText(r)
	retry := `<p><a class="btn" href="/auth/login">` + t("auth.retry") + `</a></p>`
	// fail shows titleKey / msg (msgKey translated, or raw text from the provider).
	fail := func(code int, titleKey, msg string, err error) {
		if err != nil {
			s.Log.Warn("sign-in failed", "reason", titleKey, "err", err)
		}
		authPage(w, r, code, t(titleKey), "<p>"+html.EscapeString(t(msg))+"</p>"+retry)
	}
	if s.Hub.OIDC == nil {
		fail(http.StatusNotFound, "auth.unavailable", "auth.noProvider", nil)
		return
	}
	var st oauthState
	binding, err := s.Hub.securityCookieValue(r, oauthCookie)
	if err == nil {
		st, err = s.Hub.OIDC.consume(s.Hub, binding)
	}
	http.SetCookie(w, s.Hub.securityCookie(oauthCookie, "", -1))
	q := r.URL.Query()
	if e := q.Get("error"); e != "" {
		msg := q.Get("error_description")
		if msg == "" {
			msg = e
		}
		fail(http.StatusForbidden, "auth.refused", msg, errors.New(e))
		return
	}
	if err != nil || st.State == "" || q.Get("state") != st.State {
		fail(http.StatusBadRequest, "auth.expired", "auth.expiredText", err)
		return
	}
	p, oc, err := s.Hub.OIDC.setup()
	if err != nil {
		fail(http.StatusBadGateway, "auth.idpDown", "auth.idpDownText", err)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	ctx = oidc.ClientContext(ctx, &http.Client{Timeout: 15 * time.Second})
	tok, err := oc.Exchange(ctx, q.Get("code"), oauth2.VerifierOption(st.Verifier))
	if err != nil {
		fail(http.StatusBadGateway, "auth.failed", "auth.codeRefused", err)
		return
	}
	raw, _ := tok.Extra("id_token").(string)
	idt, err := p.Verifier(&oidc.Config{ClientID: s.Hub.OIDC.cfg.ClientID}).Verify(ctx, raw)
	if err != nil {
		fail(http.StatusBadGateway, "auth.failed", "auth.tokenInvalid", err)
		return
	}
	if idt.Subject == "" {
		fail(http.StatusBadGateway, "auth.failed", "auth.tokenInvalid", errors.New("empty ID token subject"))
		return
	}
	if idt.Nonce != st.Nonce {
		fail(http.StatusBadRequest, "auth.failed", "auth.nonce", errors.New("nonce mismatch"))
		return
	}
	var cl idClaims
	if err := idt.Claims(&cl); err != nil {
		fail(http.StatusBadGateway, "auth.failed", "auth.claims", err)
		return
	}
	if cl.PreferredUsername == "" && cl.Name == "" || (len(s.Hub.OIDC.cfg.AllowedGroups) > 0 && cl.Groups == nil) {
		// Some providers only put the profile in the userinfo response.
		if ui, err := p.UserInfo(ctx, oauth2.StaticTokenSource(tok)); err == nil {
			if ui.Subject == "" || ui.Subject != idt.Subject {
				fail(http.StatusBadGateway, "auth.failed", "auth.claims", errors.New("userinfo subject mismatch"))
				return
			}
			var more idClaims
			if ui.Claims(&more) == nil {
				cl.PreferredUsername = firstNonEmpty(cl.PreferredUsername, more.PreferredUsername)
				cl.Nickname = firstNonEmpty(cl.Nickname, more.Nickname)
				cl.Name = firstNonEmpty(cl.Name, more.Name)
				cl.Email = firstNonEmpty(cl.Email, more.Email)
				if cl.Groups == nil {
					cl.Groups = more.Groups
				}
			}
		}
	}
	if gs := s.Hub.OIDC.cfg.AllowedGroups; len(gs) > 0 && !slices.ContainsFunc(cl.Groups, func(g string) bool { return slices.Contains(gs, g) }) {
		fail(http.StatusForbidden, "auth.denied", "auth.notInGroup",
			fmt.Errorf("user %q not in %v", cl.PreferredUsername, gs))
		return
	}
	username := firstNonEmpty(cl.PreferredUsername, cl.Nickname, cl.Email, cl.Name)
	u, err := s.Store.UpsertOIDCUser(ctx, idt.Subject, username, cl.Name, cl.Email)
	if err != nil {
		fail(http.StatusInternalServerError, "auth.failed", "auth.saveAccount", err)
		return
	}
	s.startSession(w, r, u, st.Next)
}

// firstNonEmpty returns the first non-empty string.
func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

func (s *Server) startSession(w http.ResponseWriter, r *http.Request, u *store.User, next string) {
	tok, err := s.Store.CreateSession(r.Context(), u.ID, s.Hub.ttl())
	if err != nil {
		s.Log.Error("create session", "err", err)
		t := pageText(r)
		authPage(w, r, http.StatusInternalServerError, t("auth.failed"), "<p>"+t("auth.session")+"</p>")
		return
	}
	s.Hub.setSessionCookie(w, tok)
	s.Log.Info("signed in", "user", u.Handle)
	http.Redirect(w, r, safeNext(next), http.StatusFound)
}

func (s *Server) logout(w http.ResponseWriter, r *http.Request) {
	if !s.sameOrigin(r) {
		writeErr(w, http.StatusForbidden, "cross-origin request refused")
		return
	}
	if tok, err := s.Hub.securityCookieValue(r, sessionCookie); err == nil {
		_ = s.Store.DeleteSession(r.Context(), tok)
	}
	http.SetCookie(w, s.Hub.securityCookie(sessionCookie, "", -1))
	http.Redirect(w, r, "/auth/logged-out", http.StatusSeeOther)
}

func (s *Server) loggedOut(w http.ResponseWriter, r *http.Request) {
	t := pageText(r)
	authPage(w, r, http.StatusOK, t("auth.signedOut"), `<p><a class="btn" href="/auth/login">`+t("auth.signInAgain")+`</a></p>`)
}

// devLogin signs in as any name (only with Hub.DevLogin, for local tests).
func (s *Server) devLogin(w http.ResponseWriter, r *http.Request) {
	name := strings.TrimSpace(r.URL.Query().Get("user"))
	if name == "" || len(name) > 40 {
		http.Redirect(w, r, "/auth/login", http.StatusFound)
		return
	}
	u, err := s.Store.UpsertOIDCUser(r.Context(), "dev:"+strings.ToLower(name), name, name, "")
	if err != nil {
		authPage(w, r, http.StatusInternalServerError, pageText(r)("auth.failed"), "<p>"+html.EscapeString(err.Error())+"</p>")
		return
	}
	s.startSession(w, r, u, r.URL.Query().Get("next"))
}

// pageText returns the translator of the sign-in pages, in the browser's
// language (French or English).
func pageText(r *http.Request) func(key string) string {
	l := i18n.FromAcceptLanguage(r.Header.Get("Accept-Language"))
	return func(key string) string { return i18n.T(l, key) }
}

// authPage renders the small standalone pages of the sign-in flow. title is
// text, body is HTML.
func authPage(w http.ResponseWriter, r *http.Request, code int, title, body string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Vary", "Accept-Language")
	w.WriteHeader(code)
	fmt.Fprintf(w, `<!doctype html><html lang="%s"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1">
<meta name="color-scheme" content="dark light"><title>%s · Rocket Tracker</title><link rel="stylesheet" href="/style.css"></head>
<body><main class="container auth-page"><div class="card auth-card"><div class="auth-brand"><svg class="brand-mark" viewBox="0 0 32 32" aria-hidden="true">
<path d="M9 22 L16 7 L23 22 L16 18 Z" fill="var(--us)"/><path d="M16 18 L23 22 L16 25 Z" fill="var(--them)"/></svg><span>Rocket Tracker</span></div>
<h1>%s</h1>%s</div></main></body></html>`, i18n.FromAcceptLanguage(r.Header.Get("Accept-Language")).Code(), html.EscapeString(title), html.EscapeString(title), body)
}
