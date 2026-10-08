package agent

import (
	"context"
	"fmt"
	"strconv"

	"rocket-tracker/internal/store"
)

// ImportKey identifies a match of a local database on the server, so that
// running the import again updates instead of duplicating.
func ImportKey(m *store.Match) string {
	return "import:" + m.GUID + ":" + strconv.FormatInt(m.StartedAt.Unix(), 10)
}

// Import uploads the matches and hand-entered games of a local database
// (the one `rltracker run` fills) to the server.
func Import(ctx context.Context, c *Client, st *store.Store, progress func(done, total int)) (matches, manual int, err error) {
	ms, err := st.List(ctx)
	if err != nil {
		return 0, 0, err
	}
	for i, m := range ms {
		if err := c.SendMatch(ctx, ImportKey(m), m); err != nil {
			return matches, 0, fmt.Errorf("match %d (%s): %w", m.ID, m.StartedAt.Format("2006-01-02 15:04"), err)
		}
		matches++
		if progress != nil {
			progress(i+1, len(ms))
		}
	}
	ds, err := st.ListManual(ctx)
	if err != nil {
		return matches, 0, err
	}
	for _, d := range ds {
		if err := c.SetManual(ctx, d); err != nil {
			return matches, manual, fmt.Errorf("manual games %s %s: %w", d.Day, d.Mode, err)
		}
		manual++
	}
	return matches, manual, nil
}
