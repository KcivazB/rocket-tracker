package web

import (
	"os/exec"
	"testing"
)

// TestTranslations runs devtools/i18n_test.js: every key used by the
// dashboard exists in French and English. Skipped without Node.js.
func TestTranslations(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not installed")
	}
	out, err := exec.Command(node, "../devtools/i18n_test.js").CombinedOutput()
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	t.Log(string(out))
}
