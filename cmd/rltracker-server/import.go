package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"rocket-tracker/internal/server"
	"rocket-tracker/internal/store"
)

// cmdImport: `rltracker-server import --user HANDLE PATH...` stores matches
// copied from a gaming PC, without the network (the PC and the server need
// not run at the same time). A PATH is the PC's local database
// (rltracker.db), the agent's queue folder (outbox) or .json files from it.
func cmdImport(args []string, out io.Writer) error {
	fs := flag.NewFlagSet("rltracker-server import", flag.ContinueOnError)
	dataDir := fs.String("data-dir", env("RT_DATA_DIR", "data"), "data directory")
	handle := fs.String("user", "", "player receiving the matches (handle shown on the Players page)")
	fs.Usage = func() {
		fmt.Fprintln(fs.Output(), "usage: rltracker-server import --user HANDLE PATH...")
		fmt.Fprintln(fs.Output(), "  PATH: rltracker.db, the agent's outbox folder, or .json files from it")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *handle == "" || fs.NArg() == 0 {
		fs.Usage()
		return errors.New("--user and at least one PATH are required")
	}
	dbPath := filepath.Join(*dataDir, "rltracker-server.db")
	if _, err := os.Stat(dbPath); err != nil {
		return fmt.Errorf("server database %s not found (wrong --data-dir / RT_DATA_DIR?)", dbPath)
	}
	st, err := store.Open(dbPath)
	if err != nil {
		return err
	}
	defer st.Close()
	ctx := context.Background()
	u, err := st.UserByHandle(ctx, *handle)
	if errors.Is(err, store.ErrNotFound) {
		var hs []string
		if us, err := st.Users(ctx); err == nil {
			for _, u := range us {
				hs = append(hs, u.Handle)
			}
		}
		return fmt.Errorf("no player %q (they must have signed in once); players: %s", *handle, strings.Join(hs, ", "))
	} else if err != nil {
		return err
	}
	c, err := server.UserSettings(ctx, st, u.ID)
	if err != nil {
		return err
	}
	im := &server.Importer{Scope: st.User(u.ID), DefaultTag: c.DefaultTag}

	var total server.ImportResult
	for _, p := range fs.Args() {
		res, err := im.Path(ctx, p)
		total.Matches += res.Matches
		total.Manual += res.Manual
		total.Skipped = append(total.Skipped, res.Skipped...)
		if err != nil {
			return fmt.Errorf("%s: %w", p, err)
		}
	}
	for _, s := range total.Skipped {
		fmt.Fprintln(out, "skipped", s)
	}
	fmt.Fprintf(out, "%d matches and %d manual entries imported for %s\n", total.Matches, total.Manual, u.Handle)
	return nil
}
