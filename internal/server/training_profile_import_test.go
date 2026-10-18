package server

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"rocket-tracker/internal/domain"
	"rocket-tracker/internal/store"
	"testing"
	"time"
)

func TestHTTPImportKeepsDistinctTrainingProfiles(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "source.db")
	src, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	at := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	d := domain.Destination{Server: "https://synthetic.invalid", ServerID: "installation", AccountID: 1}
	for i, p := range []string{"profile-a", "profile-b"} {
		tr := &domain.Training{StartedAt: at, EndedAt: at.Add(time.Minute), Source: "game", Profile: p, Touches: (i + 1) * 10}
		if err := src.User(0).RecordTraining(ctx, tr, &d); err != nil {
			t.Fatal(err)
		}
	}
	rows, err := src.User(0).Trainings(ctx)
	if err != nil || len(rows) != 2 {
		t.Fatal(rows, err)
	}
	if err := src.Close(); err != nil {
		t.Fatal(err)
	}
	source, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	_, h, sc := limitServer(t)
	body, ct := limitMultipart(t, limitUpload{"source.db", source})
	for replay := 0; replay < 2; replay++ {
		w := limitRequest(h, body, ct)
		var result ImportResult
		if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil || w.Code != 200 {
			t.Fatalf("HTTP %d: %s (%v)", w.Code, w.Body.String(), err)
		}
		got, err := sc.Trainings(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if len(got) != 2 {
			t.Fatalf("HTTP %d reported %+v, persisted %d: %+v", w.Code, result, len(got), got)
		}
		byProfile := map[string]int{}
		for _, tr := range got {
			byProfile[tr.Profile] = tr.Touches
		}
		if byProfile["profile-a"] != 10 || byProfile["profile-b"] != 20 {
			t.Fatalf("profile measurements changed: %v", byProfile)
		}
	}
}
