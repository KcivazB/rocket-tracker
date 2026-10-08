package setup

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
)

// IsInstallDir reports whether dir looks like a Rocket League install.
func IsInstallDir(dir string) bool {
	if dir == "" {
		return false
	}
	for _, p := range []string{
		filepath.Join(dir, "Binaries", "Win64", "RocketLeague.exe"),
		filepath.Join(dir, "TAGame", "Config"),
	} {
		if _, err := os.Stat(p); err == nil {
			return true
		}
	}
	return false
}

// Candidates lists install directories to probe, override first.
func Candidates(override string) []string {
	var c []string
	if override != "" {
		c = append(c, override)
	}
	c = append(c, epicManifestDirs()...)
	for _, drive := range []string{"C", "D", "E", "F", "G"} {
		root := drive + `:\`
		c = append(c,
			filepath.Join(root, "Program Files", "Epic Games", "rocketleague"),
			filepath.Join(root, "Epic Games", "rocketleague"),
			filepath.Join(root, "Games", "Epic Games", "rocketleague"),
			filepath.Join(root, "Program Files (x86)", "Steam", "steamapps", "common", "rocketleague"),
			filepath.Join(root, "Program Files", "Steam", "steamapps", "common", "rocketleague"),
			filepath.Join(root, "SteamLibrary", "steamapps", "common", "rocketleague"),
			filepath.Join(root, "Steam", "steamapps", "common", "rocketleague"),
			filepath.Join(root, "Games", "SteamLibrary", "steamapps", "common", "rocketleague"),
		)
	}
	seen := map[string]bool{}
	out := c[:0]
	for _, d := range c {
		k := strings.ToLower(filepath.Clean(d))
		if !seen[k] {
			seen[k] = true
			out = append(out, d)
		}
	}
	return out
}

// epicManifestDirs reads Epic Games Launcher manifests for Rocket League.
func epicManifestDirs() []string {
	pd := os.Getenv("ProgramData")
	if pd == "" {
		pd = `C:\ProgramData`
	}
	files, _ := filepath.Glob(filepath.Join(pd, "Epic", "EpicGamesLauncher", "Data", "Manifests", "*.item"))
	var out []string
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			continue
		}
		var m struct {
			DisplayName     string `json:"DisplayName"`
			AppName         string `json:"AppName"`
			InstallLocation string `json:"InstallLocation"`
		}
		if json.Unmarshal(b, &m) != nil || m.InstallLocation == "" {
			continue
		}
		if strings.Contains(strings.ToLower(m.DisplayName), "rocket league") || strings.EqualFold(m.AppName, "Sugar") {
			out = append(out, m.InstallLocation)
		}
	}
	return out
}

// FindInstallDir returns the first valid install dir, or "".
func FindInstallDir(override string) string {
	for _, d := range Candidates(override) {
		if IsInstallDir(d) {
			return d
		}
	}
	return ""
}
