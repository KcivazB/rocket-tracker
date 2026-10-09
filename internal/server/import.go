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
	"strings"

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
	Matches int      `json:"matches"`
	Manual  int      `json:"manual"`
	Skipped []string `json:"skipped,omitempty"` // "<file>: <reason>"
}

func (r *ImportResult) add(o ImportResult) {
	r.Matches += o.Matches
	r.Manual += o.Manual
	r.Skipped = append(r.Skipped, o.Skipped...)
}

// sqliteMagic starts every SQLite database file.
var sqliteMagic = []byte("SQLite format 3\x00")

// IsSQLite reports whether head (the first bytes of a file) is a database.
func IsSQLite(head []byte) bool { return bytes.HasPrefix(head, sqliteMagic) }

// QueueEntry imports one file of the agent's queue ({"key", "match"}).
func (im *Importer) QueueEntry(ctx context.Context, name string, data []byte) (ImportResult, error) {
	var res ImportResult
	var e struct {
		Key   string       `json:"key"`
		Match *store.Match `json:"match"`
	}
	if err := json.Unmarshal(data, &e); err != nil || e.Match == nil {
		res.Skipped = append(res.Skipped, name+": not a queued match")
		return res, nil
	}
	if e.Key == "" {
		e.Key = strings.TrimSuffix(filepath.Base(name), ".json")
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

// Database imports the matches and hand-entered games of a local database,
// with the keys `rltracker agent import` uses (the two never duplicate).
func (im *Importer) Database(ctx context.Context, path string) (ImportResult, error) {
	var res ImportResult
	src, err := store.Open(path)
	if err != nil {
		return res, fmt.Errorf("not a Rocket Tracker database: %w", err)
	}
	defer src.Close()
	ms, err := src.List(ctx)
	if err != nil {
		return res, err
	}
	for _, m := range ms {
		key := store.ImportKey(m)
		if err := IngestAgentMatch(ctx, im.Scope, im.DefaultTag, key, m); errors.Is(err, ErrInvalidMatch) {
			res.Skipped = append(res.Skipped, key+": "+err.Error())
			continue
		} else if err != nil {
			return res, err
		}
		res.Matches++
	}
	ds, err := src.ListManual(ctx)
	if err != nil {
		return res, err
	}
	for _, d := range ds {
		if err := im.Scope.SetManual(ctx, d); err != nil {
			return res, fmt.Errorf("manual games %s %s: %w", d.Day, d.Mode, err)
		}
		res.Manual++
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

// MaxImportBody bounds an upload (a local database with years of matches and
// movement data stays well under this).
const MaxImportBody = 1 << 30

// importFiles handles POST /api/import: a multipart upload of rltracker.db
// and/or outbox .json files, stored for the signed-in player.
func (s *Server) importFiles(w http.ResponseWriter, r *http.Request) {
	sc, ok := s.scope(w, r)
	if !ok {
		return
	}
	defTag := ""
	if s.Hub != nil {
		c, err := s.userSettings(r.Context(), sc.UserID())
		if err != nil {
			writeErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		defTag = c.DefaultTag
	} else if s.Config != nil {
		defTag = s.Config.Get().DefaultTag
	}
	im := &Importer{Scope: sc, DefaultTag: defTag}

	r.Body = http.MaxBytesReader(w, r.Body, MaxImportBody)
	mr, err := r.MultipartReader()
	if err != nil {
		writeErr(w, http.StatusBadRequest, "expected a multipart upload")
		return
	}
	var res ImportResult
	files := 0
	for {
		part, err := mr.NextPart()
		if err == io.EOF {
			break
		} else if err != nil {
			writeErr(w, http.StatusBadRequest, "upload interrupted: "+err.Error())
			return
		}
		name := part.FileName()
		if name == "" {
			part.Close()
			continue
		}
		files++
		r1, err := s.importPart(r.Context(), im, name, part)
		part.Close()
		res.add(r1)
		if err != nil {
			s.Log.Warn("import", "file", name, "err", err)
			writeErr(w, http.StatusBadRequest, name+": "+err.Error())
			return
		}
	}
	if files == 0 {
		writeErr(w, http.StatusBadRequest, "no file")
		return
	}
	s.Log.Info("files imported", "files", files, "matches", res.Matches, "manual", res.Manual, "skipped", len(res.Skipped))
	writeJSON(w, http.StatusOK, res)
}

// importPart imports one uploaded file: a database goes through a temporary
// file (SQLite needs one), a queued match is read in memory.
func (s *Server) importPart(ctx context.Context, im *Importer, name string, part io.Reader) (ImportResult, error) {
	head := make([]byte, len(sqliteMagic))
	n, err := io.ReadFull(part, head)
	if err != nil && !errors.Is(err, io.ErrUnexpectedEOF) && !errors.Is(err, io.EOF) {
		return ImportResult{}, err
	}
	body := io.MultiReader(bytes.NewReader(head[:n]), part)
	if !IsSQLite(head[:n]) {
		data, err := io.ReadAll(io.LimitReader(body, MaxMatchBody+1))
		if err != nil {
			return ImportResult{}, err
		}
		if len(data) > MaxMatchBody {
			return ImportResult{Skipped: []string{name + ": not a database nor a queued match"}}, nil
		}
		return im.QueueEntry(ctx, name, data)
	}
	dir, err := os.MkdirTemp("", "rltracker-import-")
	if err != nil {
		return ImportResult{}, err
	}
	defer os.RemoveAll(dir)
	tmp := filepath.Join(dir, "import.db")
	f, err := os.Create(tmp)
	if err != nil {
		return ImportResult{}, err
	}
	if _, err := io.Copy(f, body); err != nil {
		f.Close()
		return ImportResult{}, err
	}
	if err := f.Close(); err != nil {
		return ImportResult{}, err
	}
	return im.Database(ctx, tmp)
}
