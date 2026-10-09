package store

import (
	"context"
	"errors"
	"fmt"
	"time"
)

// Rank is a rank and/or MMR the player noted for a playlist (the Stats API
// gives neither): typically at the end of a session.
type Rank struct {
	ID       int64     `json:"id"`
	At       time.Time `json:"at"`
	Playlist string    `json:"playlist"` // see Playlists
	Tier     *int      `json:"tier"`     // 0 unranked, 1-3 Bronze I-III ... 19-21 Grand Champion I-III, 22 Supersonic Legend; nil = not noted
	Division *int      `json:"division"` // 1-4 (not for unranked / Supersonic Legend); nil = not noted
	MMR      *int      `json:"mmr"`      // nil = not noted
}

// Playlists are the competitive playlists a rank can be noted for.
var Playlists = []string{"1v1", "2v2", "3v3", "hoops", "rumble", "dropshot", "snowday", "tournament"}

// TierSSL is the highest tier, which has no divisions.
const TierSSL = 22

// Validate checks a rank entry: a known playlist, and a rank or an MMR.
func (r *Rank) Validate() error {
	ok := false
	for _, p := range Playlists {
		ok = ok || p == r.Playlist
	}
	switch {
	case !ok:
		return fmt.Errorf("unknown playlist %q", r.Playlist)
	case r.Tier == nil && r.MMR == nil:
		return errors.New("give a rank or an MMR")
	case r.Tier != nil && (*r.Tier < 0 || *r.Tier > TierSSL):
		return errors.New("tier must be 0 (unranked) to 22 (Supersonic Legend)")
	case r.Division != nil && (*r.Division < 1 || *r.Division > 4):
		return errors.New("division must be 1 to 4")
	case r.Division != nil && (r.Tier == nil || *r.Tier == 0 || *r.Tier == TierSSL):
		return errors.New("a division needs a tier that has divisions")
	case r.MMR != nil && (*r.MMR < 0 || *r.MMR > 5000):
		return errors.New("mmr must be 0 to 5000")
	case r.At.IsZero():
		return errors.New("missing date")
	}
	return nil
}

// Ranks lists the user's rank entries, oldest first.
func (sc *Scope) Ranks(ctx context.Context) ([]Rank, error) {
	rows, err := sc.s.db.QueryContext(ctx, `SELECT id, at, playlist, tier, division, mmr FROM ranks
		WHERE user_id = ? ORDER BY at, id`, sc.uid)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Rank{}
	for rows.Next() {
		var r Rank
		var at string
		var tier, div, mmr int
		if err := rows.Scan(&r.ID, &at, &r.Playlist, &tier, &div, &mmr); err != nil {
			return nil, err
		}
		r.At, _ = time.Parse(tsLayout, at)
		r.Tier, r.Division, r.MMR = optional(tier, -1), optional(div, 0), optional(mmr, -1)
		out = append(out, r)
	}
	return out, rows.Err()
}

func optional(v, none int) *int {
	if v == none {
		return nil
	}
	return &v
}

func orNone(p *int, none int) int {
	if p == nil {
		return none
	}
	return *p
}

// AddRank stores a rank entry and sets r.ID.
func (sc *Scope) AddRank(ctx context.Context, r *Rank) error {
	r.At = r.At.UTC().Truncate(time.Second)
	if err := r.Validate(); err != nil {
		return err
	}
	res, err := sc.s.db.ExecContext(ctx, `INSERT INTO ranks (user_id, at, playlist, tier, division, mmr) VALUES (?, ?, ?, ?, ?, ?)`,
		sc.uid, r.At.Format(tsLayout), r.Playlist, orNone(r.Tier, -1), orNone(r.Division, 0), orNone(r.MMR, -1))
	if err != nil {
		return err
	}
	r.ID, err = res.LastInsertId()
	return err
}

// DeleteRank removes one of the user's rank entries.
func (sc *Scope) DeleteRank(ctx context.Context, id int64) error {
	res, err := sc.s.db.ExecContext(ctx, `DELETE FROM ranks WHERE id = ? AND user_id = ?`, id, sc.uid)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}
