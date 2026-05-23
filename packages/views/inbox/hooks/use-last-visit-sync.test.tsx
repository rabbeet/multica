import { describe, it, expect, vi, beforeEach } from "vitest";
import { renderHook, act, waitFor } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import {
  useLastVisitSync,
  _resetLastVisitSyncForTests,
} from "./use-last-visit-sync";
import { useLastVisitStore } from "@multica/core/issues/stores";

const listLastVisitsMock = vi.fn();
const markIssueVisitedMock = vi.fn();

vi.mock("@multica/core/api", () => ({
  api: {
    listLastVisits: (...args: unknown[]) => listLastVisitsMock(...args),
    markIssueVisited: (...args: unknown[]) => markIssueVisitedMock(...args),
  },
}));

function wrap(): React.FC<{ children: React.ReactNode }> {
  const qc = new QueryClient({
    defaultOptions: { queries: { retry: false, gcTime: 0 } },
  });
  function Wrapper({ children }: { children: React.ReactNode }) {
    return <QueryClientProvider client={qc}>{children}</QueryClientProvider>;
  }
  return Wrapper;
}

beforeEach(() => {
  listLastVisitsMock.mockReset();
  markIssueVisitedMock.mockReset();
  listLastVisitsMock.mockResolvedValue({ items: [], total: 0 });
  markIssueVisitedMock.mockResolvedValue(undefined);
  useLastVisitStore.setState({ visits: {} });
  _resetLastVisitSyncForTests();
});

describe("useLastVisitSync (PUL-239)", () => {
  it("hydrates the store from /api/last-visits on first mount", async () => {
    const serverTs = "2026-05-23T03:00:00.000Z";
    listLastVisitsMock.mockResolvedValueOnce({
      items: [{ issue_id: "issue-A", last_visited_at: serverTs }],
      total: 1,
    });
    renderHook(() => useLastVisitSync("ws-1"), { wrapper: wrap() });
    await waitFor(() => {
      expect(listLastVisitsMock).toHaveBeenCalledWith("ws-1");
      expect(useLastVisitStore.getState().visits["issue-A"]).toBe(Date.parse(serverTs));
    });
  });

  it("does NOT re-hydrate on the second hook mount for the same workspace", async () => {
    const { unmount } = renderHook(() => useLastVisitSync("ws-1"), { wrapper: wrap() });
    await waitFor(() => expect(listLastVisitsMock).toHaveBeenCalledTimes(1));
    unmount();
    renderHook(() => useLastVisitSync("ws-1"), { wrapper: wrap() });
    // No new call — the per-tab guard prevented it.
    await new Promise((r) => setTimeout(r, 50));
    expect(listLastVisitsMock).toHaveBeenCalledTimes(1);
  });

  it("markVisited updates the local store AND POSTs to the server", async () => {
    const { result } = renderHook(() => useLastVisitSync("ws-1"), { wrapper: wrap() });
    await waitFor(() => expect(listLastVisitsMock).toHaveBeenCalled());
    act(() => {
      result.current.markVisited("issue-B");
    });
    // Local store updated synchronously.
    expect(useLastVisitStore.getState().visits["issue-B"]).toBeGreaterThan(0);
    // POST fired (async).
    await waitFor(() => {
      expect(markIssueVisitedMock).toHaveBeenCalledWith("ws-1", "issue-B");
    });
  });

  it("hydrate failure leaves the local cache intact (soft-fail)", async () => {
    useLastVisitStore.setState({ visits: { "local-only": 1234 } });
    listLastVisitsMock.mockRejectedValueOnce(new Error("offline"));
    renderHook(() => useLastVisitSync("ws-1"), { wrapper: wrap() });
    await waitFor(() => expect(listLastVisitsMock).toHaveBeenCalled());
    // Local entry survives — delta-mode UI falls back gracefully.
    expect(useLastVisitStore.getState().visits["local-only"]).toBe(1234);
  });
});
