package server

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"rocket-tracker/internal/domain"
	"rocket-tracker/internal/store"
)

type limitUpload struct {
	name string
	data []byte
}

func limitMultipart(t *testing.T, files ...limitUpload) ([]byte, string) {
	t.Helper()
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	for _, f := range files {
		w, err := mw.CreateFormFile("file", f.name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write(f.data); err != nil {
			t.Fatal(err)
		}
	}
	if err := mw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes(), mw.FormDataContentType()
}

func limitQueue(t *testing.T, key string) []byte {
	t.Helper()
	m := ingestionFixture()
	m.GUID = key
	b, err := json.Marshal(map[string]any{"key": key, "match": m})
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func limitServer(t *testing.T) (*Server, http.Handler, *store.Scope) {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "target.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	s := &Server{Store: st, TempDir: t.TempDir()}
	return s, s.Handler(), st.User(store.LocalUser)
}

func limitRequest(h http.Handler, body []byte, contentType string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(http.MethodPost, "http://localhost/api/import", bytes.NewReader(body))
	r.Header.Set("Content-Type", contentType)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

func assertImportEmpty(t *testing.T, s *Server, sc *store.Scope) {
	t.Helper()
	if n, err := sc.Count(context.Background()); err != nil || n != 0 {
		t.Fatalf("wrote %d matches: %v", n, err)
	}
	if days, err := sc.ListManual(context.Background()); err != nil || len(days) != 0 {
		t.Fatalf("wrote manual entries: %v %v", days, err)
	}
	if trainings, err := sc.Trainings(context.Background()); err != nil || len(trainings) != 0 {
		t.Fatalf("wrote trainings: %v %v", trainings, err)
	}
	if entries, err := os.ReadDir(s.TempDir); err != nil || len(entries) != 0 {
		t.Fatalf("temporary files left: %v %v", entries, err)
	}
}

func TestImportLimitsInvalidConfiguration(t *testing.T) {
	for _, entry := range []struct{ name, value string }{
		{"RT_IMPORT_MAX_BYTES", "0"}, {"RT_IMPORT_MAX_BYTES", "9223372036854775807"},
		{"RT_IMPORT_MAX_TEMP_BYTES", "-1"}, {"RT_IMPORT_MAX_FILES", "bad"},
		{"RT_IMPORT_MAX_PARTS", "0"}, {"RT_IMPORT_MAX_CONCURRENT", "-1"},
		{"RT_IMPORT_TIMEOUT", "0s"}, {"RT_IMPORT_TIMEOUT", "forever"},
	} {
		t.Run(entry.name+entry.value, func(t *testing.T) {
			s, h, sc := limitServer(t)
			t.Setenv(entry.name, entry.value)
			body, ct := limitMultipart(t, limitUpload{"good.json", limitQueue(t, "good")})
			w := limitRequest(h, body, ct)
			if w.Code != 500 || !strings.Contains(w.Body.String(), `"field":"`+entry.name+`"`) {
				t.Fatalf("config %d %s", w.Code, w.Body.String())
			}
			assertImportEmpty(t, s, sc)
		})
	}
}

func TestImportLimitsBadEnvelopeAndTemporaryDirectory(t *testing.T) {
	for _, kind := range []string{"content_type", "no_files", "temp_directory"} {
		t.Run(kind, func(t *testing.T) {
			s, h, sc := limitServer(t)
			body, ct := limitMultipart(t, limitUpload{"good.json", limitQueue(t, "good")})
			status, field := 400, "multipart"
			switch kind {
			case "content_type":
				ct = "application/json"
			case "no_files":
				body, ct = limitMultipart(t)
				field = "files"
			case "temp_directory":
				// Restore TempDir after the response so the cleanup assertion can
				// inspect the original directory rather than this deliberate file.
				original := s.TempDir
				bad := filepath.Join(t.TempDir(), "not-a-directory")
				if err := os.WriteFile(bad, nil, 0600); err != nil {
					t.Fatal(err)
				}
				s.TempDir = bad
				defer func() { s.TempDir = original }()
				status, field = 500, "temporary_bytes"
			}
			w := limitRequest(h, body, ct)
			if w.Code != status || !strings.Contains(w.Body.String(), `"field":"`+field+`"`) {
				t.Fatalf("%s: %d %s", kind, w.Code, w.Body.String())
			}
			if n, err := sc.Count(context.Background()); err != nil || n != 0 {
				t.Fatalf("wrote %d: %v", n, err)
			}
		})
	}
}

func TestImportTrainingValidationAndReplay(t *testing.T) {
	s, h, sc := limitServer(t)
	start := ingestionFixture().StartedAt
	good := store.Training{ID: 99999, StartedAt: start, EndedAt: start.Add(10 * time.Minute), Source: "game"}
	bad := good
	bad.Source = "unknown"
	marshal := func(key string, tr store.Training) []byte {
		b, err := json.Marshal(map[string]any{"key": key, "training": tr})
		if err != nil {
			t.Fatal(err)
		}
		return b
	}
	body, ct := limitMultipart(t,
		limitUpload{"bad-training.json", marshal("bad", bad)},
		limitUpload{"good-training.json", marshal("good", good)},
		limitUpload{"long-key.json", marshal(strings.Repeat("k", 201), good)})
	for range 2 {
		w := limitRequest(h, body, ct)
		var res ImportResult
		if err := json.Unmarshal(w.Body.Bytes(), &res); err != nil || w.Code != 200 || res.Trainings != 1 || res.Matches != 0 || len(res.Skipped) != 2 {
			t.Fatalf("training import %d %+v: %v", w.Code, res, err)
		}
	}
	if trainings, err := sc.Trainings(context.Background()); err != nil || len(trainings) != 1 || trainings[0].ID == good.ID || trainings[0].Source != "game" {
		t.Fatalf("training replay: %+v %v", trainings, err)
	}
	if n, err := sc.Count(context.Background()); err != nil || n != 0 {
		t.Fatalf("training became a match: %d %v", n, err)
	}
	if entries, err := os.ReadDir(s.TempDir); err != nil || len(entries) != 0 {
		t.Fatalf("temporary files left: %v %v", entries, err)
	}
}

func TestImportLargeQueueKeepsSkippedContract(t *testing.T) {
	s, h, sc := limitServer(t)
	body, ct := limitMultipart(t, limitUpload{"large.json", bytes.Repeat([]byte("x"), MaxMatchBody+1)}, limitUpload{"good.json", limitQueue(t, "good")})
	w := limitRequest(h, body, ct)
	var res ImportResult
	if err := json.Unmarshal(w.Body.Bytes(), &res); err != nil || w.Code != 200 || res.Matches != 1 || len(res.Skipped) != 1 {
		t.Fatalf("oversized content %d %+v: %v", w.Code, res, err)
	}
	if n, err := sc.Count(context.Background()); err != nil || n != 1 {
		t.Fatalf("count %d: %v", n, err)
	}
	if entries, err := os.ReadDir(s.TempDir); err != nil || len(entries) != 0 {
		t.Fatalf("temporary files left: %v %v", entries, err)
	}
}

func TestImportLimitsRejectBeforeWrites(t *testing.T) {
	for _, kind := range []string{"files", "temporary", "body", "truncated", "epilogue", "configuration"} {
		t.Run(kind, func(t *testing.T) {
			s, h, sc := limitServer(t)
			q := limitQueue(t, "first")
			body, ct := limitMultipart(t, limitUpload{"first.json", q}, limitUpload{"second.json", limitQueue(t, "second")})
			status, field := http.StatusRequestEntityTooLarge, "files"
			switch kind {
			case "files":
				t.Setenv("RT_IMPORT_MAX_FILES", "1")
			case "temporary":
				t.Setenv("RT_IMPORT_MAX_TEMP_BYTES", strconv.Itoa(len(q)))
				field = "temporary_bytes"
			case "body":
				t.Setenv("RT_IMPORT_MAX_BYTES", strconv.Itoa(len(body)-1))
				field = "body_bytes"
			case "truncated":
				body = body[:len(body)-10]
				status, field = http.StatusBadRequest, "multipart"
			case "epilogue":
				t.Setenv("RT_IMPORT_MAX_BYTES", strconv.Itoa(len(body)))
				body = append(body, []byte(strings.Repeat("x", 100))...)
				field = "body_bytes"
			case "configuration":
				t.Setenv("RT_IMPORT_MAX_FILES", "0")
				status, field = http.StatusInternalServerError, "RT_IMPORT_MAX_FILES"
			}
			w := limitRequest(h, body, ct)
			if kind == "epilogue" {
				// Also prove the epilogue limit without a Content-Length shortcut.
				r := httptest.NewRequest(http.MethodPost, "http://localhost/api/import", bytes.NewReader(body))
				r.ContentLength = -1
				r.Header.Set("Content-Type", ct)
				w = httptest.NewRecorder()
				h.ServeHTTP(w, r)
			}
			if w.Code != status {
				t.Errorf("status %d, want %d: %s", w.Code, status, w.Body.String())
			}
			var failure struct {
				Field    string       `json:"field"`
				Error    string       `json:"error"`
				Imported ImportResult `json:"imported"`
			}
			if err := json.Unmarshal(w.Body.Bytes(), &failure); err != nil || failure.Field != field || failure.Error == "" || failure.Imported.Matches != 0 {
				t.Errorf("error envelope: %+v %v", failure, err)
			}
			assertImportEmpty(t, s, sc)
		})
	}
}

func TestImportLimitsAcceptExactBoundaryAndReplay(t *testing.T) {
	s, h, sc := limitServer(t)
	q := limitQueue(t, "boundary")
	body, ct := limitMultipart(t, limitUpload{"boundary.json", q})
	t.Setenv("RT_IMPORT_MAX_FILES", "1")
	t.Setenv("RT_IMPORT_MAX_BYTES", strconv.Itoa(len(body)))
	t.Setenv("RT_IMPORT_MAX_TEMP_BYTES", strconv.Itoa(len(q)))
	for range 2 {
		w := limitRequest(h, body, ct)
		var res ImportResult
		if err := json.Unmarshal(w.Body.Bytes(), &res); err != nil || w.Code != http.StatusOK || res.Matches != 1 {
			t.Fatalf("boundary/replay %d %s: %v", w.Code, w.Body.String(), err)
		}
	}
	if n, err := sc.Count(context.Background()); err != nil || n != 1 {
		t.Fatalf("replay count %d: %v", n, err)
	}
	if entries, err := os.ReadDir(s.TempDir); err != nil || len(entries) != 0 {
		t.Fatalf("temporary files left: %v %v", entries, err)
	}
}

type pausedImportBody struct {
	started, closed     chan struct{}
	readOnce, closeOnce sync.Once
}

func (b *pausedImportBody) Read([]byte) (int, error) {
	b.readOnce.Do(func() { close(b.started) })
	<-b.closed
	return 0, io.ErrClosedPipe
}
func (b *pausedImportBody) Close() error {
	b.closeOnce.Do(func() { close(b.closed) })
	return nil
}

func TestImportLimitsConcurrencyCancellationAndRelease(t *testing.T) {
	t.Setenv("RT_IMPORT_MAX_CONCURRENT", "1")
	s, h, sc := limitServer(t)
	body := &pausedImportBody{started: make(chan struct{}), closed: make(chan struct{})}
	t.Cleanup(func() { body.Close() })
	r := httptest.NewRequest(http.MethodPost, "http://localhost/api/import", body)
	r.Header.Set("Content-Type", "multipart/form-data; boundary=blocked")
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	w := httptest.NewRecorder()
	done := make(chan struct{})
	go func() { h.ServeHTTP(w, r.WithContext(ctx)); close(done) }()
	select {
	case <-body.started:
	case <-time.After(2 * time.Second):
		t.Fatal("first import did not read")
	}
	q, ct := limitMultipart(t, limitUpload{"second.json", limitQueue(t, "second")})
	busy := limitRequest(h, q, ct)
	if busy.Code != http.StatusTooManyRequests || busy.Header().Get("Retry-After") == "" || !strings.Contains(busy.Body.String(), `"field":"concurrency"`) {
		t.Fatalf("busy: %d %s", busy.Code, busy.Body.String())
	}
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("cancel did not interrupt body read")
	}
	if w.Code != http.StatusRequestTimeout {
		t.Fatalf("cancel status: %d %s", w.Code, w.Body.String())
	}
	assertImportEmpty(t, s, sc)
	if retry := limitRequest(h, q, ct); retry.Code != http.StatusOK {
		t.Fatalf("slot not released: %d %s", retry.Code, retry.Body.String())
	}
}

func TestImportLimitsDeadlineInterruptsHTTPRead(t *testing.T) {
	t.Setenv("RT_IMPORT_TIMEOUT", "30ms")
	s, h, sc := limitServer(t)
	ts := httptest.NewServer(h)
	defer ts.Close()
	reader, writer := io.Pipe()
	defer reader.Close()
	defer writer.Close()
	go func() {
		_, _ = io.WriteString(writer, "--slow\r\nContent-Disposition: form-data; name=\"file\"; filename=\"slow.json\"\r\n\r\n{")
	}()
	req, err := http.NewRequest(http.MethodPost, ts.URL+"/api/import", reader)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "multipart/form-data; boundary=slow")
	client := &http.Client{Timeout: 2 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil || resp.StatusCode != http.StatusRequestTimeout || !bytes.Contains(data, []byte(`"field":"duration"`)) {
		t.Fatalf("deadline: %d %s: %v", resp.StatusCode, data, err)
	}
	assertImportEmpty(t, s, sc)
}

func TestImportLimitsFieldsAndUnknownLength(t *testing.T) {
	t.Setenv("RT_IMPORT_MAX_PARTS", "1")
	s, h, sc := limitServer(t)
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	if err := mw.WriteField("ignored", "still counts as a part"); err != nil {
		t.Fatal(err)
	}
	f, err := mw.CreateFormFile("file", "valid.json")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.Write(limitQueue(t, "first")); err != nil {
		t.Fatal(err)
	}
	if err := mw.Close(); err != nil {
		t.Fatal(err)
	}
	w := limitRequest(h, buf.Bytes(), mw.FormDataContentType())
	if w.Code != 413 || !strings.Contains(w.Body.String(), `"field":"parts"`) {
		t.Fatalf("parts %d %s", w.Code, w.Body.String())
	}
	assertImportEmpty(t, s, sc)
	t.Setenv("RT_IMPORT_MAX_PARTS", "2")
	t.Setenv("RT_IMPORT_MAX_BYTES", strconv.Itoa(buf.Len()-1))
	r := httptest.NewRequest(http.MethodPost, "http://localhost/api/import", bytes.NewReader(buf.Bytes()))
	r.ContentLength = -1
	r.Header.Set("Content-Type", mw.FormDataContentType())
	w = httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 413 || !strings.Contains(w.Body.String(), `"field":"body_bytes"`) {
		t.Fatalf("chunked body %d %s", w.Code, w.Body.String())
	}
	assertImportEmpty(t, s, sc)
}

func TestImportContentFailureReportsPartialProgress(t *testing.T) {
	s, h, sc := limitServer(t)
	body, ct := limitMultipart(t, limitUpload{"good.json", limitQueue(t, "first")}, limitUpload{"corrupt.db", append(append([]byte(nil), sqliteMagic...), []byte("not a database")...)})
	w := limitRequest(h, body, ct)
	var failure struct {
		Imported ImportResult `json:"imported"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &failure); err != nil || w.Code != 400 || failure.Imported.Matches != 1 {
		t.Fatalf("partial: %d %s: %v", w.Code, w.Body.String(), err)
	}
	if n, err := sc.Count(context.Background()); err != nil || n != 1 {
		t.Fatalf("partial count %d: %v", n, err)
	}
	if entries, err := os.ReadDir(s.TempDir); err != nil || len(entries) != 0 {
		t.Fatalf("temporary files left: %v %v", entries, err)
	}
}

func TestImportScopeAndSkippedContents(t *testing.T) {
	e := newHub(t, nil)
	client := e.devLogin(t, "alice")
	body, ct := limitMultipart(t, limitUpload{"bad.json", []byte(`{} {}`)}, limitUpload{"good.json", limitQueue(t, "scoped")})
	resp, err := client.Post(e.ts.URL+"/api/import", ct, bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var res ImportResult
	if err := json.NewDecoder(resp.Body).Decode(&res); err != nil || resp.StatusCode != 200 || res.Matches != 1 || len(res.Skipped) != 1 {
		t.Fatalf("scoped result %+v: %v (%d)", res, err, resp.StatusCode)
	}
	u, err := e.st.UserByHandle(context.Background(), "alice")
	if err != nil {
		t.Fatal(err)
	}
	if n, err := e.st.User(u.ID).Count(context.Background()); err != nil || n != 1 {
		t.Fatalf("owner count %d: %v", n, err)
	}
	if n, err := e.st.Count(context.Background()); err != nil || n != 0 {
		t.Fatalf("leaked into local scope %d: %v", n, err)
	}
}

// This exercise copies only stopped synthetic stores. It never starts an agent
// or calls a delivery transport, and needs no external backup/SQLite program.
func TestOperationsStoppedBackupRestore(t *testing.T) {
	ctx := context.Background()
	data := t.TempDir()
	path := filepath.Join(data, "rltracker.db")
	src, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer src.Close()
	installation, err := src.InstallationID(ctx)
	if err != nil {
		t.Fatal(err)
	}
	u, err := src.UpsertOIDCUser(ctx, "synthetic:owner", "owner", "Synthetic", "")
	if err != nil {
		t.Fatal(err)
	}
	owner := src.User(u.ID)
	_, deviceToken, err := owner.CreateDevice(ctx, "synthetic")
	if err != nil {
		t.Fatal(err)
	}
	if err := src.SetUserSettings(ctx, u.ID, `{"player_ids":["synthetic-profile"]}`); err != nil {
		t.Fatal(err)
	}
	destination := domain.Destination{Server: "https://delivery.invalid", ServerID: "synthetic-server-installation", AccountID: 42}
	scope := src.User(store.LocalUser)
	m := ingestionFixture()
	if err := scope.RecordResult(ctx, m, &destination); err != nil {
		t.Fatal(err)
	}
	old, err := scope.PendingDeliveries(ctx, destination)
	if err != nil || len(old) != 1 {
		t.Fatalf("initial pending %d: %v", len(old), err)
	}
	m.TeamScore++
	if err := scope.RecordResult(ctx, m, &destination); err != nil {
		t.Fatal(err)
	}
	training := &domain.Training{StartedAt: m.StartedAt.Add(-time.Hour), EndedAt: m.StartedAt.Add(-50 * time.Minute), Source: "game"}
	if err := scope.RecordTraining(ctx, training, &destination); err != nil {
		t.Fatal(err)
	}
	unknown := domain.Destination{Server: destination.Server}
	held := ingestionFixture()
	held.GUID = "held-unknown-destination"
	if err := scope.RecordResult(ctx, held, &unknown); err != nil {
		t.Fatal(err)
	}
	if err := scope.SetManual(ctx, store.ManualDay{Day: "2026-10-01", Mode: "1v1", Games: 3, Wins: 1}); err != nil {
		t.Fatal(err)
	}
	files := map[string][]byte{
		"config.json":                   []byte(`{"player_ids":["synthetic-profile"],"discord_webhook":""}`),
		"agent.json":                    []byte(fmt.Sprintf(`{"server":%q,"token":%q}`, destination.Server, deviceToken)),
		"private/oidc-secret":           []byte("synthetic-secret-not-valid"),
		"outbox/destination":            []byte(destination.Server),
		"outbox/legacy.json":            limitQueue(t, "legacy-unknown-account"),
		"outbox/rejected/rejected.json": limitQueue(t, "legacy-rejected"),
		"outbox-old-server/aside.json":  limitQueue(t, "legacy-other-server"),
	}
	for rel, dataBytes := range files {
		name := filepath.Join(data, rel)
		if err := os.MkdirAll(filepath.Dir(name), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(name, dataBytes, 0600); err != nil {
			t.Fatal(err)
		}
	}
	if err := src.PreserveLegacyOutbox(ctx, filepath.Join(data, "outbox")); err != nil {
		t.Fatal(err)
	}
	if err := src.PreserveLegacyOutbox(ctx, filepath.Join(data, "outbox-old-server")); err != nil {
		t.Fatal(err)
	}
	before, err := scope.PendingDeliveries(ctx, destination)
	if err != nil || len(before) != 2 {
		t.Fatalf("pending before %d: %v", len(before), err)
	}
	status, err := scope.DeliveryStatus(ctx)
	if err != nil || status.Pending != 2 || status.Held != 1 || status.LegacyHeld != 3 {
		t.Fatalf("status before %+v: %v", status, err)
	}
	if err := src.Close(); err != nil {
		t.Fatal(err)
	}
	backup, restored := t.TempDir(), t.TempDir()
	manifest := copyStoppedTree(t, data, backup)
	if got := copyStoppedTree(t, backup, restored); !reflect.DeepEqual(got, manifest) {
		t.Fatal("restored bytes differ from backup manifest")
	}
	reopened, err := store.Open(filepath.Join(restored, "rltracker.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	if got, err := reopened.InstallationID(ctx); err != nil || got != installation {
		t.Fatalf("installation changed: %v", err)
	}
	rsc := reopened.User(store.LocalUser)
	after, err := rsc.PendingDeliveries(ctx, destination)
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatalf("pending payload/revision/key/destination changed: %v", err)
	}
	if got, err := rsc.DeliveryStatus(ctx); err != nil || got != status {
		t.Fatalf("status after %+v: %v", got, err)
	}
	if matches, err := rsc.List(ctx); err != nil || len(matches) != 2 || domain.ProfileKey(matches[0]) != "synthetic-profile" {
		t.Fatalf("matches/profiles not restored: %d %v", len(matches), err)
	}
	if trainings, err := rsc.Trainings(ctx); err != nil || len(trainings) != 1 || trainings[0].Source != "game" {
		t.Fatalf("training not restored: %+v %v", trainings, err)
	}
	for _, other := range []domain.Destination{
		{Server: destination.Server, ServerID: destination.ServerID, AccountID: 43},
		{Server: "https://other.invalid", ServerID: destination.ServerID, AccountID: 42},
		{Server: destination.Server, ServerID: "other-installation", AccountID: 42},
	} {
		if got, err := rsc.PendingDeliveries(ctx, other); err != nil || len(got) != 0 {
			t.Fatalf("pending crossed destination: %d %v", len(got), err)
		}
	}
	if _, err := rsc.PendingDeliveries(ctx, unknown); err == nil {
		t.Fatal("unverified destination became eligible")
	}
	if err := rsc.CompleteDelivery(ctx, old[0], false); err != nil {
		t.Fatal(err)
	}
	if got, err := rsc.PendingDeliveries(ctx, destination); err != nil || !reflect.DeepEqual(got, before) {
		t.Fatalf("stale acknowledgement removed restored revision: %v", err)
	}
	if got, err := reopened.User(owner.UserID()).Count(ctx); err != nil || got != 0 {
		t.Fatalf("local records crossed user scope: %d %v", got, err)
	}
	if got, _, err := reopened.DeviceUser(ctx, deviceToken); err != nil || got == nil || got.ID != u.ID {
		t.Fatalf("device identity not restored: %v", err)
	}
	if got, err := reopened.UserSettings(ctx, u.ID); err != nil || got != `{"player_ids":["synthetic-profile"]}` {
		t.Fatalf("settings not restored: %v", err)
	}
	if days, err := rsc.ListManual(ctx); err != nil || len(days) != 1 || days[0].Games != 3 {
		t.Fatalf("manual not restored: %v", err)
	}
	for rel, want := range files {
		got, err := os.ReadFile(filepath.Join(restored, rel))
		if err != nil || !bytes.Equal(got, want) {
			t.Fatalf("missing/changed %s: %v", rel, err)
		}
	}
	verify, err := sql.Open("sqlite", filepath.Join(restored, "rltracker.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer verify.Close()
	var integrity string
	if err := verify.QueryRowContext(ctx, `PRAGMA integrity_check`).Scan(&integrity); err != nil || integrity != "ok" {
		t.Fatalf("application SQLite integrity %q: %v", integrity, err)
	}
	if got := copyStoppedTree(t, backup, t.TempDir()); !reflect.DeepEqual(got, manifest) {
		t.Fatal("restore trial changed the source backup")
	}
}

func copyStoppedTree(t *testing.T, source, destination string) map[string][32]byte {
	t.Helper()
	manifest := map[string][32]byte{}
	err := filepath.WalkDir(source, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(source, path)
		if err != nil {
			return err
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("backup refuses symlink %s", rel)
		}
		if entry.IsDir() {
			return os.MkdirAll(filepath.Join(destination, rel), 0700)
		}
		if !entry.Type().IsRegular() {
			return fmt.Errorf("backup refuses special file %s", rel)
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		manifest[rel] = sha256.Sum256(data)
		return os.WriteFile(filepath.Join(destination, rel), data, 0600)
	})
	if err != nil {
		t.Fatal(err)
	}
	return manifest
}

func TestOperationsSQLiteWALCopy(t *testing.T) {
	// Keep a read transaction open so closing the writer leaves a real WAL.
	// Both connections are closed before either directory copy starts.
	dir := t.TempDir()
	path := filepath.Join(dir, "wal.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`PRAGMA journal_mode=WAL; PRAGMA wal_autocheckpoint=0; CREATE TABLE fixture(id INTEGER PRIMARY KEY, value TEXT); INSERT INTO fixture VALUES(1,'before')`); err != nil {
		t.Fatal(err)
	}
	reader, err := sql.Open("sqlite", "file:"+filepath.ToSlash(path)+"?mode=ro")
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	tx, err := reader.Begin()
	if err != nil {
		t.Fatal(err)
	}
	var value string
	if err := tx.QueryRow(`SELECT value FROM fixture WHERE id=1`).Scan(&value); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO fixture VALUES(2,'wal-only')`); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	if err := reader.Close(); err != nil {
		t.Fatal(err)
	}
	if fi, err := os.Stat(path + "-wal"); err != nil || fi.Size() == 0 {
		t.Fatalf("fixture has no stopped WAL: %v", err)
	}
	backup, restored := t.TempDir(), t.TempDir()
	manifest := copyStoppedTree(t, dir, backup)
	if got := copyStoppedTree(t, backup, restored); !reflect.DeepEqual(got, manifest) {
		t.Fatal("WAL bundle bytes changed")
	}
	verify, err := sql.Open("sqlite", filepath.Join(restored, "wal.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer verify.Close()
	var n int
	if err := verify.QueryRow(`SELECT COUNT(*) FROM fixture`).Scan(&n); err != nil || n != 2 {
		t.Fatalf("WAL recovery count %d: %v", n, err)
	}
	if err := verify.QueryRow(`PRAGMA integrity_check`).Scan(&value); err != nil || value != "ok" {
		t.Fatalf("integrity %q: %v", value, err)
	}
}
