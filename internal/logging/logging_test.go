package logging

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRotation(t *testing.T) {
	p := filepath.Join(t.TempDir(), "x.log")
	r, err := OpenRotating(p, 100, 2)
	if err != nil {
		t.Fatal(err)
	}
	line := strings.Repeat("a", 39) + "\n"
	for i := 0; i < 10; i++ {
		if _, err := r.Write([]byte(line)); err != nil {
			t.Fatal(err)
		}
	}
	r.Close()
	for _, f := range []string{p, p + ".1", p + ".2"} {
		st, err := os.Stat(f)
		if err != nil {
			t.Fatalf("%s missing", f)
		}
		if st.Size() > 100 {
			t.Fatalf("%s too big: %d", f, st.Size())
		}
	}
	if _, err := os.Stat(p + ".3"); err == nil {
		t.Fatal("keep=2 exceeded")
	}
}
