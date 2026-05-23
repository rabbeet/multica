import { describe, it, expect } from "vitest";
import {
  formatAnsweredContent,
  extractAnsweredQuestionId,
  collectAnsweredQuestionIds,
} from "./answer-markers";

describe("formatAnsweredContent", () => {
  it("prepends the marker before the variant text", () => {
    const out = formatAnsweredContent("comment-A:1", "rbtd_bg_sym");
    expect(out).toBe('<div data-pul240-answer="comment-A:1"></div>\nrbtd_bg_sym');
  });

  it("round-trips through extractAnsweredQuestionId", () => {
    const out = formatAnsweredContent("c1:2", "any");
    expect(extractAnsweredQuestionId(out)).toBe("c1:2");
  });
});

describe("extractAnsweredQuestionId", () => {
  it("returns null on plain prose (no marker)", () => {
    expect(extractAnsweredQuestionId("just a reply, no marker")).toBeNull();
  });

  it("returns null on empty content", () => {
    expect(extractAnsweredQuestionId("")).toBeNull();
  });

  it("returns null when the marker appears mid-content (not at start)", () => {
    // We anchor the regex to start-of-line to avoid matching fenced code
    // blocks or paragraphs that happen to mention the attribute name.
    expect(
      extractAnsweredQuestionId('intro paragraph\n<div data-pul240-answer="c:1"></div>'),
    ).toBeNull();
  });
});

describe("collectAnsweredQuestionIds", () => {
  it("collects ids across multiple replies, ignores unmarked ones", () => {
    const ids = collectAnsweredQuestionIds([
      { content: '<div data-pul240-answer="c1:1"></div>\nrbtd_bg' },
      { content: "free-form member comment" },
      { content: '<div data-pul240-answer="c1:3"></div>\nshadow mode' },
      { content: null },
    ]);
    expect(ids.has("c1:1")).toBe(true);
    expect(ids.has("c1:3")).toBe(true);
    expect(ids.has("c1:2")).toBe(false);
    expect(ids.size).toBe(2);
  });

  it("returns an empty set when no entries carry markers", () => {
    const ids = collectAnsweredQuestionIds([
      { content: "go" },
      { content: "погнали" },
    ]);
    expect(ids.size).toBe(0);
  });
});
