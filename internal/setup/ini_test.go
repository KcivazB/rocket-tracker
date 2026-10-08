package setup

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPatchIniCreatesWhenEmpty(t *testing.T) {
	got := PatchIni("", 30, 49123, 49124)
	want := "[TAGame.MatchStatsExporter_TA]\r\nPacketSendRate=30\r\nPort=49123\r\nWebPort=49124\r\n"
	if got != want {
		t.Fatalf("got %q", got)
	}
	v := ParseIni(got)
	if !v.HasRate || v.PacketSendRate != 30 || v.Port != 49123 || v.WebPort != 49124 {
		t.Fatalf("%+v", v)
	}
}

func TestPatchIniPreservesOtherContent(t *testing.T) {
	in := "; header comment\r\n[Core.System]\r\nPaths=..\\Foo\r\n\r\n[TAGame.MatchStatsExporter_TA]\r\n; keep me\r\nPacketSendRate=0\r\nExtraKey=1\r\n\r\n[Other.Section]\r\nPort=1234\r\nZ=9\r\n"
	got := PatchIni(in, 60, 49123, 49124)
	want := "; header comment\r\n[Core.System]\r\nPaths=..\\Foo\r\n\r\n[TAGame.MatchStatsExporter_TA]\r\n; keep me\r\nPacketSendRate=60\r\nExtraKey=1\r\nPort=49123\r\nWebPort=49124\r\n\r\n[Other.Section]\r\nPort=1234\r\nZ=9\r\n"
	if got != want {
		t.Fatalf("got:\n%s\nwant:\n%s", got, want)
	}
	// Idempotent.
	if again := PatchIni(got, 60, 49123, 49124); again != got {
		t.Fatalf("not idempotent:\n%s", again)
	}
	v := ParseIni(got)
	if v.PacketSendRate != 60 || v.Port != 49123 {
		t.Fatalf("parse %+v (exporter section must win over Other.Section Port)", v)
	}
}

func TestPatchIniLFAndBOMAndCase(t *testing.T) {
	in := "\xef\xbb\xbf[tagame.matchstatsexporter_ta]\npacketsendrate = 0\n"
	got := PatchIni(in, 30, 49123, 49124)
	if !strings.HasPrefix(got, "\xef\xbb\xbf") || strings.Contains(got, "\r\n") {
		t.Fatalf("BOM / LF not preserved: %q", got)
	}
	if !strings.Contains(got, "packetsendrate=30\n") || strings.Count(got, "[") != 1 {
		t.Fatalf("got %q", got)
	}
}

func TestPatchIniUsesSectionHoldingRate(t *testing.T) {
	in := "[SomeOther.Name]\nPacketSendRate=0\n\n[Foo]\nA=1\n"
	got := PatchIni(in, 30, 49123, 49124)
	want := "[SomeOther.Name]\nPacketSendRate=30\nPort=49123\nWebPort=49124\n\n[Foo]\nA=1\n"
	if got != want {
		t.Fatalf("got %q", got)
	}
}

func TestPatchIniAppendsSection(t *testing.T) {
	in := "[Foo]\nA=1"
	got := PatchIni(in, 30, 49123, 49124)
	want := "[Foo]\nA=1\n\n[TAGame.MatchStatsExporter_TA]\nPacketSendRate=30\nPort=49123\nWebPort=49124\n"
	if got != want {
		t.Fatalf("got %q", got)
	}
}

func TestParseIniAnySection(t *testing.T) {
	v := ParseIni("[X]\nPacketSendRate=15.5\n")
	if !v.HasRate || v.PacketSendRate != 15.5 {
		t.Fatalf("%+v", v)
	}
	v = ParseIni("[X]\nPacketSendRate=15\n[TAGame.MatchStatsExporter_TA]\nPacketSendRate=0\n")
	if v.PacketSendRate != 0 {
		t.Fatalf("exporter section must win: %+v", v)
	}
	if v := ParseIni("garbage\n=\n[unterminated\n"); v.HasRate {
		t.Fatalf("%+v", v)
	}
}

func TestEnableStatsAPIFiles(t *testing.T) {
	install := t.TempDir()
	dir := ConfigDir(install)

	// Nothing exists: DefaultStatsAPI.ini is created, TAStatsAPI.ini is not.
	if _, err := EnableStatsAPI(install, 30, 49123, 49124); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, TAIniName)); !os.IsNotExist(err) {
		t.Fatal("TAStatsAPI.ini must not be created")
	}
	st := CheckIni(install)
	if !st.Found || !st.OK || st.PacketSendRate != 30 || filepath.Base(st.Path) != DefaultIniName {
		t.Fatalf("%+v", st)
	}

	// TAStatsAPI.ini exists with other content and rate 0: patched too, and it
	// becomes the effective file.
	ta := filepath.Join(dir, TAIniName)
	if err := os.WriteFile(ta, []byte("[Keep]\nX=1\n[TAGame.MatchStatsExporter_TA]\nPacketSendRate=0\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if st := CheckIni(install); st.OK || filepath.Base(st.Path) != TAIniName {
		t.Fatalf("before: %+v", st)
	}
	written, err := EnableStatsAPI(install, 0 /* invalid => default 30 */, 49123, 49124)
	if err != nil || len(written) != 2 {
		t.Fatalf("written %v err %v", written, err)
	}
	b, _ := os.ReadFile(ta)
	if !strings.Contains(string(b), "[Keep]\nX=1\n") || !strings.Contains(string(b), "PacketSendRate=30") {
		t.Fatalf("TA content %q", b)
	}
	if st := CheckIni(install); !st.OK || filepath.Base(st.Path) != TAIniName {
		t.Fatalf("after: %+v", st)
	}
	if !IsInstallDir(install) {
		t.Fatal("dir with TAGame\\Config should be an install dir")
	}
	if CheckIni("").Found {
		t.Fatal("empty install dir")
	}
}

// Regression: a UTF-16 ini (common for Unreal Engine configs) was patched as
// bytes, appending ASCII to a UTF-16 file and corrupting it.
func TestEnableStatsAPIUTF16(t *testing.T) {
	install := t.TempDir()
	dir := ConfigDir(install)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(dir, DefaultIniName)
	orig := "[Other]\r\nName=Été\r\n[TAGame.MatchStatsExporter_TA]\r\nPacketSendRate=0\r\n"
	if err := os.WriteFile(p, encodeIni(orig, encUTF16LE), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := EnableStatsAPI(install, 30, 49123, 49124); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(p)
	if b[0] != 0xFF || b[1] != 0xFE {
		t.Fatalf("BOM lost: % x", b[:4])
	}
	text, enc := decodeIni(b)
	if enc != encUTF16LE || !strings.Contains(text, "[Other]\r\nName=Été\r\n") || !strings.Contains(text, "PacketSendRate=30\r\n") ||
		strings.Count(text, "[TAGame.MatchStatsExporter_TA]") != 1 {
		t.Fatalf("patched UTF-16 content %q", text)
	}
	if st := CheckIni(install); !st.OK || st.PacketSendRate != 30 || st.Port != 49123 {
		t.Fatalf("check %+v", st)
	}
}

// The ini is replaced atomically: a failed write leaves the original intact,
// reports a permission error (so setup elevates) and no temp file behind.
func TestEnableStatsAPIAtomicOnFailure(t *testing.T) {
	install := t.TempDir()
	dir := ConfigDir(install)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(dir, DefaultIniName)
	orig := "[Keep]\nX=1\n"
	if err := os.WriteFile(p, []byte(orig), 0o444); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(p, 0o644) })
	_, err := EnableStatsAPI(install, 30, 49123, 49124)
	if err == nil || !IsPermission(err) {
		t.Fatalf("expected a permission error, got %v", err)
	}
	if b, _ := os.ReadFile(p); string(b) != orig {
		t.Fatalf("original modified: %q", b)
	}
	ents, _ := os.ReadDir(dir)
	if len(ents) != 1 {
		t.Fatalf("leftover files: %v", ents)
	}
	_ = os.Chmod(p, 0o644)
	if _, err := EnableStatsAPI(install, 30, 49123, 49124); err != nil {
		t.Fatal(err)
	}
	if ents, _ := os.ReadDir(dir); len(ents) != 1 {
		t.Fatalf("leftover files after success: %v", ents)
	}
}
