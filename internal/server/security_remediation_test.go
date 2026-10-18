package server

import (
	"context"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"rocket-tracker/internal/store"
)

func TestSecurityCSVAllTextColumns(t *testing.T) {
	for _, prefix := range []string{"=", "+", "-", "@", "\t", "\r", "\n"} {
		t.Run(fmt.Sprintf("prefix-%x", prefix[0]), func(t *testing.T) {
			text := prefix + "synthetic"
			m := ingestionFixture()
			m.GUID = text
			m.Arena = text
			m.Mode = text
			m.Variant = text
			m.Tag = text
			m.Result = text
			m.FirstGoal = text
			m.Me.Name = text
			m.Me.PrimaryID = text
			m.MyTeam = 0
			m.GoalDiff = -1
			m.Players = []store.Player{{Name: text, Team: 0}, {Name: text, Team: 1}}
			m.Statfeed = map[string]int{text: 1}
			row := CSVRow(m)
			textColumns := map[string]bool{}
			for _, key := range []string{"guid", "arena", "mode", "variant", "tag", "result", "first_goal", "me_name", "me_primary_id", "teammates", "opponents", "statfeed"} {
				textColumns[key] = true
			}
			checked := 0
			for i, key := range CSVHeader {
				if textColumns[key] {
					checked++
					if !strings.HasPrefix(row[i], "'"+prefix) {
						t.Errorf("%s unsafe: %q", key, row[i])
					}
				}
				if key == "goal_diff" && row[i] != "-1" {
					t.Errorf("numeric goal_diff escaped: %q", row[i])
				}
			}
			if checked != len(textColumns) {
				t.Fatalf("checked %d/%d columns; headers %v", checked, len(textColumns), CSVHeader)
			}
		})
	}
}

func TestSecurityCSVImportOwnAndSocialExport(t *testing.T) {
	for _, prefix := range []string{"=", "+", "-", "@", "\t", "\r", "\n"} {
		t.Run(fmt.Sprintf("prefix-%x", prefix[0]), func(t *testing.T) {
			e := newHub(t, nil)
			alice := e.devLogin(t, "Alice")
			bob := e.devLogin(t, "Bob")
			m := ingestionFixture()
			m.Mode = prefix + "mode"
			m.Variant = prefix + "variant"
			m.FirstGoal = prefix + "first"
			raw, err := json.Marshal(map[string]any{"key": "security-csv", "match": m})
			if err != nil {
				t.Fatal(err)
			}
			body, ct := limitMultipart(t, limitUpload{"synthetic.json", raw})
			code, out := call(t, alice, "POST", e.ts.URL+"/api/import", string(body), "Content-Type", ct)
			if code != 200 {
				t.Fatalf("import %d %s", code, out)
			}
			for _, route := range []struct {
				c    *http.Client
				path string
			}{{alice, "/api/export.csv"}, {bob, "/api/players/alice/export.csv"}} {
				for _, sep := range []rune{';', ','} {
					code, out = call(t, route.c, "GET", e.ts.URL+route.path+"?sep="+string(sep), "")
					if code != 200 {
						t.Fatalf("export %d %s", code, out)
					}
					reader := csv.NewReader(strings.NewReader(strings.TrimPrefix(out, "\xef\xbb\xbf")))
					reader.Comma = sep
					rows, err := reader.ReadAll()
					if err != nil || len(rows) != 2 {
						t.Fatalf("CSV rows=%d err=%v", len(rows), err)
					}
					for i, key := range rows[0] {
						expected := map[string]string{"mode": "'" + m.Mode, "variant": "'" + m.Variant, "first_goal": "'" + m.FirstGoal, "goal_diff": "-1"}
						if want, ok := expected[key]; ok && rows[1][i] != want {
							t.Errorf("%s cell=%q want=%q", key, rows[1][i], want)
						}
					}
				}
			}
		})
	}
}

func TestSecuritySharedRevisionAcrossScopes(t *testing.T) {
	e := newHub(t, nil)
	alice := e.devLogin(t, "Alice")
	bob := e.devLogin(t, "Bob")
	ctx := context.Background()
	au, err := e.st.UserByHandle(ctx, "alice")
	if err != nil {
		t.Fatal(err)
	}
	bu, err := e.st.UserByHandle(ctx, "bob")
	if err != nil {
		t.Fatal(err)
	}
	a, b := e.st.User(au.ID), e.st.User(bu.ID)
	am := ingestionFixture()
	am.GUID = "shared-security"
	am.Me.PrimaryID = "alice-profile"
	if err := a.Save(ctx, am); err != nil {
		t.Fatal(err)
	}
	status := func(c *http.Client, path string) statusJSON {
		t.Helper()
		code, body := call(t, c, "GET", e.ts.URL+path, "")
		var st statusJSON
		if code != 200 || json.Unmarshal([]byte(body), &st) != nil || st.SharedRev == 0 {
			t.Fatalf("status %d %s", code, body)
		}
		return st
	}
	before := status(alice, "/api/status")
	check := func(wantShared int) {
		t.Helper()
		now := status(alice, "/api/status")
		social := status(bob, "/api/players/alice/status")
		if now.DataRev != before.DataRev || now.MatchCount != before.MatchCount {
			t.Fatal("B write changed A personal status")
		}
		if now.SharedRev <= before.SharedRev || now.SharedRev != a.SharedRev() || now.SharedRev != b.SharedRev() || social.SharedRev != now.SharedRev || social.DataRev != now.DataRev {
			t.Fatalf("global counter absent or scoped: before=%+v now=%+v social=%+v", before, now, social)
		}
		shared, err := a.Shared(ctx)
		if err != nil || len(shared) != wantShared {
			t.Fatalf("shared=%d err=%v", len(shared), err)
		}
		before = now
	}
	bm := ingestionFixture()
	bm.GUID = am.GUID
	bm.Me.PrimaryID = "bob-profile"
	if err := b.Save(ctx, bm); err != nil {
		t.Fatal(err)
	}
	check(1)
	if _, err := b.SetTag(ctx, bm.ID, "tournament"); err != nil {
		t.Fatal(err)
	}
	check(1)
	if err := b.Delete(ctx, bm.ID); err != nil {
		t.Fatal(err)
	}
	check(0)
	// Local status exposes exactly the same API, even for a write in a different scope.
	ts, st := newTestServer(t)
	sc := st.User(store.LocalUser)
	beforeData, beforeShared := sc.DataRev(), sc.SharedRev()
	m := ingestionFixture()
	if err := st.User(42).Save(ctx, m); err != nil {
		t.Fatal(err)
	}
	_, body := do(t, "GET", ts.URL+"/api/status", "")
	var local statusJSON
	if err := json.Unmarshal([]byte(body), &local); err != nil {
		t.Fatal(err)
	}
	if local.DataRev != beforeData || local.SharedRev <= beforeShared || local.SharedRev != sc.SharedRev() {
		t.Fatalf("local revision=%+v", local)
	}
}
