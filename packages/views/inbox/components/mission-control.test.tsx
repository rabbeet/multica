import { describe, it, expect, vi, beforeEach } from "vitest";
import { render, waitFor } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { I18nextProvider } from "react-i18next";
import { createInstance } from "i18next";
import { MissionControl } from "./mission-control";
import { actionInboxKey } from "@multica/core/issues/queries";
import { useDensityStore } from "@multica/core/issues/stores";
import { NavigationProvider } from "../../navigation";
import { RESOURCES } from "../../locales";
import type { ListActionInboxResponse } from "@multica/core/types";

const noopNav = {
  push: vi.fn(),
  replace: vi.fn(),
  back: vi.fn(),
  pathname: "/test",
  searchParams: new URLSearchParams(),
};

// Stub the workspace-id resolver so the component doesn't try to read
// it from a router context that doesn't exist in vitest's jsdom.
vi.mock("@multica/core/hooks", async (importOriginal) => {
  const actual = await importOriginal<typeof import("@multica/core/hooks")>();
  return {
    ...actual,
    useWorkspaceId: () => "test-ws-id",
  };
});

vi.mock("@multica/core/paths", async (importOriginal) => {
  const actual = await importOriginal<typeof import("@multica/core/paths")>();
  return {
    ...actual,
    useWorkspacePaths: () => ({
      issueDetail: (id: string) => `/ws/issues/${id}`,
      missionControl: () => "/ws/inbox/mission-control",
    }),
  };
});

// The chip primitives talk to api.createComment under the hood — stub
// so a render in vitest doesn't try to hit the (mocked) network.
vi.mock("@multica/core/issues/mutations", () => ({
  useCreateComment: () => ({
    mutate: vi.fn(),
    mutateAsync: vi.fn(),
  }),
}));

// PUL-238 — use-action-inbox now subscribes to WS events. Tests don't
// run real WebSocket plumbing, so stub the realtime hooks as no-ops.
// The cache-invalidation paths they wire are exercised in
// use-action-inbox.test.ts directly.
vi.mock("@multica/core/realtime", () => ({
  useWSEvent: () => {},
  useWSReconnect: () => {},
}));

function buildPayload(overrides?: Partial<ListActionInboxResponse>): ListActionInboxResponse {
  return {
    items: [
      {
        id: "issue-with-question",
        workspace_id: "test-ws-id",
        number: 222,
        identifier: "PUL-222",
        title: "Роутер router_new_1_max_sym",
        status: "in_progress",
        priority: "none",
        assignee_type: "agent",
        assignee_id: "a1",
        creator_type: "member",
        creator_id: "m1",
        created_at: new Date().toISOString(),
        updated_at: new Date().toISOString(),
        latest_agent_comment: {
          id: "c-q",
          author_id: "a1",
          created_at: new Date().toISOString(),
          content:
            "**Plan published.**\n\n1. **URL name**: default `rbtd_bg_sym`. Альтернативы: rbtd_bg / rbtd_anchor",
        },
      },
      {
        id: "issue-quiet",
        workspace_id: "test-ws-id",
        number: 224,
        identifier: "PUL-224",
        title: "Lift validity audit",
        status: "in_progress",
        priority: "none",
        assignee_type: "agent",
        assignee_id: "a1",
        creator_type: "member",
        creator_id: "m1",
        created_at: new Date().toISOString(),
        updated_at: new Date().toISOString(),
      },
    ],
    total: 2,
    ...overrides,
  };
}

function renderMissionControl(payload: ListActionInboxResponse | null) {
  const i18n = createInstance();
  i18n.init({
    lng: "en",
    fallbackLng: "en",
    interpolation: { escapeValue: false },
    resources: { en: RESOURCES.en },
  });
  const qc = new QueryClient({
    defaultOptions: { queries: { retry: false, gcTime: 0 } },
  });
  if (payload) {
    qc.setQueryData(actionInboxKey("test-ws-id"), payload);
  }
  return render(
    <QueryClientProvider client={qc}>
      <I18nextProvider i18n={i18n}>
        <NavigationProvider value={noopNav}>
          <MissionControl />
        </NavigationProvider>
      </I18nextProvider>
    </QueryClientProvider>,
  );
}

beforeEach(() => {
  // Density store is persisted; reset between tests so snapshots are stable.
  useDensityStore.setState({ density: "compact" });
});

describe("MissionControl", () => {
  it("splits items into 'Needs your action' vs 'In progress' by chip presence", async () => {
    const { findByText, queryByText } = renderMissionControl(buildPayload());
    // The issue with the agent-question goes into "Needs your action (1)"
    // — the chip detector found 1 numbered question with a bold title +
    // variants, so the row counts.
    expect(await findByText(/Needs your action \(1\)/)).toBeTruthy();
    // "In progress (1)" header is rendered collapsed by default — the
    // body row isn't visible until the user expands.
    expect(await findByText(/In progress \(1/)).toBeTruthy();
    expect(queryByText("Lift validity audit")).toBeNull();
  });

  it("renders the inbox-zero card when no items at all", async () => {
    const { findByText } = renderMissionControl({ items: [], total: 0 });
    expect(await findByText(/Inbox zero/i)).toBeTruthy();
  });

  it("renders inbox-zero variant when there are active items but none need action", async () => {
    const payload = buildPayload();
    payload.items = payload.items.filter((it) => !it.latest_agent_comment);
    payload.total = payload.items.length;
    const { findByText } = renderMissionControl(payload);
    // The 'no-action but has active issues' variant shows the count.
    expect(await findByText(/none waiting on you/i)).toBeTruthy();
  });

  it("density toggle flips state in the store and changes the button label", async () => {
    const { findByLabelText, findByText } = renderMissionControl(buildPayload());
    const toggle = await findByLabelText(/toggle row density/i);
    expect((await findByText("Compact")).textContent).toContain("Compact");
    toggle.click();
    await waitFor(() => {
      expect(useDensityStore.getState().density).toBe("expanded");
    });
  });

  it("renders the header even before data lands (so the skeleton has context)", () => {
    const { getByText } = renderMissionControl(null);
    expect(getByText(/Mission Control/i)).toBeTruthy();
  });
});
