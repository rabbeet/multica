import { describe, it, expect, beforeEach, vi } from "vitest";
import {
  recordSkillUsage,
  getSkillRecencyMap,
  sortSkillsByRecency,
  insertSkillAndRecord,
} from "./skill-recency";

const WS = "ws-1";

beforeEach(() => {
  window.localStorage.clear();
});

describe("skill-recency", () => {
  it("recordSkillUsage writes `{ skillId: timestamp }` under per-workspace key", () => {
    recordSkillUsage(WS, "skill-a");
    const raw = window.localStorage.getItem("multica:skill-recency:ws-1");
    expect(raw).not.toBeNull();
    const parsed = JSON.parse(raw!);
    expect(parsed["skill-a"]).toEqual(expect.any(Number));
  });

  it("getSkillRecencyMap reads back what record wrote", () => {
    recordSkillUsage(WS, "skill-a");
    recordSkillUsage(WS, "skill-b");
    const map = getSkillRecencyMap(WS);
    expect(map["skill-a"]).toEqual(expect.any(Number));
    expect(map["skill-b"]).toEqual(expect.any(Number));
  });

  it("isolates entries by workspace id", () => {
    recordSkillUsage("ws-1", "skill-a");
    recordSkillUsage("ws-2", "skill-b");
    expect(getSkillRecencyMap("ws-1")).toHaveProperty("skill-a");
    expect(getSkillRecencyMap("ws-1")).not.toHaveProperty("skill-b");
    expect(getSkillRecencyMap("ws-2")).toHaveProperty("skill-b");
  });

  it("silently no-ops when localStorage.setItem throws (quota exceeded)", () => {
    const originalSetItem = Storage.prototype.setItem;
    Storage.prototype.setItem = vi.fn(() => {
      throw new DOMException("QuotaExceededError");
    });
    try {
      expect(() => recordSkillUsage(WS, "skill-a")).not.toThrow();
    } finally {
      Storage.prototype.setItem = originalSetItem;
    }
  });

  it("prunes to MAX_ENTRIES=100 keeping the most recent", () => {
    // Record 105 entries with monotonically increasing timestamps.
    let t = 1_700_000_000_000;
    const dateSpy = vi.spyOn(Date, "now").mockImplementation(() => t++);
    try {
      for (let i = 0; i < 105; i++) {
        recordSkillUsage(WS, `skill-${i}`);
      }
    } finally {
      dateSpy.mockRestore();
    }
    const map = getSkillRecencyMap(WS);
    const keys = Object.keys(map);
    expect(keys.length).toBe(100);
    // The first 5 (oldest) should have been evicted.
    expect(map).not.toHaveProperty("skill-0");
    expect(map).not.toHaveProperty("skill-4");
    expect(map).toHaveProperty("skill-104");
  });

  it("returns empty map on corrupt JSON in localStorage", () => {
    window.localStorage.setItem("multica:skill-recency:ws-1", "{not json");
    const map = getSkillRecencyMap(WS);
    expect(map).toEqual({});
  });

  it("sortSkillsByRecency: recency DESC, alphabetical fallback for never-used", () => {
    const items = [
      { id: "b", name: "Beta" },
      { id: "a", name: "Alpha" },
      { id: "c", name: "Gamma" },
      { id: "d", name: "Delta" },
    ];
    const recency = { c: 2000, a: 1000 };
    const sorted = sortSkillsByRecency(items, recency);
    expect(sorted.map((s) => s.id)).toEqual(["c", "a", "b", "d"]);
    // c (most recent), a (older recent), b (alphabetical: Beta < Delta), d
  });

  it("sortSkillsByRecency falls back to fully alphabetical when recency map empty", () => {
    const items = [
      { id: "c", name: "Gamma" },
      { id: "a", name: "Alpha" },
      { id: "b", name: "Beta" },
    ];
    const sorted = sortSkillsByRecency(items, {});
    expect(sorted.map((s) => s.id)).toEqual(["a", "b", "c"]);
  });

  describe("insertSkillAndRecord helper", () => {
    it("focuses editor, inserts `/<name> `, and records usage", () => {
      const focus = vi.fn();
      const insertAtCursor = vi.fn();
      const editor = { focus, insertAtCursor };
      insertSkillAndRecord(editor, WS, { id: "skill-x", name: "do-thing" });
      expect(focus).toHaveBeenCalledOnce();
      expect(insertAtCursor).toHaveBeenCalledWith("/do-thing ");
      expect(getSkillRecencyMap(WS)).toHaveProperty("skill-x");
    });

    it("no-ops on null editor (does not throw, does not record)", () => {
      expect(() =>
        insertSkillAndRecord(null, WS, { id: "skill-x", name: "do-thing" }),
      ).not.toThrow();
      expect(getSkillRecencyMap(WS)).not.toHaveProperty("skill-x");
    });
  });
});
