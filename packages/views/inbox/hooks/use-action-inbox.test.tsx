import { describe, it, expect, vi, beforeEach } from "vitest";
import { renderHook, act } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { useActionInbox } from "./use-action-inbox";
import { actionInboxKey } from "@multica/core/issues/queries";
import type { ListActionInboxResponse } from "@multica/core/types";

// Realtime hooks normally require a WSProvider. Tests capture the
// handlers each hook is registered with so we can dispatch synthetic
// events through them without a real WebSocket.
const wsHandlers: Record<string, (payload: unknown) => void> = {};
let wsReconnectHandler: (() => void) | null = null;

vi.mock("@multica/core/realtime", () => ({
  useWSEvent: (event: string, handler: (payload: unknown) => void) => {
    wsHandlers[event] = handler;
  },
  useWSReconnect: (cb: () => void) => {
    wsReconnectHandler = cb;
  },
}));

function buildPayload(): ListActionInboxResponse {
  return {
    items: [],
    total: 0,
  };
}

function setup(workspaceId = "ws-1") {
  const qc = new QueryClient({
    defaultOptions: { queries: { retry: false, gcTime: 0 } },
  });
  qc.setQueryData(actionInboxKey(workspaceId), buildPayload());
  const wrapper = ({ children }: { children: React.ReactNode }) => (
    <QueryClientProvider client={qc}>{children}</QueryClientProvider>
  );
  const invalidateSpy = vi.spyOn(qc, "invalidateQueries");
  const result = renderHook(() => useActionInbox(workspaceId), { wrapper });
  return { qc, invalidateSpy, result };
}

beforeEach(() => {
  vi.useFakeTimers();
  for (const key of Object.keys(wsHandlers)) delete wsHandlers[key];
  wsReconnectHandler = null;
});

describe("useActionInbox — WS subscription (PUL-238)", () => {
  it("registers handlers for comment + issue events on mount", () => {
    setup();
    expect(typeof wsHandlers["comment:created"]).toBe("function");
    expect(typeof wsHandlers["comment:updated"]).toBe("function");
    expect(typeof wsHandlers["comment:deleted"]).toBe("function");
    expect(typeof wsHandlers["issue:updated"]).toBe("function");
    expect(typeof wsReconnectHandler).toBe("function");
  });

  it("collapses a 20-event burst into a single cache invalidation (200ms debounce)", () => {
    const { invalidateSpy } = setup();
    // Dispatch 20 comment:created events in rapid succession.
    for (let i = 0; i < 20; i++) {
      act(() => {
        wsHandlers["comment:created"]?.({ comment: { id: `c-${i}` } });
      });
    }
    // Nothing fires synchronously — the debounce is trailing-edge.
    expect(invalidateSpy).not.toHaveBeenCalled();
    // Wait out the 200ms window.
    act(() => {
      vi.advanceTimersByTime(250);
    });
    expect(invalidateSpy).toHaveBeenCalledTimes(1);
    expect(invalidateSpy).toHaveBeenCalledWith({ queryKey: actionInboxKey("ws-1") });
  });

  it("issue:updated for a DIFFERENT workspace does NOT invalidate", () => {
    const { invalidateSpy } = setup("ws-1");
    act(() => {
      wsHandlers["issue:updated"]?.({
        issue: { id: "i1", workspace_id: "ws-2" },
      });
    });
    act(() => {
      vi.advanceTimersByTime(300);
    });
    expect(invalidateSpy).not.toHaveBeenCalled();
  });

  it("WS reconnect fires its own (non-debounced) invalidation", () => {
    const { invalidateSpy } = setup();
    act(() => {
      wsReconnectHandler?.();
    });
    expect(invalidateSpy).toHaveBeenCalledTimes(1);
  });
});
