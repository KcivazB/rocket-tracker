package server

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

func TestImportUnsupportedSchemaReportsPartialProgress(t *testing.T) {
	for _, version := range []int{-1, 1000000} {
		t.Run(fmt.Sprint(version), func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "unsupported.db")
			db, err := sql.Open("sqlite", path)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := db.Exec(fmt.Sprintf("PRAGMA user_version = %d", version)); err != nil {
				db.Close()
				t.Fatal(err)
			}
			if err := db.Close(); err != nil {
				t.Fatal(err)
			}
			source, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			s, h, sc := limitServer(t)
			body, ct := limitMultipart(t, limitUpload{"good.json", limitQueue(t, "first")}, limitUpload{"unsupported.db", source})
			w := limitRequest(h, body, ct)
			var failure struct {
				Imported ImportResult `json:"imported"`
			}
			if err := json.Unmarshal(w.Body.Bytes(), &failure); err != nil || w.Code != 400 || failure.Imported.Matches != 1 {
				t.Fatalf("partial: %d %s: %v", w.Code, w.Body.String(), err)
			}
			if n, err := sc.Count(context.Background()); err != nil || n != 1 {
				t.Fatalf("persisted count %d: %v", n, err)
			}
			if entries, err := os.ReadDir(s.TempDir); err != nil || len(entries) != 0 {
				t.Fatalf("temporary files remain: %v %v", entries, err)
			}
		})
	}
}
