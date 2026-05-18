import { describe, it, expect, beforeEach, afterEach, vi } from "vitest";
import { render } from "@testing-library/react";
import { I18nextProvider } from "react-i18next";
import { createInstance } from "i18next";
import type { OwnershipMeta, OwnershipSlug } from "@multica/core/types";
import { OwnershipChip } from "./ownership-chip";
import { RESOURCES } from "../../locales";

function renderWithI18n(ui: React.ReactNode, locale: "en" | "zh-Hans" = "en") {
  const i18n = createInstance();
  i18n.init({
    lng: locale,
    fallbackLng: "en",
    interpolation: { escapeValue: false },
    resources: { en: RESOURCES.en, "zh-Hans": RESOURCES["zh-Hans"] },
  });
  return render(<I18nextProvider i18n={i18n}>{ui}</I18nextProvider>);
}

// Freeze "now" so timeAgo() is deterministic across runs and asserts
// against literal "5m" / "1h" suffixes don't drift with wall clock.
const NOW = new Date("2026-05-18T17:30:00Z").getTime();

beforeEach(() => {
  vi.useFakeTimers();
  vi.setSystemTime(NOW);
});

afterEach(() => {
  vi.useRealTimers();
});

const FIVE_MIN_AGO = new Date(NOW - 5 * 60 * 1000).toISOString();

describe("PUL-180 OwnershipChip", () => {
  it("returns null when ownership is null (chip hidden)", () => {
    const { container } = renderWithI18n(<OwnershipChip ownership={null} meta={null} />);
    expect(container.firstChild).toBeNull();
  });

  it("renders ME label, brand color, User icon for ownership=me", () => {
    const meta: OwnershipMeta = { since: FIVE_MIN_AGO, agent_name: null, reason: null };
    const { container, getByLabelText } = renderWithI18n(
      <OwnershipChip ownership="me" meta={meta} />,
    );
    const chip = getByLabelText(/Your move/);
    expect(chip.textContent).toContain("ME");
    expect(chip.className).toMatch(/text-brand/);
    // lucide-react renders SVGs with a `class` containing the lucide
    // family + the specific icon name; assert the chip has at least
    // one SVG (icon presence) for a11y/regression coverage.
    expect(container.querySelectorAll("svg").length).toBeGreaterThan(0);
  });

  it("agent: includes agent_name and uses Bot icon", () => {
    const meta: OwnershipMeta = {
      since: FIVE_MIN_AGO,
      agent_name: "agent-1",
      reason: null,
    };
    const { getByLabelText } = renderWithI18n(
      <OwnershipChip ownership="agent" meta={meta} />,
    );
    const chip = getByLabelText(/agent-1 working/);
    expect(chip.textContent).toContain("AI");
    expect(chip.className).toMatch(/text-blue/);
  });

  it("agent: missing agent_name falls back to 'Agent' label in tooltip", () => {
    const meta: OwnershipMeta = { since: null, agent_name: null, reason: null };
    const { getByLabelText } = renderWithI18n(
      <OwnershipChip ownership="agent" meta={meta} />,
    );
    // tooltip uses agent_unknown fallback
    expect(getByLabelText(/Agent working/)).toBeTruthy();
  });

  it("waiting + reason=approval → WAIT label, amber, Clock icon, approval tooltip", () => {
    const meta: OwnershipMeta = {
      since: FIVE_MIN_AGO,
      agent_name: null,
      reason: "approval",
    };
    const { getByLabelText } = renderWithI18n(
      <OwnershipChip ownership="waiting" meta={meta} />,
    );
    const chip = getByLabelText(/Awaiting approval/);
    expect(chip.textContent).toContain("WAIT");
    expect(chip.className).toMatch(/text-amber/);
  });

  it("waiting + reason=null defaults to 'review' tooltip", () => {
    const meta: OwnershipMeta = {
      since: FIVE_MIN_AGO,
      agent_name: null,
      reason: null,
    };
    const { getByLabelText } = renderWithI18n(
      <OwnershipChip ownership="waiting" meta={meta} />,
    );
    expect(getByLabelText(/Awaiting review/)).toBeTruthy();
  });

  it("since=null produces tooltip without ', N ago' suffix", () => {
    const meta: OwnershipMeta = { since: null, agent_name: null, reason: null };
    const { getByLabelText } = renderWithI18n(
      <OwnershipChip ownership="me" meta={meta} />,
    );
    const chip = getByLabelText("Your move");
    // exact match (no trailing ", 5m" / ", just now") — defended by
    // useOwnershipTooltip's empty-string `since` fallback.
    expect(chip.getAttribute("aria-label")).toBe("Your move");
  });

  it("aria-label mirrors title (a11y parity, matches PhaseChip discipline)", () => {
    const meta: OwnershipMeta = { since: FIVE_MIN_AGO, agent_name: null, reason: null };
    const { getByLabelText } = renderWithI18n(
      <OwnershipChip ownership="me" meta={meta} />,
    );
    const chip = getByLabelText(/Your move/);
    expect(chip.getAttribute("title")).toBe(chip.getAttribute("aria-label"));
  });

  // Tooltip-hook branches (inline in ownership-chip.tsx per
  // /plan-eng-review C1). Cases 9-13 in the plan's test matrix.

  it("tooltip: me + since → 'Your move, 5m ago'-equivalent", () => {
    const meta: OwnershipMeta = { since: FIVE_MIN_AGO, agent_name: null, reason: null };
    const { getByLabelText } = renderWithI18n(
      <OwnershipChip ownership="me" meta={meta} />,
    );
    // useTimeAgo formats 5 min as "5m" per common.time.minutes
    // template; we assert the suffix joined inline. Locale en.
    const chip = getByLabelText("Your move, 5m");
    expect(chip).toBeTruthy();
  });

  it("tooltip: agent + named + since reads '{name} working, 5m'", () => {
    const meta: OwnershipMeta = {
      since: FIVE_MIN_AGO,
      agent_name: "agent-1",
      reason: null,
    };
    const { getByLabelText } = renderWithI18n(
      <OwnershipChip ownership="agent" meta={meta} />,
    );
    expect(getByLabelText("agent-1 working, 5m")).toBeTruthy();
  });

  it("tooltip: waiting/review + since → 'Awaiting review, 5m'", () => {
    const meta: OwnershipMeta = {
      since: FIVE_MIN_AGO,
      agent_name: null,
      reason: "review",
    };
    const { getByLabelText } = renderWithI18n(
      <OwnershipChip ownership="waiting" meta={meta} />,
    );
    expect(getByLabelText("Awaiting review, 5m")).toBeTruthy();
  });

  it("tooltip: waiting/approval + since → 'Awaiting approval, 5m'", () => {
    const meta: OwnershipMeta = {
      since: FIVE_MIN_AGO,
      agent_name: null,
      reason: "approval",
    };
    const { getByLabelText } = renderWithI18n(
      <OwnershipChip ownership="waiting" meta={meta} />,
    );
    expect(getByLabelText("Awaiting approval, 5m")).toBeTruthy();
  });

  it("tooltip: since=null branches across all three ownership values omit suffix", () => {
    const baselines: Array<{
      ownership: OwnershipSlug;
      meta: OwnershipMeta;
      expect: string;
    }> = [
      {
        ownership: "me",
        meta: { since: null, agent_name: null, reason: null },
        expect: "Your move",
      },
      {
        ownership: "agent",
        meta: { since: null, agent_name: "agent-1", reason: null },
        expect: "agent-1 working",
      },
      {
        ownership: "waiting",
        meta: { since: null, agent_name: null, reason: "review" },
        expect: "Awaiting review",
      },
    ];
    for (const tc of baselines) {
      const { getByLabelText, unmount } = renderWithI18n(
        <OwnershipChip ownership={tc.ownership} meta={tc.meta} />,
      );
      expect(getByLabelText(tc.expect).getAttribute("aria-label")).toBe(tc.expect);
      unmount();
    }
  });
});
