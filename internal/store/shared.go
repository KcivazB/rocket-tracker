package store

import "context"

// userScanner reads a leading user_id column, then lets scanMatch read the rest.
type userScanner struct {
	r   scanner
	uid *int64
}

func (u userScanner) Scan(dest ...any) error { return u.r.Scan(append([]any{u.uid}, dest...)...) }

// Shared is one of a user's online matches that other server accounts
// recorded too (same match guid): they played it together, as teammates or
// opponents.
type Shared struct {
	MatchID int64        `json:"id"`
	GUID    string       `json:"guid"`
	MyTeam  int          `json:"my_team"`
	With    []SharedWith `json:"with"`
}

// SharedWith is another account's copy of a shared match.
type SharedWith struct {
	UserID    int64  `json:"-"`
	MatchID   int64  `json:"id"`
	Team      int    `json:"team"`       // their team
	PrimaryID string `json:"primary_id"` // their platform id in that match
}

// Shared lists the scope user's online matches recorded by other accounts,
// most recent first.
func (sc *Scope) Shared(ctx context.Context) ([]Shared, error) {
	rows, err := sc.s.db.QueryContext(ctx, `SELECT a.id, a.guid, COALESCE(json_extract(a.data, '$.my_team'), 0),
			b.user_id, b.id, COALESCE(json_extract(b.data, '$.my_team'), 0), COALESCE(json_extract(b.data, '$.me.primary_id'), '')
		FROM matches a JOIN matches b ON b.guid = a.guid AND b.user_id <> a.user_id
		WHERE a.user_id = ? AND a.online = 1 AND a.guid <> '' AND b.user_id > 0 AND b.online = 1
		ORDER BY a.started_at DESC, a.id, b.user_id`, sc.uid)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Shared{}
	for rows.Next() {
		var s Shared
		var w SharedWith
		if err := rows.Scan(&s.MatchID, &s.GUID, &s.MyTeam, &w.UserID, &w.MatchID, &w.Team, &w.PrimaryID); err != nil {
			return nil, err
		}
		if n := len(out); n > 0 && out[n-1].MatchID == s.MatchID {
			out[n-1].With = append(out[n-1].With, w)
			continue
		}
		s.With = []SharedWith{w}
		out = append(out, s)
	}
	return out, rows.Err()
}

// UserMatch is a match with the account that recorded it.
type UserMatch struct {
	UserID int64
	Match  *Match
}

// SharedCopies returns the other accounts' copies of the scope user's match
// id (empty when nobody else recorded it).
func (sc *Scope) SharedCopies(ctx context.Context, id int64) ([]UserMatch, error) {
	m, err := sc.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	out := []UserMatch{}
	if !m.Online || m.GUID == "" {
		return out, nil
	}
	rows, err := sc.s.db.QueryContext(ctx, `SELECT user_id, id, tag, data FROM matches
		WHERE guid = ? AND user_id <> ? AND user_id > 0 AND online = 1 ORDER BY user_id`, m.GUID, sc.uid)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var uid int64
		cm, err := scanMatch(userScanner{rows, &uid})
		if err != nil {
			return nil, err
		}
		if cm != nil { // nil: corrupt row, skipped
			out = append(out, UserMatch{UserID: uid, Match: cm})
		}
	}
	return out, rows.Err()
}
