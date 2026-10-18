package store

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"rocket-tracker/internal/domain"
)

// InstallationID is persisted with the database, including across restoration.
func (s *Store) InstallationID(ctx context.Context) (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	if _, err := s.db.ExecContext(ctx, `INSERT OR IGNORE INTO kv(k,v) VALUES('installation-id',?)`, hex.EncodeToString(b[:])); err != nil {
		return "", err
	}
	var id string
	err := s.db.QueryRowContext(ctx, `SELECT v FROM kv WHERE k='installation-id'`).Scan(&id)
	return id, err
}

func (sc *Scope) RecordResult(ctx context.Context, m *domain.Match, d *domain.Destination) error {
	return sc.saveWithDelivery(ctx, m, "", d, true)
}

func (sc *Scope) RecordTraining(ctx context.Context, t *domain.Training, d *domain.Destination) error {
	return sc.addTraining(ctx, t, "", d, true)
}

func (sc *Scope) queueTrainingDelivery(ctx context.Context, tx *sql.Tx, t *domain.Training, d *domain.Destination) error {
	if d == nil {
		return nil
	}
	return sc.queueDelivery(ctx, tx, "training", t.ID, t, d)
}

// Empty origin is historical/server/import ownership. It cannot acquire today's
// local recording destination. Local-only is explicit and survives reopen.
const localOnlyOrigin = "local-only"

func recordingOrigin(d *domain.Destination, local bool) string {
	if d != nil {
		return canonicalDestination(*d)
	}
	if local {
		return localOnlyOrigin
	}
	return ""
}
func (sc *Scope) persistOrigin(ctx context.Context, tx *sql.Tx, kind string, id int64, origin string) error {
	_, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO record_origins(user_id,kind,record_id,destination) VALUES(?,?,?,?)`, sc.uid, kind, id, origin)
	return err
}

func canonicalDestination(d domain.Destination) string {
	d.Server = strings.TrimRight(strings.TrimSpace(d.Server), "/")
	b, _ := json.Marshal(d)
	return string(b)
}

func (sc *Scope) queueDelivery(ctx context.Context, tx *sql.Tx, kind string, id int64, value any, d *domain.Destination) error {
	payload, err := json.Marshal(value)
	if err != nil {
		return err
	}
	destination := canonicalDestination(*d)
	if _, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO record_origins(user_id,kind,record_id,destination) VALUES(?,?,?,?)`, sc.uid, kind, id, destination); err != nil {
		return err
	}
	var origin string
	if err := tx.QueryRowContext(ctx, `SELECT destination FROM record_origins WHERE user_id=? AND kind=? AND record_id=?`, sc.uid, kind, id).Scan(&origin); err != nil {
		return err
	}
	if origin != destination {
		return errors.New("record origin differs from delivery destination")
	}

	// The identity is local to this database/user/record/destination, not a game GUID.
	var installation string
	err = tx.QueryRowContext(ctx, `SELECT v FROM kv WHERE k='installation-id'`).Scan(&installation)
	if errors.Is(err, sql.ErrNoRows) {
		var b [16]byte
		if _, err = rand.Read(b[:]); err != nil {
			return err
		}
		installation = hex.EncodeToString(b[:])
		_, err = tx.ExecContext(ctx, `INSERT INTO kv(k,v) VALUES('installation-id',?)`, installation)
	}
	if err != nil {
		return err
	}
	key := sha256.Sum256([]byte(fmt.Sprintf("%s/%d/%s/%d/%s", installation, sc.uid, kind, id, destination)))
	state := "pending"
	if !d.Bound() {
		state = "held"
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO delivery_intents(user_id,kind,record_id,destination,delivery_key,payload,state)
 VALUES(?,?,?,?,?,?,?) ON CONFLICT(user_id,kind,record_id,destination) DO UPDATE SET
 revision=revision+1,payload=excluded.payload,state=excluded.state,reason=''
 WHERE payload<>excluded.payload`, sc.uid, kind, id, destination, hex.EncodeToString(key[:]), string(payload), state)
	return err
}

func (sc *Scope) PendingDeliveries(ctx context.Context, d domain.Destination) ([]domain.Delivery, error) {
	if !d.Bound() {
		return nil, errors.New("delivery destination is not verified")
	}
	rows, err := sc.s.db.QueryContext(ctx, `SELECT id,revision,delivery_key,kind,payload FROM delivery_intents WHERE user_id=? AND destination=? AND state='pending' ORDER BY id LIMIT 100`, sc.uid, canonicalDestination(d))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []domain.Delivery{}
	for rows.Next() {
		var v domain.Delivery
		var payload string
		if err := rows.Scan(&v.ID, &v.Revision, &v.Key, &v.Kind, &payload); err != nil {
			return nil, err
		}
		v.Destination = d
		v.State = "pending"
		switch v.Kind {
		case "match":
			v.Match = new(domain.Match)
			err = json.Unmarshal([]byte(payload), v.Match)
		case "training":
			v.Training = new(domain.Training)
			err = json.Unmarshal([]byte(payload), v.Training)
		default:
			err = errors.New("unknown delivery kind")
		}
		if err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

// CompleteDelivery cannot acknowledge a newer revision or another destination.
func (sc *Scope) CompleteDelivery(ctx context.Context, v domain.Delivery, rejected bool) error {
	if rejected {
		_, err := sc.s.db.ExecContext(ctx, `UPDATE delivery_intents SET state='rejected',reason='server rejected payload' WHERE id=? AND revision=? AND user_id=? AND destination=?`, v.ID, v.Revision, sc.uid, canonicalDestination(v.Destination))
		return err
	}
	_, err := sc.s.db.ExecContext(ctx, `DELETE FROM delivery_intents WHERE id=? AND revision=? AND user_id=? AND destination=?`, v.ID, v.Revision, sc.uid, canonicalDestination(v.Destination))
	return err
}

type DeliveryStatus struct {
	Pending    int `json:"pending"`
	Held       int `json:"held"`
	Rejected   int `json:"rejected"`
	LegacyHeld int `json:"legacy_held"`
}

func (sc *Scope) DeliveryStatus(ctx context.Context) (DeliveryStatus, error) {
	var v DeliveryStatus
	err := sc.s.db.QueryRowContext(ctx, `SELECT COALESCE(SUM(state='pending'),0),COALESCE(SUM(state='held'),0),COALESCE(SUM(state='rejected'),0) FROM delivery_intents WHERE user_id=?`, sc.uid).Scan(&v.Pending, &v.Held, &v.Rejected)
	if err != nil {
		return v, err
	}
	err = sc.s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM legacy_deliveries`).Scan(&v.LegacyHeld)
	return v, err
}

// PreserveLegacyOutbox copies bounded queue documents without inferring their
// account from today's token. Sources (including rejected/asides) remain intact.
func (s *Store) PreserveLegacyOutbox(ctx context.Context, root string) error {
	count := 0
	var totalBytes int64
	return filepath.WalkDir(root, func(path string, e fs.DirEntry, err error) error {
		if errors.Is(err, os.ErrNotExist) && path == root {
			return nil
		}
		if err != nil {
			return err
		}
		if e.Type()&os.ModeSymlink != 0 {
			return errors.New("legacy queue contains a symbolic link")
		}
		if e.IsDir() {
			return nil
		}
		if !strings.HasSuffix(e.Name(), ".json") {
			return nil
		}
		count++
		if count > 10000 {
			return errors.New("legacy queue exceeds 10000 documents; preserve and migrate in batches")
		}
		fi, err := e.Info()
		if err != nil {
			return err
		}
		if fi.Size() > 4<<20 {
			return fmt.Errorf("legacy document exceeds 4 MiB: %s", e.Name())
		}
		totalBytes += fi.Size()
		if totalBytes > 64<<20 {
			return errors.New("legacy queue exceeds 64 MiB; preserve and migrate in batches")
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		digest := sha256.Sum256(b)
		source := filepath.Base(root) + "/" + filepath.ToSlash(rel) + ":" + hex.EncodeToString(digest[:])
		_, err = s.db.ExecContext(ctx, `INSERT OR IGNORE INTO legacy_deliveries(source,data,reason) VALUES(?,?,'legacy destination/account not verified; source retained for explicit scoped import')`, source, b)
		return err
	})
}
