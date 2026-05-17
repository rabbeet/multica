// Tracks the last time the current user picked a given skill (via the `/`
// autocomplete dropdown or the popular-skills bar), per workspace, in browser
// storage. Used to rank skills so recently-used ones surface first in both
// surfaces.
//
// Mirrors the shape of `mention-recency.ts` rather than reusing a generic
// store. Per the eng-review (D1=A): mention-recency stays untouched, this file
// is a standalone copy — rule of three has not been met yet (only two recency
// trackers). When a third one appears, factor out then.
//
// Data is per-device by design — the goal is "make the next skill faster",
// not a cross-device profile. If localStorage is unavailable (SSR, sandboxed
// environments) every accessor degrades to a no-op so callers can use it
// unconditionally.

type RecencyMap = Record<string, number>;

const STORAGE_PREFIX = "multica:skill-recency:";
const MAX_ENTRIES = 100;

function storageKey(workspaceId: string): string {
  return `${STORAGE_PREFIX}${workspaceId}`;
}

function getStorage(): Storage | null {
  if (typeof window === "undefined") return null;
  try {
    return window.localStorage;
  } catch {
    return null;
  }
}

function readRecencyMap(workspaceId: string): RecencyMap {
  const storage = getStorage();
  if (!storage) return {};
  const raw = storage.getItem(storageKey(workspaceId));
  if (!raw) return {};
  try {
    const parsed = JSON.parse(raw);
    if (parsed && typeof parsed === "object") return parsed as RecencyMap;
  } catch {
    // Corrupt entry — drop it on the next write rather than throwing.
  }
  return {};
}

function writeRecencyMap(workspaceId: string, map: RecencyMap): void {
  const storage = getStorage();
  if (!storage) return;
  try {
    storage.setItem(storageKey(workspaceId), JSON.stringify(map));
  } catch {
    // Quota exceeded or storage disabled — silently skip.
  }
}

export function recordSkillUsage(workspaceId: string, skillId: string): void {
  if (!workspaceId || !skillId) return;
  const map = readRecencyMap(workspaceId);
  map[skillId] = Date.now();

  // Lazy prune: keep the map bounded so it doesn't grow forever as skills
  // come and go.
  const entries = Object.entries(map);
  if (entries.length > MAX_ENTRIES) {
    entries.sort(([, ta], [, tb]) => tb - ta);
    const trimmed: RecencyMap = {};
    for (const [key, ts] of entries.slice(0, MAX_ENTRIES)) {
      trimmed[key] = ts;
    }
    writeRecencyMap(workspaceId, trimmed);
    return;
  }

  writeRecencyMap(workspaceId, map);
}

export function getSkillRecencyMap(workspaceId: string): RecencyMap {
  if (!workspaceId) return {};
  return readRecencyMap(workspaceId);
}

// Sorts skill items by recency DESC, with an alphabetical name fallback for
// items the user has never picked.
export function sortSkillsByRecency<T extends { id: string; name: string }>(
  items: T[],
  recency: RecencyMap,
): T[] {
  return [...items].sort((a, b) => {
    const ra = recency[a.id] ?? 0;
    const rb = recency[b.id] ?? 0;
    if (ra !== rb) return rb - ra;
    return a.name.localeCompare(b.name);
  });
}

// Minimal editor interface needed for skill insertion. Kept narrow to avoid
// a cyclic import with ContentEditorRef (which lives one directory up and
// itself loads extensions including this one).
export interface SkillInsertableEditor {
  focus: () => void;
  insertAtCursor: (text: string) => void;
}

/**
 * The single side-effect path for "user picked a skill" — used from both the
 * `/` autocomplete `command` callback and the popular-skills-bar `onPick`
 * handler. Focuses the editor, inserts `/<name> ` as plain text at the cursor,
 * and records the pick for next-time ranking. No-op if `editor` is null.
 */
export function insertSkillAndRecord(
  editor: SkillInsertableEditor | null,
  workspaceId: string,
  skill: { id: string; name: string },
): void {
  if (!editor) return;
  editor.focus();
  editor.insertAtCursor(`/${skill.name} `);
  recordSkillUsage(workspaceId, skill.id);
}
