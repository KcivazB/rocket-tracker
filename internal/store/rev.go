package store

import (
	"sync"
	"time"
)

// revisions count the writes to each user's data (matches, trainings, tags,
// ranks, hand-entered games...), so the dashboard can see that something
// changed in place (a result, a tag, a merge, a deletion) and not only when
// the counts change. Kept in memory: they start from the time the store was
// opened, so a restart is seen as a change too.
type revisions struct {
	mu   sync.Mutex
	base int64
	all  int64
	n    map[int64]int64
}

func newRevisions() *revisions {
	return &revisions{base: time.Now().UnixMilli(), n: map[int64]int64{}}
}

// touch records a write to the data of user uid.
func (s *Store) touch(uid int64) {
	s.rev.mu.Lock()
	s.rev.n[uid]++
	s.rev.all++
	s.rev.mu.Unlock()
}

// DataRev changes after each write to the user's data (see revisions).
func (sc *Scope) DataRev() int64 {
	sc.s.rev.mu.Lock()
	defer sc.s.rev.mu.Unlock()
	return sc.s.rev.base + sc.s.rev.n[sc.uid]
}

// SharedRev invalidates relationships whose other account may have changed.
// It deliberately over-invalidates on any write rather than miss a join update.
func (sc *Scope) SharedRev() int64 {
	sc.s.rev.mu.Lock()
	defer sc.s.rev.mu.Unlock()
	return sc.s.rev.base + sc.s.rev.all
}
