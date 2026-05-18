import { describe, it, expect } from "vitest";
import { render } from "@testing-library/react";
import { I18nextProvider } from "react-i18next";
import { createInstance } from "i18next";
import type { SkillState } from "@multica/core/types";
import { LastSkillChip } from "./last-skill-chip";
import { RESOURCES } from "../../locales";

// Minimal i18n instance for component tests. Mirrors what
// packages/views/i18n/use-t.ts builds at runtime, scoped to the
// common namespace this chip reads from.
function renderWithI18n(ui: React.ReactNode) {
  const i18n = createInstance();
  i18n.init({
    lng: "en",
    fallbackLng: "en",
    interpolation: { escapeValue: false },
    resources: { en: RESOURCES.en },
  });
  return render(<I18nextProvider i18n={i18n}>{ui}</I18nextProvider>);
}

const baseState = (override: Partial<SkillState> = {}): SkillState => ({
  skill: "office-hours",
  status: "in_progress",
  started_at: new Date(Date.now() - 5 * 60 * 1000).toISOString(),
  completed_at: null,
  updated_at: new Date(Date.now() - 5 * 60 * 1000).toISOString(),
  ...override,
});

describe("PUL-177 LastSkillChip", () => {
  it("renders nothing when skill is null", () => {
    const { container } = renderWithI18n(<LastSkillChip skill={null} />);
    expect(container.textContent).toBe("");
  });

  it("renders priority label OH for office-hours in_progress", () => {
    const { getByLabelText } = renderWithI18n(<LastSkillChip skill={baseState()} />);
    const chip = getByLabelText(/office-hours/);
    expect(chip.textContent).toMatch(/·\s*OH/);
  });

  it("renders done with check glyph and brand class", () => {
    const { getByLabelText } = renderWithI18n(
      <LastSkillChip
        skill={baseState({
          status: "done",
          completed_at: new Date(Date.now() - 60 * 1000).toISOString(),
        })}
      />,
    );
    const chip = getByLabelText(/office-hours/);
    expect(chip.textContent).toMatch(/✓\s*OH/);
    expect(chip.className).toMatch(/bg-brand/);
  });

  it("uses fallback label for dynamic slug", () => {
    const { getByLabelText } = renderWithI18n(
      <LastSkillChip skill={baseState({ skill: "qa" })} />,
    );
    const chip = getByLabelText(/qa/);
    expect(chip.textContent).toMatch(/·\s*QA/);
  });

  it("tooltip text contains the relative timestamp", () => {
    const { getByLabelText } = renderWithI18n(<LastSkillChip skill={baseState()} />);
    const chip = getByLabelText(/office-hours/);
    // useTimeAgo formats 5 minutes ago as "5m" per common.time.minutes.
    expect(chip.getAttribute("aria-label")).toMatch(/started/);
    expect(chip.getAttribute("aria-label")).toMatch(/m/);
  });

  it("a11y aria-label mirrors the title attribute", () => {
    const { getByLabelText } = renderWithI18n(<LastSkillChip skill={baseState()} />);
    const chip = getByLabelText(/office-hours/);
    expect(chip.getAttribute("title")).toBe(chip.getAttribute("aria-label"));
  });
});
