package eventstore

import (
	"context"
	"database/sql"
	"path/filepath"
	"strings"
	"testing"
)

func TestListMatchingUsesTypeAndSessionIndexes(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t, filepath.Join(t.TempDir(), "events.db"))
	add := func(eventType, native, launch string) {
		t.Helper()
		request := testRequest("p", `{}`)
		request.Type, request.Actor.NativeSessionID, request.Actor.ACRSessionID = eventType, native, launch
		if _, err := store.Append(ctx, request); err != nil {
			t.Fatal(err)
		}
	}
	add("intent.observed", "s1", "l1")
	add("action.completed", "s1", "l1")
	add("contract.activated", "", "")
	add("intent.observed", "s2", "")
	add("intent.observed", "s1", "l1")

	types := func(events []Event) string {
		values := []string{}
		for _, event := range events {
			values = append(values, event.Type+"@"+event.Actor.NativeSessionID)
		}
		return strings.Join(values, ",")
	}
	for _, item := range []struct {
		filter Filter
		after  uint64
		want   string
	}{
		{Filter{Types: []string{"contract.activated", "action.completed"}}, 0, "action.completed@s1,contract.activated@"},
		{Filter{NativeSessionID: "s1"}, 0, "intent.observed@s1,action.completed@s1,intent.observed@s1"},
		{Filter{Types: []string{"intent.observed"}, NativeSessionID: "s1"}, 1, "intent.observed@s1"},
		{Filter{ACRSessionID: "l1", Types: []string{"intent.observed"}}, 0, "intent.observed@s1,intent.observed@s1"},
		{Filter{}, 3, "intent.observed@s2,intent.observed@s1"},
	} {
		events, err := store.ListMatching(ctx, "p", item.after, 10, item.filter)
		if err != nil || types(events) != item.want {
			t.Errorf("ListMatching(%+v, after %d) = %s, %v; want %s", item.filter, item.after, types(events), err, item.want)
		}
	}

	for index, query := range []string{
		`SELECT sequence FROM events WHERE project_id = 'p' AND sequence > 0 AND json_extract(actor, '$.native_session_id') = 's1' ORDER BY sequence`,
		`SELECT sequence FROM events WHERE project_id = 'p' AND sequence > 0 AND type IN ('intent.observed') ORDER BY sequence`,
	} {
		plan := queryPlan(t, store.db, query)
		if !strings.Contains(plan, []string{"events_by_native_session", "events_by_type"}[index]) {
			t.Errorf("plan for %q = %s", query, plan)
		}
	}
}

func queryPlan(t *testing.T, db *sql.DB, query string) string {
	t.Helper()
	rows, err := db.Query("EXPLAIN QUERY PLAN " + query)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	plan := []string{}
	for rows.Next() {
		var id, parent, unused int
		var detail string
		if err := rows.Scan(&id, &parent, &unused, &detail); err != nil {
			t.Fatal(err)
		}
		plan = append(plan, detail)
	}
	return strings.Join(plan, "; ")
}

// A version 1 database gains the indexes on open and keeps its chain.
func TestOpenMigratesVersionOneDatabase(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "events.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(schemaSQL); err != nil {
		t.Fatal(err)
	}
	db.Close()
	store := openTestStore(t, path)
	if _, err := store.Append(ctx, testRequest("p", `{"a":1}`)); err != nil {
		t.Fatal(err)
	}
	var version int
	if err := store.db.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil || version != storeSchemaVersion {
		t.Fatalf("user_version = %d, %v", version, err)
	}
	if plan := queryPlan(t, store.db, `SELECT sequence FROM events WHERE project_id = 'p' AND sequence > 0 AND type IN ('x')`); !strings.Contains(plan, "events_by_type") {
		t.Fatalf("migrated database plan = %s", plan)
	}
	if err := store.Verify(ctx, "p"); err != nil {
		t.Fatal(err)
	}
}

func TestAppendBatchIsAtomicAndChained(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t, filepath.Join(t.TempDir(), "events.db"))
	if _, err := store.Append(ctx, testRequest("p", `{"n":0}`)); err != nil {
		t.Fatal(err)
	}
	broken := testRequest("p", `{"n":2}`)
	broken.Type = ""
	if _, err := store.AppendBatch(ctx, []AppendRequest{testRequest("p", `{"n":1}`), broken}); err == nil {
		t.Fatal("a batch with an invalid event was accepted")
	}
	if head, err := store.Head(ctx, "p"); err != nil || head.Sequence != 1 {
		t.Fatalf("head after rejected batch = %+v, %v", head, err)
	}
	events, err := store.AppendBatch(ctx, []AppendRequest{testRequest("p", `{"n":1}`), testRequest("q", `{"n":1}`), testRequest("p", `{"n":2}`)})
	if err != nil || len(events) != 3 || events[0].Sequence != 2 || events[1].Sequence != 1 || events[2].Sequence != 3 || events[2].PreviousHash != events[0].EventHash {
		t.Fatalf("batch = %+v, %v", events, err)
	}
	for _, project := range []string{"p", "q"} {
		if err := store.Verify(ctx, project); err != nil {
			t.Fatal(err)
		}
	}
}

// ListMatching reads through the matching index even when the primary key
// would also satisfy its order and limit, which SQLite otherwise prefers.
func TestListMatchingNamesItsIndex(t *testing.T) {
	store := openTestStore(t, filepath.Join(t.TempDir(), "events.db"))
	for filter, index := range map[*Filter]string{
		{Types: []string{"contract.activated", "contract.retired"}}: "events_by_type",
		{NativeSessionID: "s", Types: []string{"intent.observed"}}:  "events_by_native_session",
		{ACRSessionID: "l"}: "events_by_acr_session",
	} {
		query, args := matchingQuery("p", 0, *filter, "ASC")
		rows, err := store.db.Query("EXPLAIN QUERY PLAN "+query, append(args, 500)...)
		if err != nil {
			t.Fatal(err)
		}
		plan := []string{}
		for rows.Next() {
			var id, parent, unused int
			var detail string
			rows.Scan(&id, &parent, &unused, &detail)
			plan = append(plan, detail)
		}
		rows.Close()
		if joined := strings.Join(plan, "; "); !strings.Contains(joined, index) {
			t.Errorf("filter %+v plan = %s, want %s", *filter, joined, index)
		}
	}
}

func TestLatestReturnsTheNewestMatchingEventsInOrder(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t, filepath.Join(t.TempDir(), "events.db"))
	requests := []AppendRequest{}
	for i := 0; i < 10; i++ {
		request := testRequest("p", `{}`)
		request.Type = []string{"a", "b"}[i%2]
		requests = append(requests, request)
	}
	if _, err := store.AppendBatch(ctx, requests); err != nil {
		t.Fatal(err)
	}
	events, err := store.Latest(ctx, "p", 3, Filter{Types: []string{"b"}})
	if err != nil || len(events) != 3 || events[0].Sequence != 6 || events[2].Sequence != 10 {
		t.Fatalf("latest = %+v, %v", events, err)
	}
	if all, err := store.Latest(ctx, "p", 100, Filter{}); err != nil || len(all) != 10 || all[0].Sequence != 1 {
		t.Fatalf("latest without filter = %d, %v", len(all), err)
	}
}
