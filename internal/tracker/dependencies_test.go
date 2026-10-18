package tracker

import (
	"go/parser"
	"go/token"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func TestTrackerImportsStayPure(t *testing.T) {
	// Inspect every production file, including files for other platforms.
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range files {
		if strings.HasSuffix(name, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(token.NewFileSet(), name, nil, parser.ImportsOnly)
		if err != nil {
			t.Fatal(err)
		}
		for _, spec := range f.Imports {
			path, err := strconv.Unquote(spec.Path.Value)
			if err != nil {
				t.Fatal(err)
			}
			switch path {
			case "rocket-tracker/internal/domain", "errors", "fmt", "log/slog", "math", "runtime/debug", "sort", "strconv", "strings", "sync", "time":
			default:
				t.Errorf("%s imports %s; tracker must consume pure domain observations", name, path)
			}
		}
	}
}
