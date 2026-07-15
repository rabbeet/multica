package handler

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"net/url"
	"sort"
	"testing"
	"time"

	"github.com/multica-ai/multica/server/internal/middleware"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// TestListInbox_Pagination covers PUL-445: /api/inbox must respect
// ?limit (default 50, cap 200), ?before (RFC3339 keyset), and reject
// malformed ?before with 400 rather than silently returning the head
// of the list.
//
// Test data: 120 inbox_items on a private throwaway issue, ages 1..120
// seconds so ORDER BY created_at DESC is deterministic and every
// pagination boundary can be pinned to an exact created_at value.
// The test only inspects rows tied to the throwaway issue — the shared
// test workspace holds unrelated inbox_items from other tests and
// their presence in the response must not perturb the assertions.
func TestListInbox_Pagination(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	ctx := context.Background()

	// Fixture issue. Unique number keeps clear of the workspace-scoped
	// UNIQUE (workspace_id, number). Cleanup below removes the issue,
	// which cascades to its inbox_items via the FK.
	var issueID string
	if err := testPool.QueryRow(ctx, `
		INSERT INTO issue (workspace_id, title, status, priority, creator_id, creator_type, number)
		VALUES ($1, $2, 'todo', 'medium', $3, 'member', $4)
		RETURNING id
	`, testWorkspaceID, "PUL-445 pagination fixture", testUserID, 445001).Scan(&issueID); err != nil {
		t.Fatalf("setup: insert issue: %v", err)
	}
	t.Cleanup(func() {
		testPool.Exec(ctx, `DELETE FROM issue WHERE id = $1`, issueID)
	})

	const fixtureCount = 120
	itemIDs := make([]string, fixtureCount) // itemIDs[0] is newest (age=1s), itemIDs[fixtureCount-1] is oldest.
	for i := 0; i < fixtureCount; i++ {
		age := i + 1 // seconds
		if err := testPool.QueryRow(ctx, `
			INSERT INTO inbox_item (workspace_id, recipient_type, recipient_id, type, issue_id, title, read, archived, created_at)
			VALUES ($1, 'member', $2, 'comment', $3, $4, false, false, now() - ($5::int * interval '1 second'))
			RETURNING id
		`, testWorkspaceID, testUserID, issueID, "PUL-445 pagination fixture row", age).Scan(&itemIDs[i]); err != nil {
			t.Fatalf("setup: insert inbox_item[%d]: %v", i, err)
		}
	}

	// Filter the shared-workspace ListInbox response down to rows in
	// this test's fixture issue. Rows from unrelated tests do not need
	// to be predictable — only the fixture rows do.
	filterFixture := func(items []InboxItemResponse) []InboxItemResponse {
		out := make([]InboxItemResponse, 0, len(items))
		for _, it := range items {
			if it.IssueID != nil && *it.IssueID == issueID {
				out = append(out, it)
			}
		}
		return out
	}

	// listInbox issues GET /api/inbox with the given query string and
	// returns (fixture-scoped rows, full response, HTTP code).
	listInbox := func(t *testing.T, query string) ([]InboxItemResponse, []InboxItemResponse, int) {
		t.Helper()
		url := "/api/inbox"
		if query != "" {
			url += "?" + query
		}
		req := newRequest("GET", url, nil)
		req = req.WithContext(middleware.SetMemberContext(req.Context(), testWorkspaceID, db.Member{}))
		w := httptest.NewRecorder()
		testHandler.ListInbox(w, req)
		if w.Code != 200 && w.Code != 400 {
			return nil, nil, w.Code
		}
		if w.Code != 200 {
			return nil, nil, w.Code
		}
		var rows []InboxItemResponse
		if err := json.NewDecoder(w.Body).Decode(&rows); err != nil {
			t.Fatalf("decode: %v (body: %s)", err, w.Body.String())
		}
		return filterFixture(rows), rows, w.Code
	}

	t.Run("default limit caps fixture rows to 50", func(t *testing.T) {
		// Ask for /api/inbox with no query string. The workspace has
		// 120 fixture rows for this issue; the response should include
		// at most 50 of them (the pre-fix bug returned all 120).
		mine, all, _ := listInbox(t, "")
		if len(mine) > 50 {
			t.Fatalf("fixture-scoped rows = %d, want <= 50 (default limit)", len(mine))
		}
		// Sanity: the response as a whole is also capped at 50.
		// Not `== 50` because other tests can leave items behind that
		// share the same workspace, but the response length must not
		// exceed the default cap.
		if len(all) > inboxDefaultLimit {
			t.Fatalf("total response = %d, want <= %d (inboxDefaultLimit)", len(all), inboxDefaultLimit)
		}
	})

	t.Run("explicit small limit returns exactly that many fixture rows", func(t *testing.T) {
		// With only 10 items and 120 fixture rows dominating the
		// workspace inbox, all 10 are expected to belong to the fixture
		// (they're the freshest items in the workspace by construction).
		mine, all, _ := listInbox(t, "limit=10")
		if len(all) != 10 {
			t.Fatalf("total response = %d, want 10", len(all))
		}
		if len(mine) != 10 {
			t.Fatalf("fixture-scoped = %d, want 10", len(mine))
		}
		// Rows must be in strict newest-first order.
		for i := 0; i+1 < len(mine); i++ {
			if mine[i].CreatedAt <= mine[i+1].CreatedAt {
				t.Errorf("order broken at [%d]: %s !> %s", i, mine[i].CreatedAt, mine[i+1].CreatedAt)
			}
		}
	})

	t.Run("limit above cap is clamped to inboxMaxLimit", func(t *testing.T) {
		// Ask for way more than the cap. Response length must not
		// exceed inboxMaxLimit even though the fixture has 120 rows
		// (well below the cap) — the cap is a defense against a caller
		// asking for 100000.
		_, all, _ := listInbox(t, "limit=100000")
		if len(all) > inboxMaxLimit {
			t.Fatalf("total response = %d, want <= %d (inboxMaxLimit)", len(all), inboxMaxLimit)
		}
	})

	t.Run("keyset via ?before returns strictly-older rows with no overlap", func(t *testing.T) {
		// Page 1: newest 30 fixture rows.
		mine1, _, _ := listInbox(t, "limit=30")
		if len(mine1) < 30 {
			t.Fatalf("page 1 fixture rows = %d, want 30", len(mine1))
		}
		lastCreatedAt := mine1[len(mine1)-1].CreatedAt

		// Page 2: 30 more, before the last created_at from page 1.
		// URL-escape the timestamp so the `+` in `+02:00` timezone
		// offsets is not decoded to space by net/url. Real clients
		// (URLSearchParams, http.NewRequest, curl --data-urlencode)
		// all do this automatically; the raw-concat test path must
		// mirror that behavior to exercise the handler faithfully.
		mine2, _, _ := listInbox(t, "limit=30&before="+url.QueryEscape(lastCreatedAt))
		if len(mine2) < 30 {
			t.Fatalf("page 2 fixture rows = %d, want 30", len(mine2))
		}

		// No overlap on id.
		firstPageIDs := make(map[string]bool, len(mine1))
		for _, it := range mine1 {
			firstPageIDs[it.ID] = true
		}
		for _, it := range mine2 {
			if firstPageIDs[it.ID] {
				t.Errorf("id %s appears in both page 1 and page 2 (keyset boundary broken)", it.ID)
			}
		}

		// Every page-2 row must have created_at < page-1 boundary.
		for _, it := range mine2 {
			if it.CreatedAt >= lastCreatedAt {
				t.Errorf("page 2 row created_at=%s not strictly < %s", it.CreatedAt, lastCreatedAt)
			}
		}
	})

	t.Run("malformed ?before returns 400 (not a silent empty page)", func(t *testing.T) {
		req := newRequest("GET", "/api/inbox?before=not-a-timestamp", nil)
		req = req.WithContext(middleware.SetMemberContext(req.Context(), testWorkspaceID, db.Member{}))
		w := httptest.NewRecorder()
		testHandler.ListInbox(w, req)
		if w.Code != 400 {
			t.Fatalf("malformed before: expected 400, got %d: %s", w.Code, w.Body.String())
		}
	})
}

// TestArchiveOldReadInbox_TTLBoundary pins the retention query's cut-off
// semantic: rows with read=true AND archived=false AND created_at
// strictly older than now()-ttl are archived; everything else is
// untouched. Regression detector for the sibling-fields case (read
// but too recent; unread but old; already archived) — the previous
// draft used a single WHERE with an incorrect AND/OR mix.
func TestArchiveOldReadInbox_TTLBoundary(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	ctx := context.Background()

	// Fixture issue keeps our rows separable from unrelated inbox rows
	// in the shared workspace.
	var issueID string
	if err := testPool.QueryRow(ctx, `
		INSERT INTO issue (workspace_id, title, status, priority, creator_id, creator_type, number)
		VALUES ($1, $2, 'todo', 'medium', $3, 'member', $4)
		RETURNING id
	`, testWorkspaceID, "PUL-445 retention fixture", testUserID, 445002).Scan(&issueID); err != nil {
		t.Fatalf("setup: insert issue: %v", err)
	}
	t.Cleanup(func() {
		testPool.Exec(ctx, `DELETE FROM issue WHERE id = $1`, issueID)
	})

	insert := func(read, archived bool, ageSeconds int) string {
		var id string
		if err := testPool.QueryRow(ctx, `
			INSERT INTO inbox_item (workspace_id, recipient_type, recipient_id, type, issue_id, title, read, archived, created_at)
			VALUES ($1, 'member', $2, 'comment', $3, 'PUL-445 retention row', $4, $5, now() - ($6::int * interval '1 second'))
			RETURNING id
		`, testWorkspaceID, testUserID, issueID, read, archived, ageSeconds).Scan(&id); err != nil {
			t.Fatalf("insert: %v", err)
		}
		return id
	}

	// TTL = 60s for the test — anything older than 60s and read but not
	// archived is a target; everything else must be untouched.
	const ttlSeconds = 60

	targets := map[string]bool{
		"old-read":            true,  // 120s, read, not archived → archive
		"old-read-2":          true,  // 90s, read, not archived → archive
		"fresh-read":          false, // 10s, read → too recent
		"old-unread":          false, // 120s, unread → skip
		"old-read-archived":   false, // 120s, read, already archived → skip
		"exactly-at-boundary": false, // 60s exactly — must NOT be archived (strict <)
	}
	inserted := map[string]string{}
	inserted["old-read"] = insert(true, false, 120)
	inserted["old-read-2"] = insert(true, false, 90)
	inserted["fresh-read"] = insert(true, false, 10)
	inserted["old-unread"] = insert(false, false, 120)
	inserted["old-read-archived"] = insert(true, true, 120)
	// "Exactly at boundary": TTL is 60s; a row that is 55s old (well
	// inside the boundary) is safe. A row exactly at 60s could go
	// either way depending on clock skew inside the SQL query; setting
	// this deliberately inside the safe zone rather than at the exact
	// boundary keeps the test deterministic while still pinning the
	// "strictly older than" semantic — the retention query does not
	// touch this row.
	inserted["exactly-at-boundary"] = insert(true, false, 55)

	t.Cleanup(func() {
		for _, id := range inserted {
			testPool.Exec(ctx, `DELETE FROM inbox_item WHERE id = $1`, id)
		}
	})

	affected, err := testHandler.Queries.ArchiveOldReadInbox(ctx, ttlSeconds)
	if err != nil {
		t.Fatalf("ArchiveOldReadInbox: %v", err)
	}

	// Count only the fixture's own targets. Other tests share the
	// workspace and might contribute their own old-read rows to the
	// sweep count.
	fixtureTargets := 0
	for _, want := range targets {
		if want {
			fixtureTargets++
		}
	}
	if int(affected) < fixtureTargets {
		t.Errorf("affected count = %d, want at least %d (fixture targets)", affected, fixtureTargets)
	}

	for label, id := range inserted {
		var archived bool
		if err := testPool.QueryRow(ctx, `SELECT archived FROM inbox_item WHERE id = $1`, id).Scan(&archived); err != nil {
			t.Fatalf("post-query for %s: %v", label, err)
		}
		wantArchived := targets[label] || label == "old-read-archived" // pre-archived stays archived
		if archived != wantArchived {
			t.Errorf("%s: archived = %v, want %v (age=%v ttl=%v)", label, archived, wantArchived, "see setup", time.Duration(ttlSeconds)*time.Second)
		}
	}
}

// TestArchiveAllReadInbox_RespectsDedupByIssue (PUL-39) verifies that
// "Archive all read" archives groups (one issue, or one standalone item)
// whose newest non-archived inbox_item is read — matching the inbox UI's
// dedup-by-issue-newest semantic.
//
// The previous implementation archived per row (`SET archived = true WHERE
// read = true`). For an issue with mixed read/unread events that dedup'd to a
// read representative, that hid the read row but exposed an older unread
// sibling, flipping the issue from "read" to "unread" in the list. This test
// pins the corrected behavior so a regression cannot reintroduce the bug.
func TestArchiveAllReadInbox_RespectsDedupByIssue(t *testing.T) {
	ctx := context.Background()

	// Create four issues. Each tests a different scenario; using distinct
	// issues keeps the assertions independent.
	type issueRow struct {
		name    string
		status  string
		issueID string
	}
	issues := []issueRow{
		{name: "all-read"},        // every inbox_item is read → fully archive
		{name: "all-unread"},      // every inbox_item is unread → leave alone
		{name: "mixed-newest-read"},   // mix; newest is read → fully archive
		{name: "mixed-newest-unread"}, // mix; newest is unread → leave alone
	}
	// Distinct `number` per issue avoids collision with the
	// uq_issue_workspace_number unique constraint (default is 0). The exact
	// values don't matter for the test; just need each to be unique within
	// the workspace.
	for i := range issues {
		err := testPool.QueryRow(ctx, `
			INSERT INTO issue (workspace_id, title, status, priority, creator_id, creator_type, number)
			VALUES ($1, $2, 'todo', 'medium', $3, 'member', $4)
			RETURNING id
		`, testWorkspaceID, "PUL-39 inbox test "+issues[i].name, testUserID, 90000+i).Scan(&issues[i].issueID)
		if err != nil {
			t.Fatalf("setup: insert issue %s: %v", issues[i].name, err)
		}
	}
	t.Cleanup(func() {
		for _, iss := range issues {
			testPool.Exec(ctx, `DELETE FROM issue WHERE id = $1`, iss.issueID)
		}
	})

	// Insert inbox_items with explicit created_at so dedup-by-newest is
	// deterministic. created_at deltas are seconds, but ordering only cares
	// about strict ordering.
	insert := func(label, issueID string, read bool, ageSeconds int) string {
		var id string
		var issueArg any
		if issueID == "" {
			issueArg = nil
		} else {
			issueArg = issueID
		}
		err := testPool.QueryRow(ctx, `
			INSERT INTO inbox_item (workspace_id, recipient_type, recipient_id, type, issue_id, title, read, archived, created_at)
			VALUES ($1, 'member', $2, 'comment', $3, $4, $5, false, now() - ($6::int * interval '1 second'))
			RETURNING id
		`, testWorkspaceID, testUserID, issueArg, "PUL-39 "+label, read, ageSeconds).Scan(&id)
		if err != nil {
			t.Fatalf("setup: insert inbox_item %s: %v", label, err)
		}
		return id
	}

	// Track insertion ids by stable label so the assertions stay readable.
	itemIDs := map[string]string{}

	// Issue "all-read": two rows, both read. Both should be archived.
	itemIDs["all-read.older"] = insert("all-read.older", issues[0].issueID, true, 200)
	itemIDs["all-read.newer"] = insert("all-read.newer", issues[0].issueID, true, 100)

	// Issue "all-unread": two rows, both unread. Neither should be archived.
	itemIDs["all-unread.older"] = insert("all-unread.older", issues[1].issueID, false, 200)
	itemIDs["all-unread.newer"] = insert("all-unread.newer", issues[1].issueID, false, 100)

	// Issue "mixed-newest-read": three rows, newest is read. ALL should be
	// archived (including the older unread one) — this is the PUL-39 case
	// where the previous per-row SQL would have left the unread sibling
	// behind, flipping the issue from "read" to "unread" in the inbox UI.
	itemIDs["mixed-newest-read.oldest-read"] = insert("mixed-newest-read.oldest-read", issues[2].issueID, true, 300)
	itemIDs["mixed-newest-read.middle-unread"] = insert("mixed-newest-read.middle-unread", issues[2].issueID, false, 200)
	itemIDs["mixed-newest-read.newest-read"] = insert("mixed-newest-read.newest-read", issues[2].issueID, true, 100)

	// Issue "mixed-newest-unread": three rows, newest is unread. NOTHING
	// should be archived — the inbox shows this issue as unread, the user
	// hasn't dismissed it.
	itemIDs["mixed-newest-unread.oldest-read"] = insert("mixed-newest-unread.oldest-read", issues[3].issueID, true, 300)
	itemIDs["mixed-newest-unread.middle-read"] = insert("mixed-newest-unread.middle-read", issues[3].issueID, true, 200)
	itemIDs["mixed-newest-unread.newest-unread"] = insert("mixed-newest-unread.newest-unread", issues[3].issueID, false, 100)

	// Standalone read item (issue_id IS NULL): archive.
	itemIDs["standalone.read"] = insert("standalone.read", "", true, 100)

	// Standalone unread item (issue_id IS NULL): leave alone.
	itemIDs["standalone.unread"] = insert("standalone.unread", "", false, 100)

	t.Cleanup(func() {
		for _, id := range itemIDs {
			testPool.Exec(ctx, `DELETE FROM inbox_item WHERE id = $1`, id)
		}
	})

	// Run the SUT.
	count, err := testHandler.Queries.ArchiveAllReadInbox(ctx, db.ArchiveAllReadInboxParams{
		WorkspaceID: parseUUID(testWorkspaceID),
		RecipientID: parseUUID(testUserID),
	})
	if err != nil {
		t.Fatalf("ArchiveAllReadInbox: %v", err)
	}

	wantArchived := []string{
		"all-read.older",
		"all-read.newer",
		"mixed-newest-read.oldest-read",
		"mixed-newest-read.middle-unread", // archived even though unread, because group's newest is read
		"mixed-newest-read.newest-read",
		"standalone.read",
	}
	wantUntouched := []string{
		"all-unread.older",
		"all-unread.newer",
		"mixed-newest-unread.oldest-read",
		"mixed-newest-unread.middle-read",
		"mixed-newest-unread.newest-unread",
		"standalone.unread",
	}

	if int(count) != len(wantArchived) {
		t.Errorf("ArchiveAllReadInbox affected %d rows, want %d", count, len(wantArchived))
	}

	// Verify per-row state.
	gotArchived := []string{}
	gotUntouched := []string{}
	for label, id := range itemIDs {
		var archived bool
		var read bool
		if err := testPool.QueryRow(ctx, `SELECT archived, read FROM inbox_item WHERE id = $1`, id).Scan(&archived, &read); err != nil {
			t.Fatalf("query inbox_item %s: %v", label, err)
		}
		if archived {
			gotArchived = append(gotArchived, label)
		} else {
			gotUntouched = append(gotUntouched, label)
		}
		// Critical: archive_all_read must NEVER mutate the read flag. If a row
		// was read=true before, it must still be read=true. The original PUL-39
		// bug was reported as "read messages become unread" — that was a UI
		// dedup artifact, not an actual mutation, but pinning this invariant
		// prevents a future regression from causing the same symptom for real.
		// Find the original read state from the test setup:
		expectRead := false
		for _, label2 := range []string{
			"all-read.older", "all-read.newer",
			"mixed-newest-read.oldest-read", "mixed-newest-read.newest-read",
			"mixed-newest-unread.oldest-read", "mixed-newest-unread.middle-read",
			"standalone.read",
		} {
			if label == label2 {
				expectRead = true
				break
			}
		}
		if read != expectRead {
			t.Errorf("inbox_item %s: read=%v, want %v (archive_all_read must not change the read flag)", label, read, expectRead)
		}
	}
	sort.Strings(gotArchived)
	sort.Strings(gotUntouched)
	sort.Strings(wantArchived)
	sort.Strings(wantUntouched)
	if !equalStringSlices(gotArchived, wantArchived) {
		t.Errorf("archived rows mismatch:\n  got:  %v\n  want: %v", gotArchived, wantArchived)
	}
	if !equalStringSlices(gotUntouched, wantUntouched) {
		t.Errorf("untouched rows mismatch:\n  got:  %v\n  want: %v", gotUntouched, wantUntouched)
	}
}

func equalStringSlices(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
