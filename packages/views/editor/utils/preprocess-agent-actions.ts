/**
 * Inject inline answer-chip markers into agent-authored markdown.
 *
 * Detects two patterns the agent emits when waiting on the user:
 *   1. Numbered question with a bold title and a tail listing variants —
 *      `1. **URL name**: default \`rbtd_bg_sym\`. Альтернативы: rbtd_bg / rbtd_anchor`
 *   2. Fenced code block followed (within the same comment) by a paragraph
 *      containing an ack sentinel — «Запускать сам не буду — нужен ваш ack».
 *
 * For each match, an HTML marker `<div data-type="agentQuestion" …>` or
 * `<div data-type="agentCommandBlock" …>` is appended inside the originating
 * block. The readonly renderer's `components.div` override (see
 * `readonly-content.tsx`) reads the `data-*` attributes and mounts
 * `<AgentQuestionChips/>` / `<AgentCommandButton/>` in place.
 *
 * `parent_id` for the threaded reply is NOT in the markdown — chip
 * components read it from the surrounding React context (the comment's id
 * is known to comment-card.tsx, which provides it via AuthorContext).
 *
 * Both functions are pure. The detector also exists as a standalone
 * `extractAgentActions()` returning typed AgentActions — Mission Control
 * (PR2) reuses it to surface open questions across all issues without
 * rendering any markdown.
 */

import type { AgentActions, AgentQuestion, AgentCommand } from "@multica/core/types";

// ----------------------------------------------------------------------------
// Patterns
// ----------------------------------------------------------------------------

/**
 * First-line shape of an ordered-list item that looks like a question:
 *   `(indent)(N). **Title**(:|.|nothing) (tail)`
 *
 * The title is what's between the first pair of `**`. The tail is the
 * rest of the first line — variants are parsed out of it (plus any
 * nested bullet list on subsequent indented lines).
 */
const QUESTION_HEAD_RE = /^(\s*)(\d+)\.\s+\*\*([^*]+)\*\*[:.]?\s*(.*)$/;

/** Fenced code block opener — matches the indent so we know how nested we are. */
const CODE_FENCE_RE = /^(\s*)```/;

/**
 * Patterns in the tail that signal "agent will wait for your ack before
 * running these". Matched against the paragraph that follows a code
 * fence, case-insensitive. The list is intentionally tight to avoid
 * false positives on agent comments that include code samples for
 * reference.
 */
const ACK_SENTINELS = [
  /запускать\s+сам\s+не\s+буду/i,
  /нужен\s+(ваш\s+)?ack/i,
  /explicit\s+go-ahead/i,
  /дайте\s+ack/i,
];

/** Nearest preceding `## Header` line (any level ≤4) above a block. */
const HEADER_LINE_RE = /^#{1,4}\s+(.+)$/;

// ----------------------------------------------------------------------------
// Variant extraction
// ----------------------------------------------------------------------------

/**
 * Pull variants out of a question's tail.
 *
 * Three forms, tried in order — the first that yields ≥2 variants wins:
 *   (a) `default \`X\`. Альтернативы: A / B / C` — Russian-conventional
 *       "default + alternatives" shape used by agent skills today.
 *   (b) `[a] X [b] Y [c] Z` — bracket-letter format from /office-hours
 *       output.
 *   (c) `default: A` followed by a nested bullet list (handled by the
 *       caller, which has access to the subsequent lines).
 *
 * Returns null when neither form yields a usable variant set — the
 * caller falls back to plain markdown rendering (no chips).
 */
export function parseVariantsFromTail(tail: string): {
  default: string | null;
  variants: string[];
} | null {
  // (a) default + Альтернативы list
  const defaultMatch = tail.match(/default\s+`([^`]+)`/i);
  const altMatch = tail.match(/[Аа]льтернативы:\s*(.+?)(?:\.|$)/);
  if (defaultMatch && altMatch) {
    const defaultVal = defaultMatch[1]!.trim();
    const alternatives = altMatch[1]!
      .split(/\s*\/\s*/)
      .map((s) => s.trim().replace(/^`|`$/g, ""))
      .filter(Boolean);
    if (alternatives.length >= 1) {
      const allVariants = [defaultVal, ...alternatives.filter((a) => a !== defaultVal)];
      if (allVariants.length >= 2) {
        return { default: defaultVal, variants: allVariants };
      }
    }
  }

  // (b) bracket-letter format: [a] foo [b] bar [c] baz
  const bracketRe = /\[([a-z])\]\s*([^[]+?)(?=\s*\[[a-z]\]|\s*$)/gi;
  const bracketVariants: string[] = [];
  let match: RegExpExecArray | null;
  while ((match = bracketRe.exec(tail)) !== null) {
    bracketVariants.push(match[2]!.trim().replace(/[.,;]+$/, "").trim());
  }
  if (bracketVariants.length >= 2) {
    // Default is whichever variant is annotated with "(default)" — first
    // such match wins; otherwise no default.
    const defaultIdx = bracketVariants.findIndex((v) => /\(default\)/i.test(v));
    const cleaned = bracketVariants.map((v) => v.replace(/\s*\(default\)\s*/i, "").trim());
    return {
      default: defaultIdx >= 0 ? cleaned[defaultIdx]! : null,
      variants: cleaned,
    };
  }

  return null;
}

/**
 * Pull variants out of a nested bullet list directly under the question.
 *
 * Lines look like `  - rbtd_bg_sym` (indented relative to the item).
 * Stops at the first line that's not a same-indent bullet — that
 * terminates the variant list.
 *
 * Detects `(default)` annotation on any bullet to mark the default
 * variant; "  - rbtd_bg_sym (default)" → default = "rbtd_bg_sym".
 */
export function parseVariantsFromNestedBullets(
  lines: string[],
  startIndex: number,
  minIndent: number,
): { default: string | null; variants: string[]; consumed: number } | null {
  const variants: string[] = [];
  let defaultVal: string | null = null;
  let i = startIndex;
  // Allow up to one blank line between question title and nested bullets,
  // mirroring how CommonMark treats loose lists.
  if (i < lines.length && lines[i]!.trim() === "") i++;

  while (i < lines.length) {
    const line = lines[i]!;
    const indentMatch = line.match(/^(\s*)/);
    const indent = indentMatch ? indentMatch[1]!.length : 0;
    if (indent <= minIndent || line.trim() === "") break;
    const bulletMatch = line.trim().match(/^[-*+]\s+(.+)$/);
    if (!bulletMatch) break;
    let text = bulletMatch[1]!.trim();
    if (/\(default\)/i.test(text)) {
      text = text.replace(/\s*\(default\)\s*/i, "").trim();
      defaultVal = text;
    }
    variants.push(text.replace(/^`|`$/g, ""));
    i++;
  }
  if (variants.length < 2) return null;
  return { default: defaultVal, variants, consumed: i - startIndex };
}

// ----------------------------------------------------------------------------
// HTML marker emit
// ----------------------------------------------------------------------------

function escapeAttr(s: string): string {
  return s.replace(/&/g, "&amp;").replace(/"/g, "&quot;").replace(/</g, "&lt;").replace(/>/g, "&gt;");
}

/**
 * Encode an array of strings as a URI-encoded JSON literal — safe for
 * embedding in an HTML attribute value, lossless on round-trip.
 * Avoids the "did the variant contain a `|`?" footgun a separator-based
 * encoding would have.
 */
function encodeStringArray(arr: string[]): string {
  return encodeURIComponent(JSON.stringify(arr));
}

export function decodeStringArray(attr: string | undefined): string[] {
  if (!attr) return [];
  try {
    const parsed = JSON.parse(decodeURIComponent(attr));
    return Array.isArray(parsed) ? parsed.filter((s): s is string => typeof s === "string") : [];
  } catch {
    return [];
  }
}

function questionMarker(q: {
  ordinal: number;
  title: string;
  default: string | null;
  variants: string[];
}): string {
  const attrs = [
    `data-type="agentQuestion"`,
    `data-question-ordinal="${q.ordinal}"`,
    `data-question-title="${escapeAttr(q.title)}"`,
    `data-default="${q.default !== null ? escapeAttr(q.default) : ""}"`,
    `data-variants="${escapeAttr(encodeStringArray(q.variants))}"`,
  ].join(" ");
  return `<div ${attrs}></div>`;
}

function commandMarker(c: {
  ordinal: number;
  commands: string[];
  groupLabel: string;
}): string {
  const attrs = [
    `data-type="agentCommandBlock"`,
    `data-command-ordinal="${c.ordinal}"`,
    `data-commands="${escapeAttr(encodeStringArray(c.commands))}"`,
    `data-group-label="${escapeAttr(c.groupLabel)}"`,
  ].join(" ");
  return `<div ${attrs}></div>`;
}

// ----------------------------------------------------------------------------
// Core walk — used by both the preprocessor and the typed extractor
// ----------------------------------------------------------------------------

interface DetectedQuestion {
  ordinal: number;
  title: string;
  tail: string;
  default: string | null;
  variants: string[];
  /** Index in the source line array of the question's first line.
   *  The marker should be inserted *after* the question's content block. */
  insertAfterLine: number;
  /** Indentation prefix to apply to the marker so it stays inside the li. */
  itemIndent: string;
}

interface DetectedCommand {
  ordinal: number;
  commands: string[];
  groupLabel: string;
  /** Index of the closing fence line. Marker goes on the next line. */
  insertAfterLine: number;
}

/** First pass: walk markdown lines, classify blocks, return detections. */
function walkAgentActions(markdown: string): {
  questions: DetectedQuestion[];
  commands: DetectedCommand[];
} {
  const lines = markdown.split("\n");
  const questions: DetectedQuestion[] = [];
  const commands: DetectedCommand[] = [];

  let questionOrdinal = 0;
  let commandOrdinal = 0;

  let i = 0;
  while (i < lines.length) {
    const line = lines[i]!;

    // --- Fenced code block ----------------------------------------------------
    const fenceOpen = line.match(CODE_FENCE_RE);
    if (fenceOpen) {
      const openIndent = fenceOpen[1]!.length;
      const blockLines: string[] = [];
      let close = i + 1;
      while (close < lines.length) {
        const closeMatch = lines[close]!.match(CODE_FENCE_RE);
        if (closeMatch && closeMatch[1]!.length === openIndent) break;
        blockLines.push(lines[close]!);
        close++;
      }
      // Look for ack sentinel within the next 8 lines (a paragraph or two).
      let ackFound = false;
      for (let k = close + 1; k < Math.min(lines.length, close + 9); k++) {
        if (ACK_SENTINELS.some((re) => re.test(lines[k]!))) {
          ackFound = true;
          break;
        }
      }
      if (ackFound) {
        const commandList = blockLines
          .map((l) => l.trim())
          .filter((l) => l.length > 0 && !l.startsWith("#"));
        if (commandList.length > 0) {
          // Find nearest preceding header for the group label.
          let groupLabel = "";
          for (let h = i - 1; h >= 0 && h >= i - 20; h--) {
            const hMatch = lines[h]!.match(HEADER_LINE_RE);
            if (hMatch) {
              groupLabel = hMatch[1]!.trim();
              break;
            }
          }
          commandOrdinal++;
          commands.push({
            ordinal: commandOrdinal,
            commands: commandList,
            groupLabel,
            insertAfterLine: close,
          });
        }
      }
      i = close + 1;
      continue;
    }

    // --- Numbered question item ----------------------------------------------
    const qHead = line.match(QUESTION_HEAD_RE);
    if (qHead) {
      const indent = qHead[1]!;
      // The source-side ordinal (`1.`, `2.`…) is intentionally ignored —
      // we use detection-order (`questionOrdinal++`) to keep ids unique
      // even when the agent skips numbers or duplicates a `1.` mid-list.
      // The aria-label still reads "question N" where N is detection
      // order, which matches what the user visually counts as they scan
      // the comment in document order.
      const title = qHead[3]!.trim();
      const tail = qHead[4]!.trim();

      // Try variant extraction from tail first.
      let parsed = parseVariantsFromTail(tail);
      let consumedAfterHead = 0;
      let insertAfter = i;

      if (!parsed) {
        // Fallback: nested bullets immediately after.
        const bulletResult = parseVariantsFromNestedBullets(lines, i + 1, indent.length);
        if (bulletResult) {
          parsed = { default: bulletResult.default, variants: bulletResult.variants };
          consumedAfterHead = bulletResult.consumed;
          insertAfter = i + bulletResult.consumed;
        }
      }

      if (parsed && parsed.variants.length >= 2) {
        questionOrdinal++;
        questions.push({
          ordinal: questionOrdinal,
          title,
          tail,
          default: parsed.default,
          variants: parsed.variants,
          insertAfterLine: insertAfter,
          itemIndent: indent + "   ", // 3-space indent keeps the marker inside the li
        });
      }

      i += 1 + consumedAfterHead;
      continue;
    }

    i++;
  }

  return { questions, commands };
}

// ----------------------------------------------------------------------------
// Public API
// ----------------------------------------------------------------------------

/**
 * Inject HTML chip markers into agent markdown.
 *
 * @param markdown   Raw markdown content.
 * @param isAgent    Skip injection entirely for non-agent authors. The
 *                   caller (preprocess.ts in ReadonlyContent) gets this
 *                   from AuthorContext.
 * @returns          Markdown with `<div data-type="agentQuestion" …>`
 *                   markers appended inside detected list items, and
 *                   `<div data-type="agentCommandBlock" …>` markers after
 *                   ack-gated code blocks.
 */
export function preprocessAgentActions(markdown: string, isAgent: boolean): string {
  if (!isAgent || !markdown) return markdown;
  const { questions, commands } = walkAgentActions(markdown);
  if (questions.length === 0 && commands.length === 0) return markdown;

  // Build a map of line-index → markers-to-insert-after. Multiple markers
  // on the same line concatenate in detection order.
  const insertions = new Map<number, string[]>();
  for (const q of questions) {
    const marker = `\n${q.itemIndent}${questionMarker(q)}`;
    const list = insertions.get(q.insertAfterLine) ?? [];
    list.push(marker);
    insertions.set(q.insertAfterLine, list);
  }
  for (const c of commands) {
    const marker = `\n${commandMarker(c)}`;
    const list = insertions.get(c.insertAfterLine) ?? [];
    list.push(marker);
    insertions.set(c.insertAfterLine, list);
  }

  const lines = markdown.split("\n");
  const out: string[] = [];
  for (let i = 0; i < lines.length; i++) {
    out.push(lines[i]!);
    const markers = insertions.get(i);
    if (markers) for (const m of markers) out.push(m);
  }
  return out.join("\n");
}

/**
 * Typed extraction of agent actions from a single comment — used by
 * Mission Control (PR2) to render chip rows for the latest agent
 * comment per issue without going through markdown rendering.
 *
 * `parentCommentId` is required to populate `AgentQuestion.id` /
 * `AgentCommand.id` (the chip component uses these for stable React
 * keys and as the threaded-reply target).
 */
export function extractAgentActions(
  markdown: string,
  parentCommentId: string,
): AgentActions {
  if (!markdown) return { questions: [], commands: [] };
  const { questions, commands } = walkAgentActions(markdown);
  const typedQuestions: AgentQuestion[] = questions.map((q) => ({
    id: `${parentCommentId}:${q.ordinal}`,
    ordinal: q.ordinal,
    title: q.title,
    tail: q.tail,
    default: q.default,
    variants: q.variants,
    parentCommentId,
  }));
  const typedCommands: AgentCommand[] = commands.map((c) => ({
    id: `${parentCommentId}:cmd-${c.ordinal}`,
    ordinal: c.ordinal,
    commands: c.commands,
    groupLabel: c.groupLabel,
    parentCommentId,
  }));
  return { questions: typedQuestions, commands: typedCommands };
}
