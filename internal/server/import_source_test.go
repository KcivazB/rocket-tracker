package server

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"modernc.org/sqlite"
	"rocket-tracker/internal/store"
)

func TestImportRejectsExecutableSchemaAndReleasesCapacity(t *testing.T) {
	path := filepath.Join(t.TempDir(), "hostile.db")
	st, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	st.Close()
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(`DROP TABLE kv; CREATE VIEW kv AS WITH RECURSIVE n(x) AS (VALUES(1) UNION ALL SELECT x+1 FROM n WHERE x<1000000) SELECT 'repair:goals-speeds-v1' AS k, CAST(sum(x) AS TEXT) AS v FROM n`)
	db.Close()
	if err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("RT_IMPORT_MAX_CONCURRENT", "1")
	s, h, sc := limitServer(t)
	body, ct := limitMultipart(t, limitUpload{"hostile.db", before})
	w := limitRequest(h, body, ct)
	if w.Code != 400 {
		t.Fatalf("schema status %d: %s", w.Code, w.Body.String())
	}
	assertImportEmpty(t, s, sc)
	after, _ := os.ReadFile(path)
	if string(before) != string(after) {
		t.Fatal("source was modified")
	}
	body, ct = limitMultipart(t, limitUpload{"good.json", limitQueue(t, "after-rejection")})
	w = limitRequest(h, body, ct)
	if w.Code != 200 {
		t.Fatalf("slot not released: %d %s", w.Code, w.Body.String())
	}
	if files, _ := os.ReadDir(s.TempDir); len(files) != 0 {
		t.Fatal("staging files left behind")
	}
}

func TestImportLegacyReadOnlyAndCancellation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "v1.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(`CREATE TABLE matches(id INTEGER PRIMARY KEY,tag TEXT,data TEXT); PRAGMA user_version=1`)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(ingestionFixture())
	for i := 1; i <= 2; i++ {
		if _, err = db.Exec(`INSERT INTO matches VALUES(?,?,?)`, i, "ranked", string(raw)); err != nil {
			t.Fatal(err)
		}
	}
	db.Close()
	before, _ := os.ReadFile(path)
	src, err := openImportSource(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = src.db.Exec(`DELETE FROM matches`); err == nil {
		t.Fatal("source writable")
	}
	ctx, cancel := context.WithCancel(context.Background())
	seen := 0
	err = src.matches(ctx, func(*store.Match) error { seen++; cancel(); return nil })
	if !errors.Is(err, context.Canceled) || seen != 1 {
		t.Fatalf("cancel seen=%d err=%v", seen, err)
	}
	src.db.Close()
	after, _ := os.ReadFile(path)
	if string(before) != string(after) {
		t.Fatal("source migrated/changed")
	}
}

var importFunctionID atomic.Int64

func TestImportSourceInterruptsRunningSQL(t *testing.T) {
	entered := make(chan struct{})
	var once sync.Once
	function := fmt.Sprintf("import_test_signal_%d", importFunctionID.Add(1))
	if err := sqlite.RegisterScalarFunction(function, 0, func(*sqlite.FunctionContext, []driver.Value) (driver.Value, error) {
		once.Do(func() { close(entered) })
		return int64(1), nil
	}); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "source.db")
	st, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	st.Close()
	src, err := openImportSource(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	defer src.db.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		var value int64
		done <- src.db.QueryRowContext(ctx, `WITH RECURSIVE n(x) AS (VALUES(`+function+`()) UNION ALL SELECT x+1 FROM n WHERE x<1000000000) SELECT sum(x) FROM n`).Scan(&value)
	}()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("SQL did not enter VM")
	}
	cancel()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("query ignored cancellation")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("active query did not stop")
	}
	var one int
	if err = src.db.QueryRowContext(context.Background(), "SELECT 1").Scan(&one); err != nil || one != 1 {
		t.Fatalf("connection not released/reusable: %v", err)
	}
}
