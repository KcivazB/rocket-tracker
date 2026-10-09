package server

import (
	"bytes"
	"context"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"rocket-tracker/internal/sim"
	"rocket-tracker/internal/store"
)

func TestImportUpload(t *testing.T) {
	ts, st := newTestServer(t)
	ctx := context.Background()

	// The gaming PC's local database: 10 matches and a hand-entered day.
	srcPath := filepath.Join(t.TempDir(), "rltracker.db")
	src, err := store.Open(srcPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := src.SaveMany(ctx, sim.Generate(sim.SeedOptions{N: 10, Seed: 99})); err != nil {
		t.Fatal(err)
	}
	if err := src.SetManual(ctx, store.ManualDay{Day: "2026-01-02", Mode: "2v2", Games: 4, Wins: 3}); err != nil {
		t.Fatal(err)
	}
	src.Close()
	dbBytes, err := os.ReadFile(srcPath)
	if err != nil {
		t.Fatal(err)
	}
	// A queued match of the agent's outbox, and a file that is neither.
	start := time.Date(2026, 3, 4, 20, 0, 0, 0, time.UTC)
	queued, _ := json.Marshal(map[string]any{"key": "q1", "match": &store.Match{
		StartedAt: start, EndedAt: start.Add(6 * time.Minute), Mode: "2v2", TeamSize: 2, Result: "win", TeamScore: 3, OppScore: 1}})

	upload := func() ImportResult {
		var buf bytes.Buffer
		mw := multipart.NewWriter(&buf)
		for name, data := range map[string][]byte{"rltracker.db": dbBytes, "q1.json": queued, "notes.json": []byte("hello")} {
			w, _ := mw.CreateFormFile("file", name)
			w.Write(data)
		}
		mw.Close()
		resp, err := http.Post(ts.URL+"/api/import", mw.FormDataContentType(), &buf)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		var res ImportResult
		if resp.StatusCode != 200 || json.NewDecoder(resp.Body).Decode(&res) != nil {
			t.Fatalf("import: status %d", resp.StatusCode)
		}
		return res
	}
	res := upload()
	if res.Matches != 11 || res.Manual != 1 || len(res.Skipped) != 1 {
		t.Fatalf("result %+v", res)
	}
	if n, _ := st.Count(ctx); n != 36 {
		t.Fatalf("count %d, want 36", n)
	}
	upload() // again: updates, no duplicates
	if n, _ := st.Count(ctx); n != 36 {
		t.Fatalf("count after second import %d, want 36", n)
	}
	if ds, _ := st.ListManual(ctx); len(ds) != 1 || ds[0].Wins != 3 {
		t.Fatalf("manual %+v", ds)
	}
}

func TestImporterPath(t *testing.T) {
	_, st := newTestServer(t)
	ctx := context.Background()
	dir := t.TempDir()
	start := time.Date(2026, 3, 4, 20, 0, 0, 0, time.UTC)
	for i, key := range []string{"a", "b"} {
		b, _ := json.Marshal(map[string]any{"key": key, "match": &store.Match{
			StartedAt: start.Add(time.Duration(i) * time.Hour), EndedAt: start.Add(time.Duration(i)*time.Hour + 5*time.Minute), Mode: "1v1", TeamSize: 1, Result: "loss"}})
		os.WriteFile(filepath.Join(dir, key+".json"), b, 0o644)
	}
	im := &Importer{Scope: st.User(store.LocalUser), DefaultTag: "ranked"}
	res, err := im.Path(ctx, dir)
	if err != nil || res.Matches != 2 {
		t.Fatalf("%+v %v", res, err)
	}
	if _, err := im.Path(ctx, t.TempDir()); err == nil {
		t.Fatal("empty folder: want an error")
	}
	if n, _ := st.Count(ctx); n != 27 {
		t.Fatalf("count %d", n)
	}
}
