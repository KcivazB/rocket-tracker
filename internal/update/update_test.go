package update

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestIsNewer(t *testing.T) {
	for _, c := range []struct {
		a, b string
		want bool
	}{
		{"0.4.3", "0.4.2", true},
		{"v0.5.0", "0.4.9", true},
		{"0.10.0", "0.9.0", true},
		{"1.0.0", "0.99.99", true},
		{"0.4.2", "0.4.2", false},
		{"0.4.1", "0.4.2", false},
		{"v0.4.2", "0.4.2", false},
		{"dev", "0.4.2", false},
		{"0.5.0", "dev", false},
		{"0.5", "0.4.2", false},
	} {
		if got := IsNewer(c.a, c.b); got != c.want {
			t.Errorf("IsNewer(%q, %q) = %v, want %v", c.a, c.b, got, c.want)
		}
	}
}

func TestCheckerNotifiesOnce(t *testing.T) {
	tag := "v0.5.0"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"tag_name":"` + tag + `","html_url":"https://example.com/r","draft":false,"prerelease":false}`))
	}))
	defer srv.Close()

	var got []Release
	c := &Checker{Current: "0.4.2", URL: srv.URL, OnNewer: func(r Release) { got = append(got, r) }}
	ctx := context.Background()
	c.check(ctx)
	c.check(ctx)
	if len(got) != 1 || got[0].Version != "0.5.0" || got[0].URL != "https://example.com/r" {
		t.Fatalf("notifications %+v", got)
	}
	if n := c.Newer(); n == nil || n.Version != "0.5.0" {
		t.Fatalf("Newer() = %+v", n)
	}

	tag = "v0.4.2" // same version: nothing new
	c2 := &Checker{Current: "0.4.2", URL: srv.URL, OnNewer: func(Release) { t.Error("notified for the running version") }}
	c2.check(ctx)
	if c2.Newer() != nil {
		t.Fatal("Newer() should be nil")
	}
}
