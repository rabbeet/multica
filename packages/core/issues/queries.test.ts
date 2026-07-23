import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { Issue } from "../types";

// PUL-468: exercises fetchFirstPages via `issueListOptions` — the branching
// between the fast path (single `api.boardIssues` call) and the legacy
// per-status fanout, plus the module-scoped capability memo that keeps a
// second fetch from re-probing.

const { mockListIssues, mockBoardIssues, MockBoardEndpointUnavailableError } = vi.hoisted(() => {
  class MockBoardEndpointUnavailableError extends Error {
    constructor() {
      super("board endpoint unavailable");
      this.name = "BoardEndpointUnavailableError";
    }
  }
  return {
    mockListIssues: vi.fn(),
    mockBoardIssues: vi.fn(),
    MockBoardEndpointUnavailableError,
  };
});

vi.mock("../api", () => ({
  api: {
    listIssues: (...args: any[]) => mockListIssues(...args),
    boardIssues: (...args: any[]) => mockBoardIssues(...args),
  },
  BoardEndpointUnavailableError: MockBoardEndpointUnavailableError,
}));

// eslint-disable-next-line @typescript-eslint/no-explicit-any
async function callFetchFirstPages(qopts: any): Promise<any> {
  return qopts.queryFn({} as any);
}

function makeIssue(id: string, status: string): Issue {
  return {
    id,
    workspace_id: "ws-1",
    number: 1,
    identifier: `WS-${id}`,
    title: `Issue ${id}`,
    description: null,
    status,
    priority: "none",
    assignee_type: null,
    assignee_id: null,
    creator_type: "member",
    creator_id: "u-1",
    parent_issue_id: null,
    project_id: null,
    position: 0,
    due_date: null,
    created_at: "2026-01-01T00:00:00Z",
    updated_at: "2026-01-01T00:00:00Z",
  } as Issue;
}

// Reset the module-scoped memo between tests. Imported inside beforeEach so
// the mock above is active by the time the module evaluates.
beforeEach(async () => {
  const q = await import("./queries");
  q.setBoardEndpointSupported(undefined);
  mockListIssues.mockReset();
  mockBoardIssues.mockReset();
});

afterEach(async () => {
  const q = await import("./queries");
  q.setBoardEndpointSupported(undefined);
});

describe("issueListOptions / fetchFirstPages — PUL-468 fast path", () => {
  it("calls boardIssues exactly once and buckets the response", async () => {
    mockBoardIssues.mockResolvedValueOnce({
      by_status: {
        todo: { issues: [makeIssue("i1", "todo")], total: 5 },
        in_progress: { issues: [], total: 0 },
      },
    });

    const { issueListOptions } = await import("./queries");
    const opts = issueListOptions("ws-1");
    const raw = await callFetchFirstPages(opts);

    expect(mockBoardIssues).toHaveBeenCalledTimes(1);
    expect(mockListIssues).not.toHaveBeenCalled();
    // Raw cache shape: { byStatus: { <status>: { issues, total } } }.
    expect(raw.byStatus.todo).toEqual({
      issues: [makeIssue("i1", "todo")],
      total: 5,
    });
    // Missing statuses are seeded to empty buckets by fetchFirstPages so the
    // consumer always finds every board status as a key.
    expect(raw.byStatus.backlog).toEqual({ issues: [], total: 0 });
  });

  it("memoises success so subsequent fetches skip the probe", async () => {
    mockBoardIssues.mockResolvedValue({ by_status: {} });

    const { issueListOptions, getBoardEndpointSupported } = await import("./queries");
    const opts = issueListOptions("ws-1");

    await callFetchFirstPages(opts);
    await callFetchFirstPages(opts);

    expect(mockBoardIssues).toHaveBeenCalledTimes(2);
    expect(getBoardEndpointSupported()).toBe(true);
    expect(mockListIssues).not.toHaveBeenCalled();
  });
});

describe("issueListOptions / fetchFirstPages — PUL-468 fallback", () => {
  it("falls back to per-status listIssues on 404 and memoises the failure", async () => {
    mockBoardIssues.mockRejectedValue(new MockBoardEndpointUnavailableError());
    mockListIssues.mockImplementation((params: any) =>
      Promise.resolve({ issues: [makeIssue("i-" + params.status, params.status)], total: 1 }),
    );

    const { issueListOptions, getBoardEndpointSupported } = await import("./queries");
    const opts = issueListOptions("ws-1");

    const first = await callFetchFirstPages(opts);
    expect(mockBoardIssues).toHaveBeenCalledTimes(1);
    // Board list has 10 statuses; the fanout fires one listIssues call per status.
    expect(mockListIssues).toHaveBeenCalledTimes(10);
    expect(first.byStatus.todo).toEqual({
      issues: [makeIssue("i-todo", "todo")],
      total: 1,
    });
    expect(getBoardEndpointSupported()).toBe(false);

    // Second fetch: no probe, straight to the legacy fanout.
    mockListIssues.mockClear();
    mockBoardIssues.mockClear();
    await callFetchFirstPages(opts);
    expect(mockBoardIssues).not.toHaveBeenCalled();
    expect(mockListIssues).toHaveBeenCalledTimes(10);
  });

  it("does not fall back on non-404 errors", async () => {
    const other = new Error("boom");
    mockBoardIssues.mockRejectedValue(other);

    const { issueListOptions, getBoardEndpointSupported } = await import("./queries");
    const opts = issueListOptions("ws-1");

    await expect(callFetchFirstPages(opts)).rejects.toBe(other);
    // Memo stays undefined so the next call still probes — a 500 might be
    // transient; hard-baking "unsupported" would leave the app on the slow
    // path forever after a single 5xx blip.
    expect(getBoardEndpointSupported()).toBeUndefined();
    expect(mockListIssues).not.toHaveBeenCalled();
  });

  it("respects a pre-seeded false capability (auth-initializer path)", async () => {
    mockListIssues.mockResolvedValue({ issues: [], total: 0 });

    const { issueListOptions, setBoardEndpointSupported } = await import("./queries");
    setBoardEndpointSupported(false);
    const opts = issueListOptions("ws-1");
    await callFetchFirstPages(opts);

    expect(mockBoardIssues).not.toHaveBeenCalled();
    expect(mockListIssues).toHaveBeenCalledTimes(10);
  });
});
