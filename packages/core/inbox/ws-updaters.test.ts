import { describe, it, expect } from "vitest";
import { QueryClient } from "@tanstack/react-query";
import { onInboxIssueDeleted, onInboxIssueStatusChanged } from "./ws-updaters";
import { inboxKeys, type InboxCacheData, flattenInboxPages } from "./queries";
import type { InboxItem, InboxListPage } from "../types";

const wsId = "ws-1";

function makeItem(
  id: string,
  issueId: string | null,
  overrides: Partial<InboxItem> = {},
): InboxItem {
  return {
    id,
    workspace_id: wsId,
    recipient_type: "member",
    recipient_id: "user-1",
    actor_type: null,
    actor_id: null,
    type: "mentioned",
    severity: "info",
    issue_id: issueId,
    title: `item ${id}`,
    body: null,
    issue_status: null,
    read: false,
    archived: false,
    created_at: "2025-01-01T00:00:00Z",
    details: null,
    // PUL-177: phase is always present on the wire (server-derived
    // from issue.status with a backlog default), and latest_skill
    // defaults to null for tickets without skill_state history.
    // PUL-180: ownership + ownership_meta are paired; both null
    // means "chip hidden" (closed phase or agent recipient).
    phase: "backlog",
    latest_skill: null,
    ownership: null,
    ownership_meta: null,
    ...overrides,
  };
}

// PUL-481: /api/inbox is paginated. Ws-updaters walk `data.pages` rather than
// a flat array — this helper builds the two-page shape the cache stores.
function seedCache(qc: QueryClient, pages: InboxItem[][]) {
  const cache: InboxCacheData = {
    pages: pages.map<InboxListPage>((items, idx) => ({
      items,
      next_cursor: idx < pages.length - 1 ? `c${idx}` : null,
      has_more: idx < pages.length - 1,
    })),
    pageParams: pages.map((_, idx) => (idx === 0 ? undefined : `c${idx - 1}`)),
  };
  qc.setQueryData<InboxCacheData>(inboxKeys.list(wsId), cache);
}

describe("onInboxIssueDeleted", () => {
  it("removes all inbox items referencing the deleted issue across pages", () => {
    const qc = new QueryClient();
    seedCache(qc, [
      [makeItem("i1", "issue-a"), makeItem("i2", "issue-a")],
      [makeItem("i3", "issue-b"), makeItem("i4", null)],
    ]);

    onInboxIssueDeleted(qc, wsId, "issue-a");

    const after = qc.getQueryData<InboxCacheData>(inboxKeys.list(wsId));
    expect(flattenInboxPages(after).map((i) => i.id)).toEqual(["i3", "i4"]);
  });

  it("is a no-op when the inbox cache is empty", () => {
    const qc = new QueryClient();
    expect(() => onInboxIssueDeleted(qc, wsId, "issue-a")).not.toThrow();
    expect(
      qc.getQueryData<InboxCacheData>(inboxKeys.list(wsId)),
    ).toBeUndefined();
  });
});

describe("onInboxIssueStatusChanged", () => {
  it("updates issue_status only for items referencing the issue across pages", () => {
    const qc = new QueryClient();
    seedCache(qc, [
      [makeItem("i1", "issue-a", { issue_status: "todo" })],
      [makeItem("i2", "issue-b", { issue_status: "todo" })],
    ]);

    onInboxIssueStatusChanged(qc, wsId, "issue-a", "deployed");

    const after = qc.getQueryData<InboxCacheData>(inboxKeys.list(wsId));
    const flat = flattenInboxPages(after);
    expect(flat.find((i) => i.id === "i1")?.issue_status).toBe("deployed");
    expect(flat.find((i) => i.id === "i2")?.issue_status).toBe("todo");
  });
});
