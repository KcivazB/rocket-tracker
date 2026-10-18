package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"rocket-tracker/internal/domain"
)

// Compatibility names for domain training values and rules.
type Training = domain.Training

var Focuses = domain.Focuses

const MaxTraining = domain.MaxTraining
const TrainingJoin = domain.TrainingJoin

var ErrInvalidTraining = domain.ErrInvalidTraining

func ValidFocus(f string) bool { return domain.ValidFocus(f) }

// TrainingImportKey identifies a training of a local database once imported
// on a server, like ImportKey for matches.
func TrainingImportKey(t *Training) string {
	return "import:training:" + strconv.FormatInt(t.StartedAt.Unix(), 10)
}

// Trainings lists the user's trainings, oldest first.
func (sc *Scope) Trainings(ctx context.Context) ([]Training, error) {
	rows, err := sc.s.db.QueryContext(ctx, `SELECT id, data FROM trainings WHERE user_id = ? ORDER BY started_at, id`, sc.uid)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Training{}
	for rows.Next() {
		var t Training
		var data string
		if err := rows.Scan(&t.ID, &data); err != nil {
			return nil, err
		}
		id := t.ID
		if err := json.Unmarshal([]byte(data), &t); err != nil {
			continue
		}
		t.ID = id
		out = append(out, t)
	}
	return out, rows.Err()
}

// AddTraining stores a training and sets t.ID. key (may be empty)
// identifies the sender's copy: the same key again is merged into the row it
// went to. A training overlapping one already stored (within TrainingJoin) is
// merged into it. Merging never shrinks a training nor replaces its focus, so
// a replayed upload changes nothing (even when a neighbor was merged into the
// row since, or the focus was edited).
func (sc *Scope) AddTraining(ctx context.Context, t *Training, key string) error {
	return sc.addTraining(ctx, t, key, nil, false)
}

func (sc *Scope) addTraining(ctx context.Context, t *Training, key string, destination *domain.Destination, local bool) error {
	if err := t.Normalize(); err != nil {
		return err
	}
	if len(key) > 200 {
		return fmt.Errorf("%w: key too long", ErrInvalidTraining)
	}
	tx, err := sc.s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	origin := recordingOrigin(destination, local)
	originWhere := " AND COALESCE((SELECT o.destination FROM record_origins o WHERE o.user_id=trainings.user_id AND o.kind='training' AND o.record_id=trainings.id),'')=?"
	profileWhere := " AND (?='' OR COALESCE(json_extract(data,'$.profile'),'') IN ('',?))"
	bounds := " AND started_at>=? AND ended_at<=?"
	common := []any{t.Profile, t.Profile, origin, t.EndedAt.Add(-MaxTraining).Format(tsLayout), t.StartedAt.Add(MaxTraining).Format(tsLayout)}
	// A bounded split may leave several candidates. Skip an interval-full row
	// instead of inserting another copy before reaching the replay's own row.
	findMerge := func(query string, args []any) (int64, *Training, error) {
		rows, err := tx.QueryContext(ctx, query, args...)
		if err != nil {
			return 0, nil, err
		}
		defer rows.Close()
		for rows.Next() {
			var id int64
			var data string
			if err := rows.Scan(&id, &data); err != nil {
				return 0, nil, err
			}
			var old Training
			if err := json.Unmarshal([]byte(data), &old); err != nil {
				return 0, nil, err
			}
			if old.Normalize() == nil && old.CanMerge(t) {
				return id, &old, nil
			}
		}
		return 0, nil, rows.Err()
	}
	id, old, err := findMerge(`SELECT id,data FROM trainings WHERE user_id=? AND ?!='' AND (src_key=? OR EXISTS (SELECT 1 FROM training_sources k WHERE k.user_id=trainings.user_id AND k.record_id=trainings.id AND k.src_key=?))`+profileWhere+originWhere+bounds+` ORDER BY id`, append([]any{sc.uid, key, key, key}, common...))
	if err != nil {
		return err
	}
	if old == nil {
		args := []any{sc.uid, t.EndedAt.Add(TrainingJoin).Format(tsLayout), t.StartedAt.Add(-TrainingJoin).Format(tsLayout)}
		id, old, err = findMerge(`SELECT id,data FROM trainings WHERE user_id=? AND started_at<=? AND ended_at>=?`+profileWhere+originWhere+bounds+` ORDER BY started_at,id`, append(args, common...))
		if err != nil {
			return err
		}
	}
	if old != nil {
		old.Merge(t)
		*t = *old
	}
	t.ID = id
	b, err := json.Marshal(t)
	if err != nil {
		return err
	}
	if id != 0 {
		_, err = tx.ExecContext(ctx, `UPDATE trainings SET started_at = ?, ended_at = ?, data = ? WHERE id = ?`,
			t.StartedAt.Format(tsLayout), t.EndedAt.Format(tsLayout), string(b), id)
	} else {
		var res sql.Result
		res, err = tx.ExecContext(ctx, `INSERT INTO trainings (user_id, src_key, started_at, ended_at, data) VALUES (?, ?, ?, ?, ?)`,
			sc.uid, key, t.StartedAt.Format(tsLayout), t.EndedAt.Format(tsLayout), string(b))
		if err == nil {
			t.ID, err = res.LastInsertId()
		}
	}
	if err != nil {
		return err
	}
	if err := sc.persistOrigin(ctx, tx, "training", t.ID, origin); err != nil {
		return err
	}
	if key != "" {
		if _, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO training_sources(user_id,src_key,record_id) VALUES(?,?,?)`, sc.uid, key, t.ID); err != nil {
			return err
		}
	}
	if err := t.Normalize(); err != nil {
		return err
	}
	if err := sc.queueTrainingDelivery(ctx, tx, t, destination); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	sc.s.touch(sc.uid)
	return nil
}

// SetTrainingFocus changes the mechanic a training was about.
func (sc *Scope) SetTrainingFocus(ctx context.Context, id int64, focus string) (*Training, error) {
	focus = strings.ToLower(strings.TrimSpace(focus))
	if !ValidFocus(focus) {
		return nil, fmt.Errorf("%w: unknown focus %q", ErrInvalidTraining, focus)
	}
	var data string
	err := sc.s.db.QueryRowContext(ctx, `SELECT data FROM trainings WHERE id = ? AND user_id = ?`, id, sc.uid).Scan(&data)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	} else if err != nil {
		return nil, err
	}
	var t Training
	if err := json.Unmarshal([]byte(data), &t); err != nil {
		return nil, err
	}
	t.ID, t.Focus = id, focus
	b, err := json.Marshal(t)
	if err != nil {
		return nil, err
	}
	if _, err = sc.s.db.ExecContext(ctx, `UPDATE trainings SET data = ? WHERE id = ?`, string(b), id); err != nil {
		return nil, err
	}
	sc.s.touch(sc.uid)
	return &t, nil
}

// TrainingCount returns the number of the user's trainings.
func (sc *Scope) TrainingCount(ctx context.Context) (int, error) {
	var n int
	err := sc.s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM trainings WHERE user_id = ?`, sc.uid).Scan(&n)
	return n, err
}

// DeleteTraining removes one of the user's trainings.
func (sc *Scope) DeleteTraining(ctx context.Context, id int64) error {
	res, err := sc.s.db.ExecContext(ctx, `DELETE FROM trainings WHERE id = ? AND user_id = ?`, id, sc.uid)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	sc.s.touch(sc.uid)
	return nil
}
