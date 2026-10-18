package server

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"unsafe"
)

func TestOAuthReturnPathRetentionBound(t *testing.T) {
	idp := newSecurityIDP(t, securityIDPOptions{})
	e := newHub(t, &OIDCConfig{Issuer: idp.srv.URL, ClientID: "rt", ClientSecret: "s3cret"})
	for _, tc := range []struct{ next, pad, want string }{
		{"/" + strings.Repeat("a", 512*1024), "", "/"},
		{"/ok", strings.Repeat("x", 512*1024), "/ok"},
		{"/" + strings.Repeat("a", 2047), "", "/" + strings.Repeat("a", 2047)},
		{"/" + strings.Repeat("a", 2048), "", "/"},
	} {
		raw := "next=" + tc.next + "&pad=" + tc.pad
		req := httptest.NewRequest("GET", e.ts.URL+"/auth/login?"+raw, nil)
		rec := httptest.NewRecorder()
		e.s.Handler().ServeHTTP(rec, req)
		if rec.Code != http.StatusFound {
			t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
		}
		o := e.s.Hub.OIDC
		o.mu.Lock()
		if len(o.transactions) != 1 {
			o.mu.Unlock()
			t.Fatal("unexpected transactions")
		}
		for binding, tx := range o.transactions {
			if tx.state.Next != tc.want {
				o.mu.Unlock()
				t.Fatalf("retained path len=%d want len=%d", len(tx.state.Next), len(tc.want))
			}
			// Test-only storage identity: a short substring must not retain a padded query.
			base := uintptr(unsafe.Pointer(unsafe.StringData(req.URL.RawQuery)))
			next := uintptr(unsafe.Pointer(unsafe.StringData(tx.state.Next)))
			if next >= base && next < base+uintptr(len(req.URL.RawQuery)) {
				o.mu.Unlock()
				t.Fatal("transaction retains untrusted query backing")
			}
			delete(o.transactions, binding)
		}
		o.mu.Unlock()
	}
}
