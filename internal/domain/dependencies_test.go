package domain_test

import (
	"go/parser"
	"go/token"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func TestDomainImportsStayPure(t *testing.T) {
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
			if path != "strings" && path != "time" && path != "errors" && path != "fmt" && path != "math" && path != "slices" && path != "encoding/json" {
				t.Errorf("%s imports %s; domain production code must use its pure standard-library allowlist", name, path)
			}
		}
	}
}
