import { act, render, screen, fireEvent } from "@testing-library/react";
import { createRef, type ReactNode } from "react";
import { describe, it, expect, vi, beforeEach } from "vitest";
import { workspaceKeys } from "@multica/core/workspace/queries";
import { I18nProvider } from "@multica/core/i18n/react";
import type { QueryClient } from "@tanstack/react-query";
import type { SkillSummary } from "@multica/core/types";
import enCommon from "../../locales/en/common.json";
import enAuth from "../../locales/en/auth.json";
import enSettings from "../../locales/en/settings.json";
import enEditor from "../../locales/en/editor.json";

const TEST_RESOURCES = {
  en: { common: enCommon, auth: enAuth, settings: enSettings, editor: enEditor },
};

function I18nWrapper({ children }: { children: ReactNode }) {
  return (
    <I18nProvider locale="en" resources={TEST_RESOURCES}>
      {children}
    </I18nProvider>
  );
}

// Mock the workspace id singleton — items() and command() read it
// imperatively.
vi.mock("@multica/core/platform", () => ({
  getCurrentWsId: () => "ws-1",
}));

import {
  createSkillSuggestion,
  SkillList,
  type SkillItem,
  type SkillListRef,
} from "./skill-suggestion";

function fakeQc(skills: SkillSummary[]): QueryClient {
  const map = new Map<string, unknown>();
  map.set(JSON.stringify(workspaceKeys.skills("ws-1")), skills);
  return {
    getQueryData: (key: readonly unknown[]) => map.get(JSON.stringify(key)),
  } as unknown as QueryClient;
}

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

beforeEach(() => {
  window.localStorage.clear();
});

describe("createSkillSuggestion", () => {
  it("(1) returns all workspace skills sync for an empty query, ranked by recency", () => {
    const qc = fakeQc([
      mkSkill("s-c", "gamma"),
      mkSkill("s-a", "alpha"),
      mkSkill("s-b", "beta"),
    ]);
    const config = createSkillSuggestion(qc);
    const result = config.items!({ query: "", editor: {} as never });
    expect(Array.isArray(result)).toBe(true);
    const items = result as SkillItem[];
    // No recency → alphabetical
    expect(items.map((i) => i.name)).toEqual(["alpha", "beta", "gamma"]);
  });

  it("(2) substring-filters when query is non-empty", () => {
    const qc = fakeQc([
      mkSkill("s-1", "plan-and-implement"),
      mkSkill("s-2", "plan-ceo-review"),
      mkSkill("s-3", "qa"),
    ]);
    const config = createSkillSuggestion(qc);
    const items = config.items!({ query: "pla", editor: {} as never }) as SkillItem[];
    expect(items.map((i) => i.name).sort()).toEqual([
      "plan-and-implement",
      "plan-ceo-review",
    ]);
  });

  it("(3) empty cache → empty result", () => {
    const qc = fakeQc([]);
    const config = createSkillSuggestion(qc);
    const items = config.items!({ query: "", editor: {} as never }) as SkillItem[];
    expect(items).toEqual([]);
  });

  it("(4) SkillList ArrowDown moves selection", () => {
    const ref = createRef<SkillListRef>();
    const command = vi.fn();
    const items: SkillItem[] = [
      { id: "a", name: "alpha" },
      { id: "b", name: "beta" },
    ];
    render(
      <I18nWrapper>
        <SkillList ref={ref} items={items} query="" command={command} />
      </I18nWrapper>,
    );
    // Each onKeyDown must flush React state before the next call, otherwise
    // the second call reads a stale `selectedIndex` (real Tiptap fires the
    // two key events in separate microtasks so the render gap exists in
    // production; tests have to step through it explicitly).
    act(() => {
      ref.current?.onKeyDown({ event: new KeyboardEvent("keydown", { key: "ArrowDown" }) });
    });
    act(() => {
      ref.current?.onKeyDown({ event: new KeyboardEvent("keydown", { key: "Enter" }) });
    });
    expect(command).toHaveBeenCalledWith(items[1]);
  });

  it("(5) SkillList Enter selects the currently highlighted item", () => {
    const ref = createRef<SkillListRef>();
    const command = vi.fn();
    const items: SkillItem[] = [{ id: "a", name: "alpha" }];
    render(
      <I18nWrapper>
        <SkillList ref={ref} items={items} query="" command={command} />
      </I18nWrapper>,
    );
    ref.current?.onKeyDown({ event: new KeyboardEvent("keydown", { key: "Enter" }) });
    expect(command).toHaveBeenCalledOnce();
    expect(command).toHaveBeenCalledWith(items[0]);
  });

  it("(6) Escape returns false from SkillList.onKeyDown (consumer handles cleanup)", () => {
    const ref = createRef<SkillListRef>();
    const items: SkillItem[] = [{ id: "a", name: "alpha" }];
    render(
      <I18nWrapper>
        <SkillList ref={ref} items={items} query="" command={vi.fn()} />
      </I18nWrapper>,
    );
    // SkillList itself does not handle Escape — the outer render() in
    // createSkillSuggestion does. So onKeyDown should return false for Escape.
    const result = ref.current?.onKeyDown({
      event: new KeyboardEvent("keydown", { key: "Escape" }),
    });
    expect(result).toBe(false);
  });

  it("(7) IME composing — Enter NOT intercepted (event.isComposing=true)", () => {
    const ref = createRef<SkillListRef>();
    const command = vi.fn();
    const items: SkillItem[] = [{ id: "a", name: "alpha" }];
    render(
      <I18nWrapper>
        <SkillList ref={ref} items={items} query="" command={command} />
      </I18nWrapper>,
    );
    const event = new KeyboardEvent("keydown", { key: "Enter" });
    Object.defineProperty(event, "isComposing", { value: true });
    const handled = ref.current?.onKeyDown({ event });
    expect(handled).toBe(false);
    expect(command).not.toHaveBeenCalled();
  });

  it("(8) empty items → renders 'No skills found' message", () => {
    render(
      <I18nWrapper>
        <SkillList items={[]} query="zzz" command={vi.fn()} />
      </I18nWrapper>,
    );
    expect(screen.getByText("No skills found")).toBeInTheDocument();
  });

  it("(9) command callback uses insertContentAt(range, '/<name> ') — plain text, NOT a node", () => {
    const qc = fakeQc([mkSkill("s-1", "plan-and-implement")]);
    const config = createSkillSuggestion(qc);
    const run = vi.fn();
    const chainObj: Record<string, unknown> = { run };
    chainObj.focus = vi.fn(() => chainObj);
    chainObj.insertContentAt = vi.fn(() => chainObj);
    const fakeEditor = { chain: vi.fn(() => chainObj) } as never;
    const range = { from: 0, to: 5 };
    config.command!({
      editor: fakeEditor,
      range,
      props: { id: "s-1", name: "plan-and-implement" },
    } as never);
    // Critical: the inserted content is a STRING, not an object/node spec.
    const insertContentAtMock = chainObj.insertContentAt as ReturnType<typeof vi.fn>;
    expect(insertContentAtMock).toHaveBeenCalledWith(range, "/plan-and-implement ");
    const firstCall = insertContentAtMock.mock.calls[0];
    expect(firstCall).toBeDefined();
    expect(typeof firstCall![1]).toBe("string");
    expect(run).toHaveBeenCalledOnce();
  });

  it("(10) command callback records the pick to skill-recency after insertion", () => {
    const qc = fakeQc([mkSkill("s-1", "plan-and-implement")]);
    const config = createSkillSuggestion(qc);
    const chainObj: Record<string, unknown> = { run: () => {} };
    chainObj.focus = () => chainObj;
    chainObj.insertContentAt = () => chainObj;
    const fakeEditor = { chain: () => chainObj } as never;
    config.command!({
      editor: fakeEditor,
      range: { from: 0, to: 5 },
      props: { id: "s-1", name: "plan-and-implement" },
    } as never);
    const stored = window.localStorage.getItem("multica:skill-recency:ws-1");
    expect(stored).not.toBeNull();
    expect(JSON.parse(stored!)["s-1"]).toEqual(expect.any(Number));
  });

  it("popular skill renders /name format and accepts click", () => {
    const command = vi.fn();
    const items: SkillItem[] = [{ id: "a", name: "plan-and-implement", description: "End-to-end planning" }];
    render(
      <I18nWrapper>
        <SkillList items={items} query="" command={command} />
      </I18nWrapper>,
    );
    expect(screen.getByText("/plan-and-implement")).toBeInTheDocument();
    expect(screen.getByText("End-to-end planning")).toBeInTheDocument();
    fireEvent.click(screen.getByText("/plan-and-implement"));
    expect(command).toHaveBeenCalledWith(items[0]);
  });
});
