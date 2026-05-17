import { render, screen, fireEvent } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { describe, it, expect, vi, beforeEach } from "vitest";
import type { ReactNode } from "react";
import type { SkillSummary } from "@multica/core/types";
import { workspaceKeys } from "@multica/core/workspace/queries";
import { PopularSkillsBar } from "./popular-skills-bar";
import { recordSkillUsage } from "../../editor/extensions/skill-recency";

function mkSkill(id: string, name: string, description = ""): SkillSummary {
  return {
    id,
    workspace_id: "ws-1",
    name,
    description,
    config: {},
    created_by: null,
    created_at: "2026-01-01T00:00:00Z",
    updated_at: "2026-01-01T00:00:00Z",
  };
}

function wrapWithSkills(skills: SkillSummary[]) {
  const qc = new QueryClient({
    defaultOptions: { queries: { retry: false, gcTime: 0 } },
  });
  qc.setQueryData(workspaceKeys.skills("ws-1"), skills);
  function Wrapper({ children }: { children: ReactNode }) {
    return <QueryClientProvider client={qc}>{children}</QueryClientProvider>;
  }
  return { qc, Wrapper };
}

beforeEach(() => {
  window.localStorage.clear();
});

describe("PopularSkillsBar", () => {
  it("(1) renders nothing when workspace has zero skills", () => {
    const { Wrapper } = wrapWithSkills([]);
    const { container } = render(
      <Wrapper>
        <PopularSkillsBar workspaceId="ws-1" onPick={vi.fn()} />
      </Wrapper>,
    );
    expect(container.firstChild).toBeNull();
  });

  it("(2) caps the number of pills shown at the default limit (5)", () => {
    const skills = ["a", "b", "c", "d", "e", "f", "g"].map((n, i) =>
      mkSkill(`s-${i}`, n),
    );
    const { Wrapper } = wrapWithSkills(skills);
    render(
      <Wrapper>
        <PopularSkillsBar workspaceId="ws-1" onPick={vi.fn()} />
      </Wrapper>,
    );
    // 5 pills expected, alphabetical (a..e)
    expect(screen.getAllByRole("button")).toHaveLength(5);
    expect(screen.getByText("/a")).toBeInTheDocument();
    expect(screen.getByText("/e")).toBeInTheDocument();
    expect(screen.queryByText("/f")).toBeNull();
  });

  it("(3) first render without recency → alphabetical top-N", () => {
    const skills = [mkSkill("s-c", "c"), mkSkill("s-a", "a"), mkSkill("s-b", "b")];
    const { Wrapper } = wrapWithSkills(skills);
    render(
      <Wrapper>
        <PopularSkillsBar workspaceId="ws-1" onPick={vi.fn()} />
      </Wrapper>,
    );
    const buttons = screen.getAllByRole("button");
    expect(buttons.map((b) => b.textContent)).toEqual(["/a", "/b", "/c"]);
  });

  it("(4) after recordSkillUsage(skill_x) and remount → skill_x first", () => {
    const skills = [mkSkill("s-a", "alpha"), mkSkill("s-b", "beta"), mkSkill("s-c", "gamma")];
    const { Wrapper } = wrapWithSkills(skills);
    recordSkillUsage("ws-1", "s-c"); // pick gamma
    render(
      <Wrapper>
        <PopularSkillsBar workspaceId="ws-1" onPick={vi.fn()} />
      </Wrapper>,
    );
    const buttons = screen.getAllByRole("button");
    expect(buttons[0]?.textContent).toBe("/gamma");
  });

  it("(5) click on pill calls onPick with the selected skill", () => {
    const skills = [mkSkill("s-a", "alpha")];
    const { Wrapper } = wrapWithSkills(skills);
    const onPick = vi.fn();
    render(
      <Wrapper>
        <PopularSkillsBar workspaceId="ws-1" onPick={onPick} />
      </Wrapper>,
    );
    fireEvent.click(screen.getByText("/alpha"));
    expect(onPick).toHaveBeenCalledOnce();
    expect(onPick).toHaveBeenCalledWith(expect.objectContaining({ id: "s-a", name: "alpha" }));
  });

  it("(6) stale recency for deleted skill is silently ignored at render", () => {
    const skills = [mkSkill("s-a", "alpha")];
    const { Wrapper } = wrapWithSkills(skills);
    // User had recorded a skill that has since been deleted server-side.
    recordSkillUsage("ws-1", "s-removed");
    render(
      <Wrapper>
        <PopularSkillsBar workspaceId="ws-1" onPick={vi.fn()} />
      </Wrapper>,
    );
    // Only the current skill is rendered. The stale entry doesn't crash.
    expect(screen.getAllByRole("button")).toHaveLength(1);
    expect(screen.getByText("/alpha")).toBeInTheDocument();
  });

  it("(7) honors a custom `limit` prop", () => {
    const skills = [
      mkSkill("s-a", "a"),
      mkSkill("s-b", "b"),
      mkSkill("s-c", "c"),
      mkSkill("s-d", "d"),
    ];
    const { Wrapper } = wrapWithSkills(skills);
    render(
      <Wrapper>
        <PopularSkillsBar workspaceId="ws-1" onPick={vi.fn()} limit={2} />
      </Wrapper>,
    );
    expect(screen.getAllByRole("button")).toHaveLength(2);
  });

  it("(8) aria-label is present on each pill for screen-reader users", () => {
    const skills = [mkSkill("s-a", "plan-and-implement")];
    const { Wrapper } = wrapWithSkills(skills);
    render(
      <Wrapper>
        <PopularSkillsBar workspaceId="ws-1" onPick={vi.fn()} />
      </Wrapper>,
    );
    expect(
      screen.getByRole("button", { name: "Insert /plan-and-implement" }),
    ).toBeInTheDocument();
  });

  it("(9) description renders as a tooltip via `title` attribute", () => {
    const skills = [
      mkSkill("s-a", "plan-and-implement", "End-to-end planning + impl"),
    ];
    const { Wrapper } = wrapWithSkills(skills);
    render(
      <Wrapper>
        <PopularSkillsBar workspaceId="ws-1" onPick={vi.fn()} />
      </Wrapper>,
    );
    const button = screen.getByRole("button", { name: /plan-and-implement/ });
    expect(button.getAttribute("title")).toBe("End-to-end planning + impl");
  });

  it("(10) toolbar role + aria-label set on container", () => {
    const skills = [mkSkill("s-a", "alpha")];
    const { Wrapper } = wrapWithSkills(skills);
    render(
      <Wrapper>
        <PopularSkillsBar workspaceId="ws-1" onPick={vi.fn()} />
      </Wrapper>,
    );
    expect(screen.getByRole("toolbar", { name: "Popular skills" })).toBeInTheDocument();
  });
});
