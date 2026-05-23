/**
 * PUL-240 — per-question "answered" marker.
 *
 * When the user taps a chip in `<AgentQuestionChips/>`, the threaded
 * reply prefixed the rendered variant text with a hidden HTML marker
 * carrying the question id (`${commentId}:${ordinal}`). The TL;DR
 * header and the Mission Control row count badge use that marker to
 * compute "open question count" precisely — without this, the prior
 * crude rule treated "any child reply" as answering ALL questions on
 * the parent comment (PUL-231 PR3 §answered-detection).
 *
 * The marker is emitted as a `<div data-pul240-answer="…">` element so:
 *   - the existing readonly-content sanitize-schema pass (extended below)
 *     keeps the attribute through `rehype-sanitize`.
 *   - react-markdown renders an empty `<div>` for it, but the surrounding
 *     variant text still flows naturally — no visual artifact.
 *
 * Round-trip safety: `formatAnsweredContent` builds the wire format;
 * `extractAnsweredQuestionId` reads it back. Both functions are pure
 * and dependency-free so they can run in the readonly renderer, the
 * TLDR header, the Mission Control row, and tests interchangeably.
 */

/** Wire-format marker prefix appended to chip-reply content. */
function buildMarker(questionId: string): string {
  // Plain double-quote attribute. No escaping needed today because
  // question ids only contain UUID + digit; if that ever changes the
  // sanitizer will catch the bad input.
  return `<div data-pul240-answer="${questionId}"></div>\n`;
}

/**
 * Wrap a variant reply with the answer marker the parser below will
 * pick up. The marker goes first so it's a leaf node, distinct from
 * the prose body.
 */
export function formatAnsweredContent(questionId: string, variant: string): string {
  return buildMarker(questionId) + variant;
}

/**
 * Pull the answered-question id out of a child reply's markdown, or
 * null when the marker isn't present (regular un-marked replies still
 * count as "the user replied to this comment" but don't carry per-
 * question precision).
 */
export function extractAnsweredQuestionId(content: string): string | null {
  if (!content) return null;
  // Tight regex — the marker is always a `<div data-pul240-answer="...">`
  // with no whitespace inside the tag. We don't want to match the same
  // attribute name appearing in a fenced code block, so anchor to a
  // start-of-line `<div` token.
  const match = content.match(/^<div\s+data-pul240-answer="([^"]+)"><\/div>/);
  return match ? match[1]! : null;
}

/**
 * Walk a list of timeline entries and return the set of question ids
 * that have a matching answer marker among their child replies.
 * Mission Control and the TL;DR header use this to compute open-question
 * counts per parent comment without re-scanning markdown twice.
 */
export function collectAnsweredQuestionIds(
  entries: Array<{ content?: string | null }>,
): Set<string> {
  const ids = new Set<string>();
  for (const e of entries) {
    const id = extractAnsweredQuestionId(e.content ?? "");
    if (id) ids.add(id);
  }
  return ids;
}
