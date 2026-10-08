package store

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
)

func TestManualDays(t *testing.T) {
	ctx := context.Background()
	st, err := Open(filepath.Join(t.TempDir(), "db.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	for _, bad := range []ManualDay{
		{Day: "08/10/2026", Mode: "1v1", Games: 3},
		{Day: "2026-10-08", Mode: "5v5", Games: 3},
		{Day: "2026-10-08", Mode: "1v1", Games: -1},
		{Day: "2026-10-08", Mode: "1v1", Games: MaxManualGames + 1},
		{Day: "2026-10-08", Mode: "1v1", Games: 3, Wins: 4},
	} {
		if err := st.SetManual(ctx, bad); !errors.Is(err, ErrInvalidManual) {
			t.Fatalf("%+v: want ErrInvalidManual, got %v", bad, err)
		}
	}

	must := func(d ManualDay) {
		t.Helper()
		if err := st.SetManual(ctx, d); err != nil {
			t.Fatal(err)
		}
	}
	must(ManualDay{Day: "2026-10-08", Mode: "1v1", Games: 8, Wins: 5})
	must(ManualDay{Day: "2026-10-07", Mode: "2v2", Games: 2, Wins: 0})
	must(ManualDay{Day: "2026-10-08", Mode: "1v1", Games: 10, Wins: 6}) // upsert
	got, err := st.ListManual(ctx)
	if err != nil {
		t.Fatal(err)
	}
	want := []ManualDay{{"2026-10-07", "2v2", 2, 0}, {"2026-10-08", "1v1", 10, 6}}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("list = %+v, want %+v", got, want)
	}

	must(ManualDay{Day: "2026-10-07", Mode: "2v2", Games: 0}) // delete
	if got, _ = st.ListManual(ctx); len(got) != 1 || got[0].Mode != "1v1" {
		t.Fatalf("after delete = %+v", got)
	}
}
