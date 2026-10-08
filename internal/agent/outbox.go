package agent

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"rocket-tracker/internal/store"
)

// Outbox is a directory of matches waiting to be uploaded, one JSON file per
// match (written atomically, so a crash never leaves half a file).
type Outbox struct {
	dir  string
	wake chan struct{}
}

type outboxEntry struct {
	Key   string       `json:"key"`
	Match *store.Match `json:"match"`
}

// OpenOutbox creates dir if needed.
func OpenOutbox(dir string) (*Outbox, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	return &Outbox{dir: dir, wake: make(chan struct{}, 1)}, nil
}

// Dir returns the outbox directory.
func (o *Outbox) Dir() string { return o.dir }

// Put queues a match and wakes the uploader.
func (o *Outbox) Put(key string, m *store.Match) error {
	b, err := json.Marshal(outboxEntry{Key: key, Match: m})
	if err != nil {
		return err
	}
	name := filepath.Join(o.dir, key+".json")
	tmp := name + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return err
	}
	if err := os.Rename(tmp, name); err != nil {
		os.Remove(tmp)
		return err
	}
	o.Wake()
	return nil
}

// Wake asks the uploader to run now.
func (o *Outbox) Wake() {
	select {
	case o.wake <- struct{}{}:
	default:
	}
}

// Woken is signalled by Wake.
func (o *Outbox) Woken() <-chan struct{} { return o.wake }

// Pending returns the queued file names, oldest first.
func (o *Outbox) Pending() ([]string, error) {
	es, err := os.ReadDir(o.dir)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, e := range es {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".json") {
			out = append(out, e.Name())
		}
	}
	sort.Strings(out)
	return out, nil
}

// Load reads a queued match.
func (o *Outbox) Load(name string) (string, *store.Match, error) {
	b, err := os.ReadFile(filepath.Join(o.dir, name))
	if err != nil {
		return "", nil, err
	}
	var e outboxEntry
	if err := json.Unmarshal(b, &e); err != nil {
		return "", nil, err
	}
	if e.Match == nil {
		return "", nil, errors.New("empty outbox entry")
	}
	if e.Key == "" {
		e.Key = strings.TrimSuffix(name, ".json")
	}
	return e.Key, e.Match, nil
}

// Remove drops an uploaded match.
func (o *Outbox) Remove(name string) error {
	err := os.Remove(filepath.Join(o.dir, name))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}

// Reject moves an entry the server refused to rejected/ (kept for diagnosis).
func (o *Outbox) Reject(name string) error {
	dst := filepath.Join(o.dir, "rejected")
	if err := os.MkdirAll(dst, 0o755); err != nil {
		return err
	}
	return os.Rename(filepath.Join(o.dir, name), filepath.Join(dst, name))
}
