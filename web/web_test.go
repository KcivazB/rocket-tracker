package web

import (
	"bytes"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"slices"
	"testing"
)

func TestFrontend(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Fatal("Node.js is required for frontend tests; CI uses Node 22")
	}
	for _, script := range []string{"i18n.test.js", "analytics.test.js", "profiles.test.js", "polling.test.js"} {
		t.Run(script, func(t *testing.T) {
			out, err := exec.Command(node, "tests/"+script).CombinedOutput()
			if err != nil {
				t.Fatalf("%v\n%s", err, out)
			}
			t.Log(string(out))
		})
	}
}

func TestEmbeddedAssets(t *testing.T) {
	assets := []string{
		"app.js", "i18n.js", "index.html", "setup.html", "setup.js", "style.css",
		"vendor/chart.umd.min.js",
	}
	var embedded []string
	err := fs.WalkDir(FS, ".", func(name string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !entry.IsDir() {
			embedded = append(embedded, name)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(embedded, assets) {
		t.Errorf("embedded files = %v, want only runtime assets %v", embedded, assets)
	}

	// Exercise the static-file boundary in memory, without a listening server.
	handler := http.FileServerFS(FS)
	for _, name := range assets {
		t.Run(name, func(t *testing.T) {
			data, err := FS.ReadFile(name)
			if err != nil {
				t.Fatal(err)
			}
			if len(data) == 0 {
				t.Fatal("runtime asset is empty")
			}
			url := "/" + name
			if name == "index.html" {
				url = "/" // FileServer redirects /index.html to /.
			}
			w := httptest.NewRecorder()
			handler.ServeHTTP(w, httptest.NewRequest(http.MethodGet, url, nil))
			if w.Code != http.StatusOK || !bytes.Equal(w.Body.Bytes(), data) {
				t.Fatalf("GET %s: status %d, body differs from embedded asset", url, w.Code)
			}
		})
	}
	for _, url := range []string{
		"/tests/i18n.test.js", "/tests/analytics.test.js", "/tests/profiles.test.js", "/tests/polling.test.js", "/tests/", "/fixtures/",
		"/embed.go", "/web_test.go",
	} {
		t.Run(url, func(t *testing.T) {
			w := httptest.NewRecorder()
			handler.ServeHTTP(w, httptest.NewRequest(http.MethodGet, url, nil))
			if w.Code != http.StatusNotFound {
				t.Fatalf("GET %s: status %d, want 404", url, w.Code)
			}
		})
	}
}
