import { describe, it, expect } from "vitest";
import { render } from "@testing-library/react";
import { I18nextProvider } from "react-i18next";
import { createInstance } from "i18next";
import type { PhaseSlug } from "@multica/core/types";
import { PhaseChip } from "./phase-chip";
import { RESOURCES } from "../../locales";

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

describe("PUL-177 PhaseChip", () => {
  it("renders PLN label and tooltip for planning", () => {
    const { getByLabelText } = renderWithI18n(<PhaseChip phase="planning" />);
    const chip = getByLabelText("Planning");
    expect(chip.textContent).toBe("PLN");
  });

  it("renders strikethrough class for cancelled", () => {
    const { getByLabelText } = renderWithI18n(<PhaseChip phase="cancelled" />);
    const chip = getByLabelText("Cancelled");
    expect(chip.className).toMatch(/line-through/);
  });

  it("renders all 7 phases with distinct labels", () => {
    const phases: PhaseSlug[] = [
      "backlog",
      "planning",
      "coding",
      "review",
      "done",
      "blocked",
      "cancelled",
    ];
    const seen = new Set<string>();
    for (const phase of phases) {
      const { container, unmount } = renderWithI18n(<PhaseChip phase={phase} />);
      const text = container.textContent ?? "";
      expect(text.length).toBeGreaterThan(0);
      seen.add(text);
      unmount();
    }
    // No two phases should render the same label — defends against
    // accidental copy-paste during future edits to PHASE_CONFIG.
    expect(seen.size).toBe(phases.length);
  });

  it("aria-label mirrors title (a11y parity)", () => {
    const { getByLabelText } = renderWithI18n(<PhaseChip phase="coding" />);
    const chip = getByLabelText("Coding");
    expect(chip.getAttribute("title")).toBe(chip.getAttribute("aria-label"));
  });
});
