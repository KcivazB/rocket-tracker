package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"rocket-tracker/internal/application"
	"rocket-tracker/internal/store"
)

// Importer stores matches copied from a gaming PC, for when the PC and the
// server never run at the same time: the PC's local database (rltracker.db,
// with its hand-entered games) or files of the agent's queue (outbox\*.json).
// Importing the same files again updates instead of duplicating.
type Importer struct {
	Scope      *store.Scope
	DefaultTag string
}

// ImportResult counts what an import did.
type ImportResult struct {
	Matches   int      `json:"matches"`
	Manual    int      `json:"manual"`
	Trainings int      `json:"trainings"`
	Skipped   []string `json:"skipped,omitempty"` // "<file>: <reason>"
}

func (r *ImportResult) add(o ImportResult) {
	r.Matches += o.Matches
	r.Manual += o.Manual
	r.Trainings += o.Trainings
	r.Skipped = append(r.Skipped, o.Skipped...)
}

// sqliteMagic starts every SQLite database file.
var sqliteMagic = []byte("SQLite format 3\x00")

// IsSQLite reports whether head (the first bytes of a file) is a database.
func IsSQLite(head []byte) bool { return bytes.HasPrefix(head, sqliteMagic) }

// QueueEntry imports one file of the agent's queue ({"key", "match"}).
func (im *Importer) QueueEntry(ctx context.Context, name string, data []byte) (ImportResult, error) {
	var res ImportResult
	if err := ctx.Err(); err != nil {
		return res, err
	}
	var e struct {
		Key      string          `json:"key"`
		Match    *store.Match    `json:"match"`
		Training *store.Training `json:"training"`
	}
	if err := json.Unmarshal(data, &e); err != nil || e.Match == nil && e.Training == nil {
		res.Skipped = append(res.Skipped, name+": not a queued match")
		return res, nil
	}
	if e.Key == "" {
		e.Key = strings.TrimSuffix(filepath.Base(name), ".json")
	}
	if e.Training != nil {
		if err := application.IngestTraining(ctx, im.Scope, e.Key, e.Training); errors.Is(err, store.ErrInvalidTraining) {
			res.Skipped = append(res.Skipped, name+": "+err.Error())
			return res, nil
		} else if err != nil {
			return res, err
		}
		res.Trainings++
		return res, nil
	}
	err := IngestAgentMatch(ctx, im.Scope, im.DefaultTag, e.Key, e.Match)
	if errors.Is(err, ErrInvalidMatch) {
		res.Skipped = append(res.Skipped, name+": "+err.Error())
		return res, nil
	} else if err != nil {
		return res, err
	}
	res.Matches++
	return res, nil
}

// Database imports the matches, hand-entered games and trainings of a local
// database, with the keys `rltracker agent import` uses (the two never
// duplicate).
func (im *Importer) Database(ctx context.Context, path string) (ImportResult, error) {
	var res ImportResult
	if err := ctx.Err(); err != nil {
		return res, err
	}
	src, err := openImportSource(ctx, path)
	if err != nil {
		return res, fmt.Errorf("not a Rocket Tracker database: %w", err)
	}
	defer src.db.Close()
	err = src.matches(ctx, func(m *store.Match) error {
		key := store.ImportKey(m)
		if err := IngestAgentMatch(ctx, im.Scope, im.DefaultTag, key, m); errors.Is(err, ErrInvalidMatch) {
			res.Skipped = append(res.Skipped, key+": "+err.Error())
			return nil
		} else if err != nil {
			return err
		}
		res.Matches++
		return nil
	})
	if err != nil {
		return res, err
	}
	err = src.manual(ctx, func(d store.ManualDay) error {
		if err := im.Scope.SetManual(ctx, d); err != nil {
			return fmt.Errorf("manual games %s %s: %w", d.Day, d.Mode, err)
		}
		res.Manual++
		return nil
	})
	if err != nil {
		return res, err
	}
	err = src.trainings(ctx, func(t *store.Training) error {
		key := store.TrainingImportKey(t)
		if err := application.IngestTraining(ctx, im.Scope, key, t); errors.Is(err, store.ErrInvalidTraining) {
			res.Skipped = append(res.Skipped, key+": "+err.Error())
			return nil
		} else if err != nil {
			return err
		}
		res.Trainings++
		return nil
	})
	if err != nil {
		return res, err
	}
	return res, nil
}

// Path imports a file or every .json file of a folder (the agent's outbox).
func (im *Importer) Path(ctx context.Context, path string) (ImportResult, error) {
	var res ImportResult
	fi, err := os.Stat(path)
	if err != nil {
		return res, err
	}
	if fi.IsDir() {
		es, err := os.ReadDir(path)
		if err != nil {
			return res, err
		}
		n := 0
		for _, e := range es {
			if e.IsDir() || !strings.EqualFold(filepath.Ext(e.Name()), ".json") {
				continue
			}
			n++
			r, err := im.Path(ctx, filepath.Join(path, e.Name()))
			res.add(r)
			if err != nil {
				return res, err
			}
		}
		if n == 0 {
			return res, errors.New("no .json file in this folder (expected the agent's outbox folder)")
		}
		return res, nil
	}
	f, err := os.Open(path)
	if err != nil {
		return res, err
	}
	head := make([]byte, len(sqliteMagic))
	n, _ := io.ReadFull(f, head)
	f.Close()
	if IsSQLite(head[:n]) {
		return im.Database(ctx, path)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return res, err
	}
	return im.QueueEntry(ctx, filepath.Base(path), data)
}

// MaxImportBody is the default HTTP envelope limit, retained for callers.
const MaxImportBody = 1 << 30

type importLimits struct {
	body, temporary          int64
	files, parts, concurrent int
	timeout                  time.Duration
}

// These are protective defaults, not measured player capacity requirements.
// Read once per request; environment changes require an operator-managed restart.
func readImportLimits() (importLimits, error) {
	l := importLimits{body: MaxImportBody, temporary: MaxImportBody, files: 128, parts: 256, concurrent: 2, timeout: 5 * time.Minute}
	for _, entry := range []struct {
		name  string
		value *int64
	}{
		{"RT_IMPORT_MAX_BYTES", &l.body}, {"RT_IMPORT_MAX_TEMP_BYTES", &l.temporary},
	} {
		if raw := strings.TrimSpace(os.Getenv(entry.name)); raw != "" {
			n, err := strconv.ParseInt(raw, 10, 64)
			if err != nil || n <= 0 || n == 1<<63-1 {
				return l, importFailure(500, entry.name, "configure a positive byte count below 9223372036854775807")
			}
			*entry.value = n
		}
	}
	for _, entry := range []struct {
		name  string
		value *int
	}{
		{"RT_IMPORT_MAX_FILES", &l.files}, {"RT_IMPORT_MAX_PARTS", &l.parts}, {"RT_IMPORT_MAX_CONCURRENT", &l.concurrent},
	} {
		if raw := strings.TrimSpace(os.Getenv(entry.name)); raw != "" {
			n, err := strconv.Atoi(raw)
			if err != nil || n <= 0 {
				return l, importFailure(500, entry.name, "configure a positive integer")
			}
			*entry.value = n
		}
	}
	if raw := strings.TrimSpace(os.Getenv("RT_IMPORT_TIMEOUT")); raw != "" {
		d, err := time.ParseDuration(raw)
		if err != nil || d <= 0 {
			return l, importFailure(500, "RT_IMPORT_TIMEOUT", "configure a positive Go duration, for example 5m")
		}
		l.timeout = d
	}
	return l, nil
}

// One process-wide admission gate also covers multiple Server instances. No
// unbounded wait queue: an admitted import holds its slot until cleanup ends.
var importAdmission struct {
	sync.Mutex
	active int
}

func admitImport(max int) bool {
	importAdmission.Lock()
	defer importAdmission.Unlock()
	if importAdmission.active >= max {
		return false
	}
	importAdmission.active++
	return true
}
func releaseImport() {
	importAdmission.Lock()
	importAdmission.active--
	importAdmission.Unlock()
}

type importError struct {
	status         int
	field, message string
}

func (e *importError) Error() string { return e.message }
func importFailure(status int, field, message string) error {
	return &importError{status, field, message}
}

func writeImportError(w http.ResponseWriter, ctx context.Context, err error, res ImportResult) {
	var failure *importError
	status, field, message := http.StatusBadRequest, "file", err.Error()
	var tooLarge *http.MaxBytesError
	switch {
	case errors.As(err, &failure):
		status, field, message = failure.status, failure.field, failure.message
	case errors.Is(ctx.Err(), context.DeadlineExceeded), errors.Is(err, context.DeadlineExceeded), os.IsTimeout(err):
		status, field, message = http.StatusRequestTimeout, "duration", "import deadline exceeded; inspect imported counts and retry a smaller batch (RT_IMPORT_TIMEOUT)"
	case errors.Is(ctx.Err(), context.Canceled), errors.Is(err, context.Canceled):
		status, field, message = http.StatusRequestTimeout, "duration", "import canceled; inspect imported counts before retrying"
	case errors.As(err, &tooLarge):
		status, field, message = http.StatusRequestEntityTooLarge, "body_bytes", "upload exceeds RT_IMPORT_MAX_BYTES; split the upload or ask the operator to adjust the limit"
	case errors.Is(err, io.ErrUnexpectedEOF):
		field, message = "multipart", "upload interrupted or malformed; resend the complete multipart upload"
	}
	if status == http.StatusTooManyRequests {
		w.Header().Set("Retry-After", "1")
	}
	writeJSON(w, status, struct {
		Error    string       `json:"error"`
		Field    string       `json:"field"`
		Imported ImportResult `json:"imported"`
	}{message, field, res})
}

type stagedImport struct{ name, path string }

// stageImports reads the complete envelope, including the epilogue, before any
// destination write. Content validation remains per record, not transactional.
func (s *Server) stageImports(ctx context.Context, r *http.Request, dir string, l importLimits) ([]stagedImport, error) {
	mr, err := r.MultipartReader()
	if err != nil {
		return nil, importFailure(400, "multipart", "expected multipart/form-data with file parts")
	}
	var staged []stagedImport
	var used int64
	for parts := 0; ; parts++ {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		part, err := mr.NextPart()
		if err == io.EOF {
			break
		}
		if err != nil {
			var tooLarge *http.MaxBytesError
			if errors.As(err, &tooLarge) {
				return nil, err
			}
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			return nil, importFailure(400, "multipart", "upload interrupted or malformed; resend the complete multipart upload")
		}
		if parts >= l.parts {
			return nil, importFailure(413, "parts", "too many multipart parts; split the upload (RT_IMPORT_MAX_PARTS)")
		}
		if part.FileName() == "" {
			if _, err := io.Copy(io.Discard, part); err != nil {
				return nil, err
			}
			if err := part.Close(); err != nil {
				return nil, err
			}
			continue
		}
		if len(staged) >= l.files {
			return nil, importFailure(413, "files", "too many files; split the upload (RT_IMPORT_MAX_FILES)")
		}
		path := filepath.Join(dir, strconv.Itoa(len(staged)))
		f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if err != nil {
			return nil, importFailure(500, "temporary_bytes", "cannot create temporary import file; check temporary directory permissions and free space")
		}
		n, copyErr := io.Copy(f, io.LimitReader(part, l.temporary-used))
		closeErr := f.Close()
		used += n
		if copyErr != nil {
			var diskError *os.PathError
			if errors.As(copyErr, &diskError) {
				return nil, importFailure(500, "temporary_bytes", "cannot store upload; check temporary directory permissions and free space")
			}
			return nil, copyErr
		}
		if closeErr != nil {
			return nil, importFailure(500, "temporary_bytes", "cannot store upload; check temporary directory free space")
		}
		// Probe without writing a byte beyond the staging budget.
		var extra [1]byte
		nExtra, readErr := io.ReadFull(part, extra[:])
		if nExtra > 0 {
			return nil, importFailure(413, "temporary_bytes", "upload exceeds temporary staging budget; split the upload (RT_IMPORT_MAX_TEMP_BYTES)")
		}
		if readErr != nil && readErr != io.EOF {
			return nil, readErr
		}
		if err := part.Close(); err != nil {
			return nil, err
		}
		staged = append(staged, stagedImport{part.FileName(), path})
	}
	// multipart.Reader can stop at the closing boundary before consuming the
	// request body. The HTTP envelope limit also applies to trailing bytes.
	if _, err := io.Copy(io.Discard, r.Body); err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if len(staged) == 0 {
		return nil, importFailure(400, "files", "no file; select a Rocket Tracker database or outbox JSON files")
	}
	return staged, nil
}

// importFiles handles POST /api/import for the authenticated/local scope.
// Budgets/envelope failures precede writes; content failures can be partial.
func (s *Server) importFiles(w http.ResponseWriter, r *http.Request) {
	sc, ok := s.scope(w, r)
	if !ok {
		return
	}
	l, err := readImportLimits()
	if err != nil {
		writeImportError(w, r.Context(), err, ImportResult{})
		return
	}
	if !admitImport(l.concurrent) {
		writeImportError(w, r.Context(), importFailure(429, "concurrency", "import capacity busy; retry later (RT_IMPORT_MAX_CONCURRENT)"), ImportResult{})
		return
	}
	defer releaseImport()
	ctx, cancel := context.WithTimeout(r.Context(), l.timeout)
	defer cancel()
	// A context alone does not interrupt a blocked HTTP body read. Set the
	// connection deadline as well; Close supports handlers with wrapped bodies.
	controller := http.NewResponseController(w)
	deadline, _ := ctx.Deadline()
	_ = controller.SetReadDeadline(deadline)
	defer controller.SetReadDeadline(time.Time{})
	body := r.Body
	stop := context.AfterFunc(ctx, func() { _ = body.Close() })
	defer stop()
	r.Body = http.MaxBytesReader(w, r.Body, l.body)
	if r.ContentLength > l.body {
		writeImportError(w, ctx, &http.MaxBytesError{Limit: l.body}, ImportResult{})
		return
	}
	dir, err := os.MkdirTemp(s.TempDir, "rltracker-import-")
	if err != nil {
		writeImportError(w, ctx, importFailure(500, "temporary_bytes", "cannot create import directory; check temporary directory permissions and free space"), ImportResult{})
		return
	}
	defer func() {
		if err := os.RemoveAll(dir); err != nil {
			s.Log.Warn("import temporary cleanup failed", "dir", dir, "err", err)
		}
	}()
	staged, err := s.stageImports(ctx, r, dir, l)
	if err != nil {
		writeImportError(w, ctx, err, ImportResult{})
		return
	}
	defTag := ""
	if s.Hub != nil {
		c, err := s.userSettings(ctx, sc.UserID())
		if err != nil {
			writeImportError(w, ctx, err, ImportResult{})
			return
		}
		defTag = c.DefaultTag
	} else if s.Config != nil {
		defTag = s.Config.Get().DefaultTag
	}
	im := &Importer{Scope: sc, DefaultTag: defTag}
	var res ImportResult
	for _, file := range staged {
		if err := ctx.Err(); err != nil {
			writeImportError(w, ctx, err, res)
			return
		}
		r1, err := s.importStaged(ctx, im, file)
		res.add(r1)
		if err != nil {
			writeImportError(w, ctx, fmt.Errorf("%s: %w", file.name, err), res)
			return
		}
	}
	s.Log.Info("files imported", "files", len(staged), "matches", res.Matches, "manual", res.Manual, "trainings", res.Trainings, "skipped", len(res.Skipped))
	writeJSON(w, http.StatusOK, res)
}

func (s *Server) importStaged(ctx context.Context, im *Importer, file stagedImport) (ImportResult, error) {
	f, err := os.Open(file.path)
	if err != nil {
		return ImportResult{}, err
	}
	defer f.Close()
	head := make([]byte, len(sqliteMagic))
	n, err := io.ReadFull(f, head)
	if err != nil && err != io.ErrUnexpectedEOF && err != io.EOF {
		return ImportResult{}, err
	}
	if IsSQLite(head[:n]) {
		return im.Database(ctx, file.path)
	}
	data, err := io.ReadAll(io.LimitReader(io.MultiReader(bytes.NewReader(head[:n]), f), MaxMatchBody+1))
	if err != nil {
		return ImportResult{}, err
	}
	if len(data) > MaxMatchBody {
		return ImportResult{Skipped: []string{file.name + ": not a database nor a queued match (4 MiB JSON limit)"}}, nil
	}
	return im.QueueEntry(ctx, file.name, data)
}
