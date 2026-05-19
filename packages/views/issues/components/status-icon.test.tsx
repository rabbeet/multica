import { render } from "@testing-library/react";
import { describe, it, expect } from "vitest";
import type { IssueStatus } from "@multica/core/types";
import { STATUS_CONFIG } from "@multica/core/issues/config";
import { StatusIcon } from "./status-icon";

// PUL-199: regression coverage for the defensive fallback. Without the
// guard, rendering StatusIcon with a runtime string outside the IssueStatus
// union throws "undefined is not an object (evaluating 'e.iconColor')",
// which the IssueDetail ErrorBoundary then catches — making the entire
// ticket disappear after first paint.

describe("StatusIcon", () => {
  it("(1) renders a valid status without throwing", () => {
    expect(() =>
      render(<StatusIcon status="in_progress" />),
    ).not.toThrow();
  });

  it("(2) applies the configured iconColor class for a valid status", () => {
    const { container } = render(<StatusIcon status="deployed" />);
    const svg = container.querySelector("svg");
    expect(svg).not.toBeNull();
    expect(svg!.getAttribute("class") ?? "").toContain(
      STATUS_CONFIG.deployed.iconColor,
    );
  });

  it("(3) does NOT throw when given an unknown status string", () => {
    // The `as IssueStatus` cast simulates what happens at runtime when
    // an activity-log `details.to` carries a string outside the union.
    expect(() =>
      render(<StatusIcon status={"xyz" as IssueStatus} />),
    ).not.toThrow();
  });

  it("(4) falls back to the `todo` iconColor for an unknown status", () => {
    const { container } = render(
      <StatusIcon status={"xyz" as IssueStatus} />,
    );
    const svg = container.querySelector("svg");
    expect(svg).not.toBeNull();
    expect(svg!.getAttribute("class") ?? "").toContain(
      STATUS_CONFIG.todo.iconColor,
    );
  });

  it("(5) does NOT throw on an empty status string", () => {
    expect(() =>
      render(<StatusIcon status={"" as IssueStatus} />),
    ).not.toThrow();
  });

  it("(6) does NOT throw on a status string with trailing whitespace", () => {
    // Motivating hypothesis: server may write "in_progress " (trailing
    // space) into activity log. Defensive fallback should kick in
    // rather than silently trim — explicit > clever.
    expect(() =>
      render(<StatusIcon status={"in_progress " as IssueStatus} />),
    ).not.toThrow();
    const { container } = render(
      <StatusIcon status={"in_progress " as IssueStatus} />,
    );
    const svg = container.querySelector("svg");
    expect(svg!.getAttribute("class") ?? "").toContain(
      STATUS_CONFIG.todo.iconColor,
    );
  });

  it("(7) respects inheritColor=true and skips the iconColor class", () => {
    const { container } = render(
      <StatusIcon status="in_progress" inheritColor />,
    );
    const svg = container.querySelector("svg");
    expect(svg!.getAttribute("class") ?? "").not.toContain(
      STATUS_CONFIG.in_progress.iconColor,
    );
  });
});
