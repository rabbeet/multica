import { describe, it, expect } from "vitest";
import { render } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { I18nextProvider } from "react-i18next";
import { createInstance } from "i18next";
import type { SkillState } from "@multica/core/types";
import { issueKeys } from "@multica/core/issues/queries";
import { SkillHistory } from "./skill-history";
import { RESOURCES } from "../../locales";

function renderWithProviders(
  ui: React.ReactNode,
  { initialStates }: { initialStates?: SkillState[] } = {},
) {
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
  if (initialStates !== undefined) {
    qc.setQueryData(issueKeys.skillStates("test-issue-id"), initialStates);
  }
  return render(
    <QueryClientProvider client={qc}>
      <I18nextProvider i18n={i18n}>{ui}</I18nextProvider>
    </QueryClientProvider>,
  );
}

const sample = (
  skill: string,
  status: "in_progress" | "done",
  minutesAgo: number,
): SkillState => {
  const iso = new Date(Date.now() - minutesAgo * 60 * 1000).toISOString();
  return {
    skill,
    status,
    started_at: iso,
    completed_at: status === "done" ? iso : null,
    updated_at: iso,
  };
};

describe("PUL-177 SkillHistory", () => {
  it("renders empty state when no skills applied", () => {
    const { getByText } = renderWithProviders(<SkillHistory issueId="test-issue-id" />, {
      initialStates: [],
    });
    expect(getByText("No skills have been applied to this ticket yet.")).toBeTruthy();
  });

  it("renders one row per skill", () => {
    const { container } = renderWithProviders(<SkillHistory issueId="test-issue-id" />, {
      initialStates: [sample("office-hours", "done", 30), sample("qa", "in_progress", 5)],
    });
    const items = container.querySelectorAll("li");
    expect(items.length).toBe(2);
  });

  it("renders priority slug label and full /<slug> form", () => {
    const { container } = renderWithProviders(<SkillHistory issueId="test-issue-id" />, {
      initialStates: [sample("plan-eng-review", "done", 10)],
    });
    expect(container.textContent).toContain("ENG");
    expect(container.textContent).toContain("/plan-eng-review");
  });

  it("renders 'done' subtitle for completed skills", () => {
    const { container } = renderWithProviders(<SkillHistory issueId="test-issue-id" />, {
      initialStates: [sample("office-hours", "done", 30)],
    });
    expect(container.textContent).toMatch(/done/);
  });

  it("renders 'in progress, started ...' subtitle for active skills", () => {
    const { container } = renderWithProviders(<SkillHistory issueId="test-issue-id" />, {
      initialStates: [sample("office-hours", "in_progress", 5)],
    });
    expect(container.textContent).toMatch(/in progress/);
    expect(container.textContent).toMatch(/started/);
  });
});
