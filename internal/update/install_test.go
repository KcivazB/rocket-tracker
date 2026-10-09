package update

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func TestInstall(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("new build"))
	}))
	defer srv.Close()
	exe := filepath.Join(t.TempDir(), "rltracker.exe")
	if err := os.WriteFile(exe, []byte("old build"), 0o755); err != nil {
		t.Fatal(err)
	}
	r := Release{Version: "0.6.0", Download: srv.URL}
	ctx := context.Background()

	// Rejected by verify: nothing changes.
	err := Install(ctx, nil, r, exe, func(string) error { return errors.New("bad") })
	if err == nil {
		t.Fatal("verify error ignored")
	}
	if b, _ := os.ReadFile(exe); string(b) != "old build" {
		t.Fatalf("exe changed: %q", b)
	}
	if _, err := os.Stat(exe + ".new"); !os.IsNotExist(err) {
		t.Fatal("download left behind")
	}

	var checked string
	if err := Install(ctx, nil, r, exe, func(p string) error { checked = p; return nil }); err != nil {
		t.Fatal(err)
	}
	if checked != exe+".new" {
		t.Fatalf("verified %q", checked)
	}
	if b, _ := os.ReadFile(exe); string(b) != "new build" {
		t.Fatalf("exe = %q", b)
	}
	if b, _ := os.ReadFile(exe + ".old"); string(b) != "old build" {
		t.Fatalf("old = %q", b)
	}
	CleanupOld(exe)
	if _, err := os.Stat(exe + ".old"); !os.IsNotExist(err) {
		t.Fatal(".old not removed")
	}
}

func TestFetchFindsTheExe(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"tag_name":"v0.6.0","html_url":"https://example.com/r","assets":[
			{"name":"rltracker-server-linux-amd64","browser_download_url":"https://example.com/server"},
			{"name":"rltracker.exe","browser_download_url":"https://example.com/exe"}]}`))
	}))
	defer srv.Close()
	r, err := (&Checker{Current: "0.5.0", URL: srv.URL}).fetch(context.Background())
	if err != nil || r.Download != "https://example.com/exe" {
		t.Fatalf("%+v %v", r, err)
	}
}
