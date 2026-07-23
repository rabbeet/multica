import { infiniteQueryOptions, queryOptions } from "@tanstack/react-query";
import { api, BoardEndpointUnavailableError } from "../api";
import type {
  IssueStatus,
  ListIssuesParams,
  ListIssuesCache,
  TimelinePage,
  TimelinePageParam,
} from "../types";
import { BOARD_STATUSES } from "./config";

export const issueKeys = {
  all: (wsId: string) => ["issues", wsId] as const,
  list: (wsId: string) => [...issueKeys.all(wsId), "list"] as const,
  /** All "my issues" queries — use for bulk invalidation. */
  myAll: (wsId: string) => [...issueKeys.all(wsId), "my"] as const,
  /** Per-scope "my issues" list with filter identity baked into the key. */
  myList: (wsId: string, scope: string, filter: MyIssuesFilter) =>
    [...issueKeys.myAll(wsId), scope, filter] as const,
  detail: (wsId: string, id: string) =>
    [...issueKeys.all(wsId), "detail", id] as const,
  children: (wsId: string, id: string) =>
    [...issueKeys.all(wsId), "children", id] as const,
  childProgress: (wsId: string) =>
    [...issueKeys.all(wsId), "child-progress"] as const,
  /**
   * Cursor-paginated timeline cache. Around-mode lookups use a separate cache
   * (keyed by the anchor id) so an Inbox-jump fetch does not pollute the
   * default latest-page cache that the regular issue list path consumes.
   */
  timeline: (issueId: string, around?: string | null) =>
    around
      ? (["issues", "timeline", issueId, "around", around] as const)
      : (["issues", "timeline", issueId] as const),
  reactions: (issueId: string) => ["issues", "reactions", issueId] as const,
  /** PUL-177 — SkillHistory panel data: per-(issue, skill) state rows. */
  skillStates: (issueId: string) => ["issues", "skill-states", issueId] as const,
  subscribers: (issueId: string) =>
    ["issues", "subscribers", issueId] as const,
  usage: (issueId: string) => ["issues", "usage", issueId] as const,
  /** Per-issue task list (issue-detail Execution log section). */
  tasks: (issueId: string) => ["issues", "tasks", issueId] as const,
  /** Prefix-match key for invalidating tasks across all issues — used by
   *  the global WS task: prefix path so any task lifecycle event refreshes
   *  every per-issue list, regardless of which issue is currently mounted. */
  tasksAll: () => ["issues", "tasks"] as const,
};

export type MyIssuesFilter = Pick<
  ListIssuesParams,
  "assignee_id" | "assignee_ids" | "creator_id" | "project_id"
>;

/** Page size per status column. */
export const ISSUE_PAGE_SIZE = 50;

/** Statuses the issues/my-issues pages paginate. Cancelled is intentionally excluded — it has never been surfaced in the list/board views. */
export const PAGINATED_STATUSES: readonly IssueStatus[] = BOARD_STATUSES;

/** Flatten a bucketed response to a single Issue[] for consumers that want the whole list. */
export function flattenIssueBuckets(data: ListIssuesCache) {
  const out = [];
  for (const status of PAGINATED_STATUSES) {
    const bucket = data.byStatus[status];
    if (bucket) out.push(...bucket.issues);
  }
  return out;
}

/**
 * PUL-468 capability memo. `undefined` = not yet detected; `true`/`false` =
 * server's advertised capability, learned either from `/api/config`
 * (see auth-initializer) or lazily from a first-fetch 404 on the new
 * endpoint. Module-scoped so every subsequent fetch on the same page
 * session takes the same path — no repeated probes.
 *
 * Exported setter lets the auth-initializer seed the flag from the
 * `/api/config` `issues_board_endpoint` field before the user ever mounts
 * the issues page, which avoids the first-time 404 round-trip on servers
 * that have the flag off.
 */
let boardEndpointSupported: boolean | undefined = undefined;

/** Test hook + auth-initializer seed. Not part of the public API surface. */
export function setBoardEndpointSupported(supported: boolean | undefined): void {
  boardEndpointSupported = supported;
}

/** Test-only accessor for the capability memo. */
export function getBoardEndpointSupported(): boolean | undefined {
  return boardEndpointSupported;
}

async function fetchFirstPagesBoard(filter: MyIssuesFilter = {}): Promise<ListIssuesCache> {
  const board = await api.boardIssues({
    statuses: PAGINATED_STATUSES as unknown as string[],
    limit: ISSUE_PAGE_SIZE,
    ...filter,
  });
  const byStatus: ListIssuesCache["byStatus"] = {};
  for (const status of PAGINATED_STATUSES) {
    const bucket = board.by_status[status];
    byStatus[status] = bucket
      ? { issues: bucket.issues, total: bucket.total }
      : { issues: [], total: 0 };
  }
  return { byStatus };
}

async function fetchFirstPagesLegacy(filter: MyIssuesFilter = {}): Promise<ListIssuesCache> {
  const responses = await Promise.all(
    PAGINATED_STATUSES.map((status) =>
      api.listIssues({ status, limit: ISSUE_PAGE_SIZE, offset: 0, ...filter }),
    ),
  );
  const byStatus: ListIssuesCache["byStatus"] = {};
  PAGINATED_STATUSES.forEach((status, i) => {
    const res = responses[i]!;
    byStatus[status] = { issues: res.issues, total: res.total };
  });
  return { byStatus };
}

async function fetchFirstPages(filter: MyIssuesFilter = {}): Promise<ListIssuesCache> {
  // If /api/config already told us the endpoint is off, skip the probe.
  if (boardEndpointSupported === false) {
    return fetchFirstPagesLegacy(filter);
  }
  try {
    const cache = await fetchFirstPagesBoard(filter);
    boardEndpointSupported = true;
    return cache;
  } catch (err) {
    if (err instanceof BoardEndpointUnavailableError) {
      // First-time probe: server is old or has the flag off. Remember and
      // fall back — subsequent fetches skip the probe entirely.
      boardEndpointSupported = false;
      return fetchFirstPagesLegacy(filter);
    }
    throw err;
  }
}

/**
 * CACHE SHAPE NOTE: The raw cache stores {@link ListIssuesCache} (buckets keyed
 * by status, each with `{ issues, total }`), and `select` flattens it to
 * `Issue[]` for consumers. Mutations and ws-updaters must use
 * `setQueryData<ListIssuesCache>(...)` and preserve the byStatus shape.
 *
 * Fetches the first page of each paginated status in parallel. Use
 * {@link useLoadMoreByStatus} to paginate a specific status into the cache.
 */
export function issueListOptions(wsId: string) {
  return queryOptions({
    queryKey: issueKeys.list(wsId),
    queryFn: () => fetchFirstPages(),
    select: flattenIssueBuckets,
  });
}

/**
 * Server-filtered issue list for the My Issues page.
 * Each scope gets its own cache entry so switching tabs is instant after first load.
 */
export function myIssueListOptions(
  wsId: string,
  scope: string,
  filter: MyIssuesFilter,
) {
  return queryOptions({
    queryKey: issueKeys.myList(wsId, scope, filter),
    queryFn: () => fetchFirstPages(filter),
    select: flattenIssueBuckets,
  });
}

export function issueDetailOptions(wsId: string, id: string) {
  return queryOptions({
    queryKey: issueKeys.detail(wsId, id),
    queryFn: () => api.getIssue(id),
  });
}

// PUL-177 SkillHistory panel data. Lives on the issue detail page,
// so it's lazily fetched per-issue rather than batched with the
// inbox list (the Inbox already gets latest_skill via the CTE in
// ListInboxItems).
export function issueSkillStatesOptions(issueId: string) {
  return queryOptions({
    queryKey: issueKeys.skillStates(issueId),
    queryFn: () => api.listIssueSkillStates(issueId),
  });
}

/**
 * PUL-231 Mission Control workspace inbox.
 *
 * One HTTP call returns up to 100 active issues pre-joined with their
 * latest agent comment + skill state — the client parses the comment
 * body via extractAgentActions() to render chip rows inline, no N+1.
 *
 * PR2 launched with a 15s `refetchInterval` and no WS subscription.
 * PUL-238 (PR2.5) replaced the interval with event-driven invalidation
 * inside `use-action-inbox.ts` — that hook subscribes to
 * `comment:created` / `comment:updated` / `comment:deleted` /
 * `issue:updated`, debounces 200ms, and invalidates this query. So
 * here we keep the `refetchOnWindowFocus` safety net (a user coming
 * back from another tab gets a fresh fetch on focus) but drop the
 * unconditional interval — when nothing is happening on the workspace,
 * the inbox doesn't poll at all.
 */
export const actionInboxKey = (wsId: string) =>
  ["action-inbox", wsId] as const;

export function actionInboxOptions(wsId: string) {
  return queryOptions({
    queryKey: actionInboxKey(wsId),
    queryFn: () => api.listActionInbox(wsId),
    refetchOnWindowFocus: "always" as const,
  });
}

export function childIssueProgressOptions(wsId: string) {
  return queryOptions({
    queryKey: issueKeys.childProgress(wsId),
    queryFn: () => api.getChildIssueProgress(),
    select: (data) => {
      const map = new Map<string, { done: number; total: number }>();
      for (const entry of data.progress) {
        map.set(entry.parent_issue_id, { done: entry.done, total: entry.total });
      }
      return map;
    },
  });
}

export function childIssuesOptions(wsId: string, id: string) {
  return queryOptions({
    queryKey: issueKeys.children(wsId, id),
    queryFn: () => api.listChildIssues(id).then((r) => r.issues),
  });
}

/**
 * Infinite-query options for the cursor-paginated timeline. The first page is
 * either the latest 50 entries (no `around`) or a 50-wide window centered on
 * the given comment/activity id (Inbox jump path). `getNextPageParam` walks
 * older; `getPreviousPageParam` walks newer.
 */
export function issueTimelineInfiniteOptions(
  issueId: string,
  around?: string | null,
) {
  return infiniteQueryOptions<
    TimelinePage,
    Error,
    { pages: TimelinePage[]; pageParams: TimelinePageParam[] },
    readonly unknown[],
    TimelinePageParam
  >({
    queryKey: issueKeys.timeline(issueId, around ?? null),
    initialPageParam: around
      ? ({ mode: "around", id: around } as TimelinePageParam)
      : ({ mode: "latest" } as TimelinePageParam),
    queryFn: ({ pageParam }) => api.listTimeline(issueId, pageParam),
    // Walk older: append a page below the current oldest (last entry of the
    // last loaded page). undefined = no more older entries.
    getNextPageParam: (lastPage) =>
      lastPage.has_more_before && lastPage.next_cursor
        ? ({ mode: "before", cursor: lastPage.next_cursor } as TimelinePageParam)
        : undefined,
    // Walk newer: prepend a page above the current newest (first entry of the
    // first loaded page). undefined = at the latest, no newer to fetch.
    getPreviousPageParam: (firstPage) =>
      firstPage.has_more_after && firstPage.prev_cursor
        ? ({ mode: "after", cursor: firstPage.prev_cursor } as TimelinePageParam)
        : undefined,
  });
}

export function issueReactionsOptions(issueId: string) {
  return queryOptions({
    queryKey: issueKeys.reactions(issueId),
    queryFn: async () => {
      const issue = await api.getIssue(issueId);
      return issue.reactions ?? [];
    },
  });
}

export function issueSubscribersOptions(issueId: string) {
  return queryOptions({
    queryKey: issueKeys.subscribers(issueId),
    queryFn: () => api.listIssueSubscribers(issueId),
  });
}

export function issueUsageOptions(issueId: string) {
  return queryOptions({
    queryKey: issueKeys.usage(issueId),
    queryFn: () => api.getIssueUsage(issueId),
  });
}
