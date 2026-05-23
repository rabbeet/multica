import { describe, expect, it } from "vitest";
import { readFileSync } from "node:fs";
import { join } from "node:path";
import {
  preprocessAgentActions,
  extractAgentActions,
  parseVariantsFromTail,
  parseVariantsFromNestedBullets,
  decodeStringArray,
} from "./preprocess-agent-actions";

const FIXTURES_DIR = join(__dirname, "..", "__fixtures__");
function loadFixture(name: string): string {
  return readFileSync(join(FIXTURES_DIR, name), "utf8");
}

// ----------------------------------------------------------------------------
// Variant extraction unit tests
// ----------------------------------------------------------------------------

describe("parseVariantsFromTail", () => {
  it("extracts default + Russian alternatives", () => {
    const out = parseVariantsFromTail(
      "default `rbtd_bg_sym`. Альтернативы: rbtd_bg / rbtd_anchor / rbtd_combined",
    );
    expect(out).toEqual({
      default: "rbtd_bg_sym",
      variants: ["rbtd_bg_sym", "rbtd_bg", "rbtd_anchor", "rbtd_combined"],
    });
  });

  it("extracts bracket-letter variants with explicit (default)", () => {
    const out = parseVariantsFromTail("[a] flat array [b] grouped by date (default) [c] nested by supplier");
    expect(out?.default).toBe("grouped by date");
    expect(out?.variants).toEqual(["flat array", "grouped by date", "nested by supplier"]);
  });

  it("returns null on tail with no recognisable shape", () => {
    expect(parseVariantsFromTail("just a sentence with no choices")).toBeNull();
  });

  it("returns null when only one alternative is offered (would be a one-button question)", () => {
    expect(parseVariantsFromTail("[a] only one")).toBeNull();
  });
});

describe("parseVariantsFromNestedBullets", () => {
  it("reads variants from indented bullet list", () => {
    const lines = [
      "1. **Pilot strategy**:",
      "   - parallel pilot",
      "   - in-place replace",
      "   - shadow mode",
      "",
      "next paragraph",
    ];
    const out = parseVariantsFromNestedBullets(lines, 1, 0);
    expect(out).toEqual({
      default: null,
      variants: ["parallel pilot", "in-place replace", "shadow mode"],
      consumed: 3,
    });
  });

  it("detects (default) annotation in a bullet", () => {
    const lines = ["1. **X**:", "   - A", "   - B (default)", "   - C"];
    const out = parseVariantsFromNestedBullets(lines, 1, 0);
    expect(out?.default).toBe("B");
  });

  it("returns null on a list with fewer than 2 variants", () => {
    const lines = ["1. **X**:", "   - lonely"];
    expect(parseVariantsFromNestedBullets(lines, 1, 0)).toBeNull();
  });
});

// ----------------------------------------------------------------------------
// Fixture-based detection (regression suite)
// ----------------------------------------------------------------------------

describe("preprocessAgentActions — fixtures", () => {
  it("PUL-222 q1: detects single numbered question with default + Альтернативы", () => {
    const md = loadFixture("pul-222-q1.md");
    const { questions, commands } = extractAgentActions(md, "comment-1");
    expect(questions).toHaveLength(1);
    expect(commands).toHaveLength(0);
    const q = questions[0]!;
    expect(q.id).toBe("comment-1:1");
    expect(q.title).toBe("URL name");
    expect(q.default).toBe("rbtd_bg_sym");
    expect(q.variants).toEqual(["rbtd_bg_sym", "rbtd_bg", "rbtd_anchor", "rbtd_combined"]);
    expect(q.parentCommentId).toBe("comment-1");
  });

  it("PUL-222 q2: detects ordinal-2 question (numbering preserved from source)", () => {
    const md = loadFixture("pul-222-q2.md");
    const { questions } = extractAgentActions(md, "c2");
    expect(questions).toHaveLength(1);
    expect(questions[0]!.title).toBe("Atomic swap");
    // Default is "нет" — encoded with backticks in the source, stripped by parser.
    expect(questions[0]!.default).toBe("нет");
    expect(questions[0]!.variants).toContain("нет");
    expect(questions[0]!.variants.length).toBeGreaterThanOrEqual(2);
  });

  it("PUL-222 q3: question under a header still detected", () => {
    const md = loadFixture("pul-222-q3.md");
    const { questions } = extractAgentActions(md, "c3");
    expect(questions).toHaveLength(1);
    expect(questions[0]!.title).toBe("BG canonical duplicates");
  });

  it("PUL-193 q1: nested bullet-list variants detected (verbatim bullet text preserved)", () => {
    const md = loadFixture("pul-193-q1.md");
    const { questions } = extractAgentActions(md, "c4");
    expect(questions).toHaveLength(1);
    expect(questions[0]!.variants).toHaveLength(3);
    expect(questions[0]!.variants[0]).toMatch(/parallel pilot/);
    expect(questions[0]!.variants[1]).toMatch(/in-place replace/);
    expect(questions[0]!.variants[2]).toMatch(/shadow mode/);
    expect(questions[0]!.default).toBeNull();
  });

  it("PUL-193 q2: bracket-letter format with explicit default", () => {
    const md = loadFixture("pul-193-q2.md");
    const { questions } = extractAgentActions(md, "c5");
    expect(questions).toHaveLength(1);
    expect(questions[0]!.default).toBe("grouped by date");
    expect(questions[0]!.variants).toHaveLength(3);
  });

  it("PUL-227 recovery: detects command block gated by ack sentinel", () => {
    const md = loadFixture("pul-227-recovery.md");
    const { questions, commands } = extractAgentActions(md, "c6");
    expect(questions).toHaveLength(0);
    expect(commands).toHaveLength(1);
    const c = commands[0]!;
    expect(c.id).toBe("c6:cmd-1");
    expect(c.commands).toHaveLength(4);
    expect(c.commands[0]).toBe("php artisan endpoints:refresh router_new_1_max_sym");
    expect(c.groupLabel).toBe("Быстрый recovery");
  });
});

// ----------------------------------------------------------------------------
// preprocessAgentActions string-transform behaviour
// ----------------------------------------------------------------------------

describe("preprocessAgentActions — string transform", () => {
  it("skips work entirely when isAgent=false", () => {
    const md = loadFixture("pul-222-q1.md");
    expect(preprocessAgentActions(md, false)).toBe(md);
  });

  it("returns unchanged markdown when no patterns detected", () => {
    const md = "Plain prose with no questions and no fenced code.\n\nSecond paragraph.";
    expect(preprocessAgentActions(md, true)).toBe(md);
  });

  it("injects a question marker after the matched item", () => {
    const md = loadFixture("pul-222-q1.md");
    const out = preprocessAgentActions(md, true);
    expect(out).toContain('data-type="agentQuestion"');
    expect(out).toContain('data-question-ordinal="1"');
    // The original question content is preserved verbatim.
    expect(out).toContain("**URL name**");
  });

  it("encoded data-variants attribute round-trips to the original variant list", () => {
    const md = loadFixture("pul-222-q1.md");
    const out = preprocessAgentActions(md, true);
    const match = out.match(/data-variants="([^"]+)"/);
    expect(match).not.toBeNull();
    // The escapeAttr step replaces &quot; — decode HTML entities first.
    const raw = match![1]!.replace(/&quot;/g, '"').replace(/&amp;/g, "&");
    expect(decodeStringArray(raw)).toEqual([
      "rbtd_bg_sym",
      "rbtd_bg",
      "rbtd_anchor",
      "rbtd_combined",
    ]);
  });

  it("injects a command marker after a fenced block with ack sentinel", () => {
    const md = loadFixture("pul-227-recovery.md");
    const out = preprocessAgentActions(md, true);
    expect(out).toContain('data-type="agentCommandBlock"');
    expect(out).toContain('data-command-ordinal="1"');
    expect(out).toContain('data-group-label="Быстрый recovery"');
  });

  it("does not inject a command marker for a code block with no ack sentinel", () => {
    const md = "Look at this snippet:\n\n```\nls -la\n```\n\nNothing more to say.";
    const out = preprocessAgentActions(md, true);
    expect(out).not.toContain("agentCommandBlock");
  });

  it("does not detect numbered lists without **bold** title (strict pattern)", () => {
    const md = "Things to do:\n\n1. buy groceries\n2. walk the dog\n3. write report";
    const out = preprocessAgentActions(md, true);
    expect(out).not.toContain("agentQuestion");
  });

  it("preserves all original lines (only appends markers, never deletes)", () => {
    const md = loadFixture("pul-222-q1.md");
    const out = preprocessAgentActions(md, true);
    for (const line of md.split("\n")) {
      if (line.trim()) expect(out).toContain(line);
    }
  });
});
