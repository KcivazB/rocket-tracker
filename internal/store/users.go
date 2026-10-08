package store

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"
)

// User is an account of the server mode.
type User struct {
	ID          int64  `json:"id"`
	Handle      string `json:"handle"` // stable, URL-safe
	Name        string `json:"name"`
	Email       string `json:"-"`
	CreatedAt   string `json:"created_at"`
	LastLoginAt string `json:"-"`
}

const userCols = `id, handle, name, email, created_at, last_login_at`

func scanUser(r scanner) (*User, error) {
	u := &User{}
	err := r.Scan(&u.ID, &u.Handle, &u.Name, &u.Email, &u.CreatedAt, &u.LastLoginAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return u, err
}

var handleJunk = regexp.MustCompile(`[^a-z0-9_-]+`)

// unaccent folds the common Latin accents so "Chloé" gives "chloe".
var unaccent = strings.NewReplacer(
	"à", "a", "á", "a", "â", "a", "ä", "a", "ã", "a", "å", "a", "æ", "ae",
	"ç", "c", "è", "e", "é", "e", "ê", "e", "ë", "e", "ì", "i", "í", "i", "î", "i", "ï", "i",
	"ñ", "n", "ò", "o", "ó", "o", "ô", "o", "ö", "o", "õ", "o", "ø", "o", "œ", "oe",
	"ù", "u", "ú", "u", "û", "u", "ü", "u", "ý", "y", "ÿ", "y", "ß", "ss")

// Slug turns a username into a handle candidate ([a-z0-9_-], 1-32 chars).
func Slug(s string) string {
	s = unaccent.Replace(strings.ToLower(strings.TrimSpace(s)))
	if i := strings.IndexByte(s, '@'); i > 0 {
		s = s[:i]
	}
	s = strings.Trim(handleJunk.ReplaceAllString(s, "-"), "-_")
	if len(s) > 32 {
		s = strings.Trim(s[:32], "-_")
	}
	if s == "" {
		s = "player"
	}
	return s
}

// UpsertOIDCUser returns the account of an identity provider subject,
// creating it on first login. The handle is chosen once (from username) and
// kept; name and email follow the provider.
func (s *Store) UpsertOIDCUser(ctx context.Context, sub, username, name, email string) (*User, error) {
	if sub == "" {
		return nil, errors.New("store: empty subject")
	}
	if name == "" {
		name = username
	}
	now := time.Now().UTC().Format(tsLayout)
	u, err := scanUser(s.db.QueryRowContext(ctx, `SELECT `+userCols+` FROM users WHERE oidc_sub = ?`, sub))
	if err == nil {
		if _, err := s.db.ExecContext(ctx, `UPDATE users SET name = ?, email = ?, last_login_at = ? WHERE id = ?`, name, email, now, u.ID); err != nil {
			return nil, err
		}
		u.Name, u.Email, u.LastLoginAt = name, email, now
		return u, nil
	}
	if !errors.Is(err, ErrNotFound) {
		return nil, err
	}
	base := Slug(username)
	if username == "" {
		base = Slug(name)
	}
	for i := 1; i < 1000; i++ {
		h := base
		if i > 1 {
			h = fmt.Sprintf("%s-%d", base, i)
		}
		res, err := s.db.ExecContext(ctx, `INSERT INTO users (oidc_sub, handle, name, email, last_login_at) VALUES (?, ?, ?, ?, ?)
			ON CONFLICT DO NOTHING`, sub, h, name, email, now)
		if err != nil {
			return nil, err
		}
		if n, _ := res.RowsAffected(); n == 1 {
			id, _ := res.LastInsertId()
			return s.UserByID(ctx, id)
		}
		// The subject may have been inserted concurrently.
		if u, err := scanUser(s.db.QueryRowContext(ctx, `SELECT `+userCols+` FROM users WHERE oidc_sub = ?`, sub)); err == nil {
			return u, nil
		}
	}
	return nil, errors.New("store: no free handle")
}

// UserByID returns an account.
func (s *Store) UserByID(ctx context.Context, id int64) (*User, error) {
	return scanUser(s.db.QueryRowContext(ctx, `SELECT `+userCols+` FROM users WHERE id = ?`, id))
}

// UserByHandle returns an account.
func (s *Store) UserByHandle(ctx context.Context, handle string) (*User, error) {
	return scanUser(s.db.QueryRowContext(ctx, `SELECT `+userCols+` FROM users WHERE handle = ?`, strings.ToLower(handle)))
}

// Users lists the accounts by name.
func (s *Store) Users(ctx context.Context) ([]*User, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+userCols+` FROM users ORDER BY name COLLATE NOCASE, id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*User{}
	for rows.Next() {
		u, err := scanUser(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, u)
	}
	return out, rows.Err()
}

// UserSettings returns the raw JSON settings of an account ("" when unset).
func (s *Store) UserSettings(ctx context.Context, uid int64) (string, error) {
	var v string
	err := s.db.QueryRowContext(ctx, `SELECT settings FROM users WHERE id = ?`, uid).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrNotFound
	}
	return v, err
}

// SetUserSettings stores the raw JSON settings of an account.
func (s *Store) SetUserSettings(ctx context.Context, uid int64, settings string) error {
	res, err := s.db.ExecContext(ctx, `UPDATE users SET settings = ? WHERE id = ?`, settings, uid)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// ---------------------------------------------------------------- tokens

// NewToken returns a random secret with a readable prefix.
func NewToken(prefix string) string {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		panic(err) // crypto/rand never fails on supported platforms
	}
	return prefix + base64.RawURLEncoding.EncodeToString(b)
}

// tokenHash is what the database keeps of a secret.
func tokenHash(token string) string {
	h := sha256.Sum256([]byte(token))
	return hex.EncodeToString(h[:])
}

// CreateSession opens a browser session and returns its secret.
func (s *Store) CreateSession(ctx context.Context, uid int64, ttl time.Duration) (string, error) {
	tok := NewToken("rts_")
	exp := time.Now().Add(ttl).UTC().Format(tsLayout)
	_, err := s.db.ExecContext(ctx, `INSERT INTO sessions (token_hash, user_id, expires_at) VALUES (?, ?, ?)`, tokenHash(tok), uid, exp)
	return tok, err
}

// SessionUser returns the account of a valid session. The expiry slides to
// now+ttl when less than half of it is left.
func (s *Store) SessionUser(ctx context.Context, token string, ttl time.Duration) (*User, error) {
	if token == "" {
		return nil, ErrNotFound
	}
	h := tokenHash(token)
	u, exp := &User{}, ""
	err := s.db.QueryRowContext(ctx, `SELECT u.id, u.handle, u.name, u.email, u.created_at, u.last_login_at, s.expires_at
		FROM sessions s JOIN users u ON u.id = s.user_id WHERE s.token_hash = ?`, h).
		Scan(&u.ID, &u.Handle, &u.Name, &u.Email, &u.CreatedAt, &u.LastLoginAt, &exp)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	} else if err != nil {
		return nil, err
	}
	t, err := time.Parse(tsLayout, exp)
	now := time.Now()
	if err != nil || now.After(t) {
		_, _ = s.db.ExecContext(ctx, `DELETE FROM sessions WHERE token_hash = ?`, h)
		return nil, ErrNotFound
	}
	if t.Sub(now) < ttl/2 {
		_, _ = s.db.ExecContext(ctx, `UPDATE sessions SET expires_at = ? WHERE token_hash = ?`, now.Add(ttl).UTC().Format(tsLayout), h)
	}
	return u, nil
}

// DeleteSession ends a session.
func (s *Store) DeleteSession(ctx context.Context, token string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM sessions WHERE token_hash = ?`, tokenHash(token))
	return err
}

// PurgeSessions removes expired sessions.
func (s *Store) PurgeSessions(ctx context.Context) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM sessions WHERE expires_at < ?`, time.Now().UTC().Format(tsLayout))
	return err
}

// Device is a machine running an agent, authenticated by its own token.
type Device struct {
	ID         int64  `json:"id"`
	Name       string `json:"name"`
	CreatedAt  string `json:"created_at"`
	LastSeenAt string `json:"last_seen_at"`
}

// CreateDevice registers a device and returns its token (shown only once).
func (sc *Scope) CreateDevice(ctx context.Context, name string) (*Device, string, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		name = "PC"
	}
	if r := []rune(name); len(r) > 60 {
		name = string(r[:60])
	}
	tok := NewToken("rtk_")
	res, err := sc.s.db.ExecContext(ctx, `INSERT INTO devices (user_id, name, token_hash) VALUES (?, ?, ?)`, sc.uid, name, tokenHash(tok))
	if err != nil {
		return nil, "", err
	}
	id, _ := res.LastInsertId()
	d := &Device{ID: id, Name: name}
	err = sc.s.db.QueryRowContext(ctx, `SELECT created_at FROM devices WHERE id = ?`, id).Scan(&d.CreatedAt)
	return d, tok, err
}

// Devices lists the user's devices, newest first.
func (sc *Scope) Devices(ctx context.Context) ([]Device, error) {
	rows, err := sc.s.db.QueryContext(ctx, `SELECT id, name, created_at, last_seen_at FROM devices WHERE user_id = ? ORDER BY id DESC`, sc.uid)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Device{}
	for rows.Next() {
		var d Device
		if err := rows.Scan(&d.ID, &d.Name, &d.CreatedAt, &d.LastSeenAt); err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

// DeleteDevice revokes a device token.
func (sc *Scope) DeleteDevice(ctx context.Context, id int64) error {
	res, err := sc.s.db.ExecContext(ctx, `DELETE FROM devices WHERE id = ? AND user_id = ?`, id, sc.uid)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// DeviceUser authenticates an agent token and records when it was last used
// (at most once a minute).
func (s *Store) DeviceUser(ctx context.Context, token string) (*User, *Device, error) {
	if !strings.HasPrefix(token, "rtk_") {
		return nil, nil, ErrNotFound
	}
	d := &Device{}
	var uid int64
	err := s.db.QueryRowContext(ctx, `SELECT id, user_id, name, created_at, last_seen_at FROM devices WHERE token_hash = ?`, tokenHash(token)).
		Scan(&d.ID, &uid, &d.Name, &d.CreatedAt, &d.LastSeenAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil, ErrNotFound
	} else if err != nil {
		return nil, nil, err
	}
	now := time.Now().UTC()
	if t, perr := time.Parse(tsLayout, d.LastSeenAt); perr != nil || now.Sub(t) > time.Minute {
		d.LastSeenAt = now.Format(tsLayout)
		_, _ = s.db.ExecContext(ctx, `UPDATE devices SET last_seen_at = ? WHERE id = ?`, d.LastSeenAt, d.ID)
	}
	u, err := s.UserByID(ctx, uid)
	return u, d, err
}
