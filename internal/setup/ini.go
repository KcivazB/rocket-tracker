// Package setup contains Rocket League install detection, Stats API ini
// patching, autostart and related helpers.
package setup

import (
	"encoding/binary"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"unicode/utf16"
)

// Section is the ini section of the Stats API exporter.
const Section = "TAGame.MatchStatsExporter_TA"

// Ini file names, relative to <install>\TAGame\Config.
const (
	TAIniName      = "TAStatsAPI.ini"
	DefaultIniName = "DefaultStatsAPI.ini"
)

// Defaults written by setup.
const (
	DefaultPacketSendRate = 30
	DefaultPort           = 49123
	DefaultWebPort        = 49124
)

// IniValues are the Stats API settings read from an ini file.
type IniValues struct {
	HasRate        bool
	PacketSendRate float64
	Port           int
	WebPort        int
}

type iniLine struct {
	raw     string
	section string // for key lines: owning section ("" before any header)
	key     string // lower-case key, "" when not a key line
	value   string
	header  string // section name when this line is a header
}

func parseLines(content string) []iniLine {
	content = strings.TrimPrefix(content, utf8BOM)
	content = strings.ReplaceAll(content, "\r\n", "\n")
	if content == "" {
		return nil
	}
	parts := strings.Split(content, "\n")
	if parts[len(parts)-1] == "" {
		parts = parts[:len(parts)-1]
	}
	out := make([]iniLine, 0, len(parts))
	sec := ""
	for _, raw := range parts {
		raw = strings.TrimRight(raw, "\r")
		l := iniLine{raw: raw}
		t := strings.TrimSpace(raw)
		switch {
		case strings.HasPrefix(t, "[") && strings.HasSuffix(t, "]"):
			sec = strings.TrimSpace(t[1 : len(t)-1])
			l.header = sec
		case t == "" || strings.HasPrefix(t, ";") || strings.HasPrefix(t, "#"):
		default:
			if i := strings.IndexByte(t, '='); i > 0 {
				l.key = strings.ToLower(strings.TrimSpace(t[:i]))
				l.value = strings.TrimSpace(t[i+1:])
				l.section = sec
			}
		}
		out = append(out, l)
	}
	return out
}

// ParseIni reads Stats API values. PacketSendRate is accepted under any
// section (the exporter section wins when present in several places).
func ParseIni(content string) IniValues {
	var v IniValues
	var inSectionRate bool
	for _, l := range parseLines(content) {
		if l.key == "" {
			continue
		}
		own := strings.EqualFold(l.section, Section)
		switch l.key {
		case "packetsendrate":
			f, err := strconv.ParseFloat(strings.TrimSpace(strings.Trim(l.value, `"`)), 64)
			if err != nil {
				continue
			}
			if own {
				v.PacketSendRate, v.HasRate, inSectionRate = f, true, true
			} else if !inSectionRate && !v.HasRate {
				v.PacketSendRate, v.HasRate = f, true
			}
		case "port":
			if n, err := strconv.Atoi(l.value); err == nil && (own || v.Port == 0) {
				v.Port = n
			}
		case "webport":
			if n, err := strconv.Atoi(l.value); err == nil && (own || v.WebPort == 0) {
				v.WebPort = n
			}
		}
	}
	return v
}

// PatchIni sets PacketSendRate/Port/WebPort in the exporter section,
// preserving every other line, section, comment, BOM and line ending style.
// When the exporter section is missing but another section already holds a
// PacketSendRate key, that section is patched instead; otherwise the section
// is appended.
func PatchIni(content string, rate float64, port, webPort int) string {
	bom := strings.HasPrefix(content, utf8BOM)
	nl := "\r\n"
	if strings.Contains(content, "\n") && !strings.Contains(content, "\r\n") {
		nl = "\n"
	}
	lines := parseLines(content)

	want := []struct{ key, name, val string }{
		{"packetsendrate", "PacketSendRate", strconv.FormatFloat(rate, 'f', -1, 64)},
		{"port", "Port", strconv.Itoa(port)},
		{"webport", "WebPort", strconv.Itoa(webPort)},
	}

	// Locate target section header index.
	target := -1
	for i, l := range lines {
		if l.header != "" && strings.EqualFold(l.header, Section) {
			target = i
			break
		}
	}
	if target < 0 {
		for i, l := range lines {
			if l.key == "packetsendrate" && l.section != "" {
				for j := i; j >= 0; j-- {
					if lines[j].header == l.section {
						target = j
						break
					}
				}
				break
			}
		}
	}

	var out []string
	if target < 0 {
		for _, l := range lines {
			out = append(out, l.raw)
		}
		if len(out) > 0 && strings.TrimSpace(out[len(out)-1]) != "" {
			out = append(out, "")
		}
		out = append(out, "["+Section+"]")
		for _, w := range want {
			out = append(out, w.name+"="+w.val)
		}
	} else {
		end := len(lines)
		for i := target + 1; i < len(lines); i++ {
			if lines[i].header != "" {
				end = i
				break
			}
		}
		done := map[string]bool{}
		lastContent := target
		for i := 0; i < len(lines); i++ {
			l := lines[i]
			if i > target && i < end {
				for _, w := range want {
					if l.key == w.key {
						l.raw = keyName(l.raw) + "=" + w.val
						done[w.key] = true
					}
				}
				if strings.TrimSpace(l.raw) != "" {
					lastContent = len(out)
				}
			}
			if i == target {
				lastContent = len(out)
			}
			out = append(out, l.raw)
		}
		var missing []string
		for _, w := range want {
			if !done[w.key] {
				missing = append(missing, w.name+"="+w.val)
			}
		}
		if len(missing) > 0 {
			ins := lastContent + 1
			out = append(out[:ins], append(missing, out[ins:]...)...)
		}
	}
	res := strings.Join(out, nl) + nl
	if bom {
		res = utf8BOM + res
	}
	return res
}

func keyName(raw string) string {
	t := strings.TrimSpace(raw)
	if i := strings.IndexByte(t, '='); i > 0 {
		return strings.TrimSpace(t[:i])
	}
	return t
}

// ConfigDir returns <install>\TAGame\Config.
func ConfigDir(installDir string) string {
	return filepath.Join(installDir, "TAGame", "Config")
}

// IniStatus is reported in /api/status.
type IniStatus struct {
	InstallDir     string  `json:"install_dir"`
	Path           string  `json:"path"`
	Found          bool    `json:"found"`
	PacketSendRate float64 `json:"packet_send_rate"`
	OK             bool    `json:"ok"`
	Port           int     `json:"port,omitempty"`
	WebPort        int     `json:"web_port,omitempty"`
}

// CheckIni reads TAStatsAPI.ini (if present) else DefaultStatsAPI.ini.
func CheckIni(installDir string) IniStatus {
	st := IniStatus{InstallDir: installDir}
	if installDir == "" {
		return st
	}
	dir := ConfigDir(installDir)
	st.Path = filepath.Join(dir, DefaultIniName)
	var def, ta *IniValues
	if b, err := os.ReadFile(filepath.Join(dir, DefaultIniName)); err == nil {
		text, _ := decodeIni(b)
		v := ParseIni(text)
		def = &v
	}
	if b, err := os.ReadFile(filepath.Join(dir, TAIniName)); err == nil {
		text, _ := decodeIni(b)
		v := ParseIni(text)
		ta = &v
	}
	var eff *IniValues
	switch {
	case ta != nil && ta.HasRate:
		eff = ta
		st.Path = filepath.Join(dir, TAIniName)
	case def != nil:
		eff = def
	case ta != nil:
		eff = ta
		st.Path = filepath.Join(dir, TAIniName)
	}
	if eff != nil {
		st.Found = true
		st.PacketSendRate = eff.PacketSendRate
		st.Port, st.WebPort = eff.Port, eff.WebPort
		st.OK = eff.HasRate && eff.PacketSendRate > 0
	}
	return st
}

// EnableStatsAPI writes DefaultStatsAPI.ini (created if missing) and patches
// TAStatsAPI.ini when it exists. Returns the files written.
// A permission error is returned as-is (check with IsPermission).
func EnableStatsAPI(installDir string, rate float64, port, webPort int) ([]string, error) {
	if rate <= 0 || rate > 120 {
		rate = DefaultPacketSendRate
	}
	dir := ConfigDir(installDir)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	var written []string
	for _, name := range []string{DefaultIniName, TAIniName} {
		p := filepath.Join(dir, name)
		b, err := os.ReadFile(p)
		if errors.Is(err, fs.ErrNotExist) {
			if name == TAIniName {
				continue // only patch TAStatsAPI.ini when it exists
			}
			b = nil
		} else if err != nil {
			return written, err
		}
		text, enc := decodeIni(b)
		out := encodeIni(PatchIni(text, rate, port, webPort), enc)
		if string(b) == string(out) {
			written = append(written, p)
			continue
		}
		if err := writeFileAtomic(p, out); err != nil {
			return written, err
		}
		written = append(written, p)
	}
	return written, nil
}

// iniEncoding is the on-disk text encoding of an ini file. Unreal Engine
// configs may be UTF-16; patching their bytes as UTF-8 would corrupt them.
type iniEncoding int

const (
	encUTF8 iniEncoding = iota
	encUTF16LE
	encUTF16BE
)

// decodeIni returns the file text (a UTF-8 BOM is kept, UTF-16 is converted)
// and the encoding to write it back with.
func decodeIni(b []byte) (string, iniEncoding) {
	var enc iniEncoding
	switch {
	case len(b) >= 2 && b[0] == 0xFF && b[1] == 0xFE:
		enc, b = encUTF16LE, b[2:]
	case len(b) >= 2 && b[0] == 0xFE && b[1] == 0xFF:
		enc, b = encUTF16BE, b[2:]
	case len(b) >= 2 && b[0] != 0 && b[1] == 0:
		enc = encUTF16LE // no BOM, ASCII-looking UTF-16LE
	default:
		return string(b), encUTF8
	}
	u := make([]uint16, len(b)/2)
	for i := range u {
		if enc == encUTF16LE {
			u[i] = binary.LittleEndian.Uint16(b[2*i:])
		} else {
			u[i] = binary.BigEndian.Uint16(b[2*i:])
		}
	}
	return string(utf16.Decode(u)), enc
}

// encodeIni is the inverse of decodeIni (UTF-16 is written with a BOM).
func encodeIni(s string, enc iniEncoding) []byte {
	if enc == encUTF8 {
		return []byte(s)
	}
	u := utf16.Encode([]rune(s))
	out := make([]byte, 2, 2+2*len(u))
	if enc == encUTF16LE {
		out[0], out[1] = 0xFF, 0xFE
		for _, c := range u {
			out = binary.LittleEndian.AppendUint16(out, c)
		}
	} else {
		out[0], out[1] = 0xFE, 0xFF
		for _, c := range u {
			out = binary.BigEndian.AppendUint16(out, c)
		}
	}
	return out
}

// writeFileAtomic writes data to a temp file in the same directory and
// renames it over path, so a failure never leaves a truncated ini behind.
func writeFileAtomic(path string, data []byte) error {
	f, err := os.CreateTemp(filepath.Dir(path), filepath.Base(path)+".*.tmp")
	if err != nil {
		return err
	}
	tmp := f.Name()
	_, err = f.Write(data)
	if err == nil {
		err = f.Sync()
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Rename(tmp, path)
	}
	if err != nil {
		_ = os.Remove(tmp)
	}
	return err
}

// IsPermission reports an access-denied style error.
func IsPermission(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, fs.ErrPermission) {
		return true
	}
	return strings.Contains(strings.ToLower(err.Error()), "access is denied")
}

// utf8BOM is the UTF-8 byte order mark.
const utf8BOM = "\xef\xbb\xbf"
