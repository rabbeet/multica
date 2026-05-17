import { Extension } from "@tiptap/core";
import Suggestion, { type SuggestionOptions } from "@tiptap/suggestion";
import type { SkillItem } from "./skill-suggestion";

/**
 * Tiptap Extension that registers a `/`-triggered Suggestion plugin for skill
 * autocomplete. **Does NOT declare a node type** — the chosen skill is inserted
 * as literal text by the suggestion's `command` callback, preserving the
 * markdown roundtrip (`/skill-name ` stays plain text from editor → markdown →
 * server → agent).
 *
 * Companion to `@`-mention extension, which is a Mention-node extension. Both
 * coexist in the editor extension array; their suggestion plugins are
 * independent (different `char` triggers, no shared state).
 */
export function createSkillExtension(
  suggestion: Omit<SuggestionOptions<SkillItem>, "editor">,
): Extension {
  return Extension.create({
    name: "skillSuggestion",
    addProseMirrorPlugins() {
      return [
        Suggestion<SkillItem>({
          editor: this.editor,
          ...suggestion,
        }),
      ];
    },
  });
}
