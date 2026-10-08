package main

import (
	"slices"
	"testing"

	"golang.org/x/sys/windows"
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
