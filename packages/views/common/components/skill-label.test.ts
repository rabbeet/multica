import { describe, it, expect } from "vitest";
import { fallbackSkillLabel, labelForSkill, PRIORITY_LABELS } from "./skill-label";

describe("PUL-177 skill labels", () => {
  describe("labelForSkill (priority)", () => {
    it("returns curated label for /office-hours", () => {
      expect(labelForSkill("office-hours")).toBe("OH");
    });

    it("returns curated label for all four priority skills", () => {
      expect(labelForSkill("plan-ceo-review")).toBe("CEO");
      expect(labelForSkill("plan-eng-review")).toBe("ENG");
      expect(labelForSkill("plan-design-review")).toBe("DES");
    });
  });

  describe("labelForSkill (fallback)", () => {
    it("uppercases short single-segment slug", () => {
      expect(labelForSkill("qa")).toBe("QA");
    });

    it("truncates single-segment slug to 3 chars", () => {
      expect(labelForSkill("ship")).toBe("SHI");
      expect(labelForSkill("investigate")).toBe("INV");
    });

    it("initials multi-segment slug", () => {
      expect(labelForSkill("plan-and-implement")).toBe("PAI");
      expect(labelForSkill("design-review")).toBe("DR");
    });
  });

  describe("fallbackSkillLabel edge cases", () => {
    it("returns ? for empty input", () => {
      expect(fallbackSkillLabel("")).toBe("?");
    });

    it("returns ? for all-dash slug (no real segments)", () => {
      expect(fallbackSkillLabel("--")).toBe("?");
    });

    it("caps multi-segment label at 3 letters", () => {
      expect(fallbackSkillLabel("foo-bar-baz-qux")).toBe("FBB");
    });
  });

  describe("PRIORITY_LABELS shape", () => {
    it("has exactly the four priority slugs", () => {
      expect(Object.keys(PRIORITY_LABELS).sort()).toEqual([
        "office-hours",
        "plan-ceo-review",
        "plan-design-review",
        "plan-eng-review",
      ]);
    });
  });
});
