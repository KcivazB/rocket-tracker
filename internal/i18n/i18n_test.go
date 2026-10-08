package i18n

import (
	"strings"
	"testing"
)

func TestMessagesComplete(t *testing.T) {
	for k, v := range messages {
		if v[FR] == "" || v[EN] == "" {
			t.Errorf("%s: missing translation", k)
		}
		if verbs(v[FR]) != verbs(v[EN]) {
			t.Errorf("%s: format verbs differ: %q vs %q", k, verbs(v[FR]), verbs(v[EN]))
		}
	}
}

// verbs lists the fmt verbs of s in order.
func verbs(s string) string {
	var out []string
	for i := 0; i < len(s)-1; i++ {
		if s[i] == '%' {
			out = append(out, s[i:i+2])
			i++
		}
	}
	return strings.Join(out, " ")
}

func TestAcceptLanguage(t *testing.T) {
	for h, want := range map[string]Lang{
		"":                                    EN,
		"fr-FR,fr;q=0.9,en-US;q=0.8,en;q=0.7": FR,
		"en-GB,en;q=0.9,fr;q=0.8":             EN,
		"de-DE,de;q=0.9,fr;q=0.5":             FR,
		"de-DE":                               EN,
		"en;q=0.3, fr;q=0.7":                  FR,
	} {
		if got := FromAcceptLanguage(h); got != want {
			t.Errorf("%q: got %v want %v", h, got, want)
		}
	}
	if Tf(FR, "setup.found", "C:\\RL") != "Rocket League : C:\\RL" || T(EN, "nope") != "nope" {
		t.Fatal("T/Tf")
	}
}
