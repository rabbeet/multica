import { describe, it, expect, vi, beforeEach } from "vitest";
import { render, fireEvent } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { I18nextProvider } from "react-i18next";
import { createInstance } from "i18next";
import { IssueTldrHeader } from "./issue-tldr-header";
import { issueKeys } from "@multica/core/issues/queries";
import {
  useLastVisitStore,
  useTldrCollapseStore,
} from "@multica/core/issues/stores";
import { RESOURCES } from "../../locales";
import type { SkillState, TimelineEntry } from "@multica/core/types";

// PUL-239 — the TL;DR header now uses useLastVisitSync (which calls
// /api/last-visits + /api/issues/:id/last-visit) instead of the raw
// store mark. Stub api so the tests focus on the header layout.
vi.mock("@multica/core/api", () => ({
  api: {
    listLastVisits: vi.fn().mockResolvedValue({ items: [], total: 0 }),
    markIssueVisited: vi.fn().mockResolvedValue(undefined),
  },
}));

function makeTimelinePage(entries: TimelineEntry[]) {
  return { entries, next_cursor: null, prev_cursor: null };
}

function renderHeader(opts: {
  skills?: SkillState[];
  timeline?: TimelineEntry[];
} = {}) {
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
  if (opts.skills !== undefined) {
    qc.setQueryData(issueKeys.skillStates("issue-1"), opts.skills);
  }
  if (opts.timeline !== undefined) {
    qc.setQueryData(issueKeys.timeline("issue-1", null), {
      pages: [makeTimelinePage(opts.timeline)],
      pageParams: [null],
    });
  }
  return render(
    <QueryClientProvider client={qc}>
      <I18nextProvider i18n={i18n}>
        <IssueTldrHeader workspaceId="ws-1" issueId="issue-1" />
      </I18nextProvider>
    </QueryClientProvider>,
  );
}

function agentComment(id: string, content: string, parentId: string | null = null): TimelineEntry {
  return {
    type: "comment",
    id,
    actor_type: "agent",
    actor_id: "agent-1",
    content,
    parent_id: parentId,
    comment_type: "comment",
    reactions: [],
    attachments: [],
    created_at: new Date().toISOString(),
    updated_at: new Date().toISOString(),
  };
}

beforeEach(() => {
  useLastVisitStore.setState({ visits: {} });
  useTldrCollapseStore.setState({ collapsedIssues: [] });
});

describe("IssueTldrHeader", () => {
  it("auto-suppresses when there are no skill states AND no open questions", () => {
    const { container } = renderHeader({ skills: [], timeline: [] });
    expect(container.querySelector("[data-testid='issue-tldr-header']")).toBeNull();
  });

  it("renders skill phases (done + in-progress) when skill states are present", () => {
    const now = new Date().toISOString();
    const { getByText } = renderHeader({
      skills: [
        { skill: "office-hours", status: "done", started_at: now, completed_at: now, updated_at: now },
        { skill: "plan-eng-review", status: "in_progress", started_at: now, completed_at: null, updated_at: now },
      ],
      timeline: [],
    });
    expect(getByText(/✓ office-hours/)).toBeTruthy();
    expect(getByText(/⏳ plan-eng-review/)).toBeTruthy();
  });

  it("counts open agent questions across the visible timeline and shows the jump button", () => {
    const md = "1. **URL name**: default `rbtd_bg_sym`. Альтернативы: rbtd_bg / rbtd_anchor";
    const { getByText } = renderHeader({
      skills: [],
      timeline: [agentComment("c-1", md)],
    });
    expect(getByText(/1 open question/i)).toBeTruthy();
    expect(getByText(/Jump to questions/i)).toBeTruthy();
  });

  it("PUL-240 — counts as open only the questions WITHOUT a matching answer marker", () => {
    // Agent posts 2 questions; user only answered q1. The header should
    // surface "1 open question", not 0 (PUL-240 fixes the prior crude
    // rule that treated any child reply as answering all questions).
    const md = [
      "Two questions to confirm.",
      "",
      "1. **URL name**: default `rbtd_bg_sym`. Альтернативы: rbtd_bg / rbtd_anchor",
      "",
      "2. **Atomic swap**: default `нет`. Альтернативы: да",
    ].join("\n");
    const { getByText, container } = renderHeader({
      skills: [],
      timeline: [
        agentComment("c-1", md),
        // Marker for q1 (ordinal=1) only — q2 still open.
        {
          ...agentComment(
            "c-reply",
            '<div data-pul240-answer="c-1:1"></div>\nrbtd_bg',
            "c-1",
          ),
          actor_type: "member",
          actor_id: "m-1",
        },
      ],
    });
    expect(getByText(/1 open question/i)).toBeTruthy();
    expect(container.querySelector("[data-testid='issue-tldr-header']")).not.toBeNull();
  });

  it("PUL-240 — header auto-suppresses when ALL questions on a parent are answered", () => {
    const md = "1. **URL name**: default `rbtd_bg_sym`. Альтернативы: rbtd_bg / rbtd_anchor";
    const { container } = renderHeader({
      skills: [],
      timeline: [
        agentComment("c-1", md),
        {
          ...agentComment(
            "c-reply",
            '<div data-pul240-answer="c-1:1"></div>\nrbtd_bg',
            "c-1",
          ),
          actor_type: "member",
          actor_id: "m-1",
        },
      ],
    });
    // openTotal=0 AND skills=[] → header returns null.
    expect(container.querySelector("[data-testid='issue-tldr-header']")).toBeNull();
  });

  it("collapse toggle persists into the store across renders", () => {
    const now = new Date().toISOString();
    const { getByLabelText } = renderHeader({
      skills: [
        { skill: "office-hours", status: "done", started_at: now, completed_at: now, updated_at: now },
      ],
      timeline: [],
    });
    fireEvent.click(getByLabelText(/Toggle TL;DR section/i));
    expect(useTldrCollapseStore.getState().collapsedIssues).toContain("issue-1");
  });

  it("marks the issue as visited on mount", () => {
    const now = new Date().toISOString();
    renderHeader({
      skills: [
        { skill: "office-hours", status: "done", started_at: now, completed_at: now, updated_at: now },
      ],
      timeline: [],
    });
    expect(useLastVisitStore.getState().visits["issue-1"]).toBeGreaterThan(0);
  });
});
