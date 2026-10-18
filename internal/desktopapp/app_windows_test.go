package desktopapp

import (
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"

	"golang.org/x/sys/windows"

	"rocket-tracker/internal/config"
)

// Regression: the autostart command quoted --data-dir by hand, so a data dir
// ending with a backslash (e.g. `D:\`) escaped the closing quote and the
// command line was parsed wrongly at login.
func TestAutostartCommandLineRoundTrip(t *testing.T) {
	exe := `C:\Program Files\Rocket Tracker\rltracker.exe`
	for _, dataDir := range []string{`D:\`, `C:\Users\Jean Dupont\AppData\Roaming\RocketTracker`, `E:\data "x"\`} {
		g := globals{dataDir: dataDir, port: 9000}
		want := append([]string{exe}, runArgs(dataDir, g)...)
		got, err := windows.DecomposeCommandLine(commandLine(exe, runArgs(dataDir, g)))
		if err != nil {
			t.Fatal(err)
		}
		if !slices.Equal(got, want) {
			t.Fatalf("data dir %q:\n got  %q\n want %q", dataDir, got, want)
		}
	}
	if got := commandLine(exe, runArgs(`C:\x`, globals{})); got != `"`+exe+`" run` {
		t.Fatalf("default command %q", got)
	}
}

func TestDashboardURLFallsBackToLocal(t *testing.T) {
	const local = "http://localhost:8765"
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized) // signed out: the server answers
	}))
	defer up.Close()
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadGateway) // reverse proxy in front of a stopped server
	}))
	defer proxy.Close()
	down := httptest.NewServer(http.NotFoundHandler())
	down.Close()

	for _, c := range []struct {
		server    string
		reachable bool
		want      string
	}{
		{"", false, local},
		{up.URL, false, up.URL},
		{proxy.URL, false, local},
		{down.URL, false, local},
		{down.URL, true, down.URL}, // the running agent just heard from it
	} {
		if got := dashboardURL(c.server, local, c.reachable); got != c.want {
			t.Errorf("dashboardURL(%q, reachable=%v) = %q, want %q", c.server, c.reachable, got, c.want)
		}
	}
}

func TestNeedsSetup(t *testing.T) {
	for _, c := range []struct {
		done, ini, autostart, want bool
	}{
		{false, false, false, true},
		{false, true, false, true},
		{false, false, true, true},
		{false, true, true, false}, // installed with `rltracker setup` before the wizard existed
		{true, false, false, false},
	} {
		if got := needsSetup(config.Config{SetupDone: c.done}, c.ini, c.autostart); got != c.want {
			t.Errorf("needsSetup(done=%v, ini=%v, autostart=%v) = %v", c.done, c.ini, c.autostart, got)
		}
	}
}
