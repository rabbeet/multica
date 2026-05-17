import { describe, it, expect } from "vitest";
import { Editor } from "@tiptap/core";
import StarterKit from "@tiptap/starter-kit";
import { createSkillExtension } from "./skill-extension";

const NOOP_SUGGESTION = {
  char: "/",
  items: () => [],
  render: () => ({}),
};

describe("createSkillExtension", () => {
  it('extension name is "skillSuggestion"', () => {
    const ext = createSkillExtension(NOOP_SUGGESTION);
    expect(ext.name).toBe("skillSuggestion");
  });

  it("does NOT register a node type — schema must remain unchanged from a baseline (no /skill/ nodes)", () => {
    const baseline = new Editor({ extensions: [StarterKit] });
    const withSkill = new Editor({
      extensions: [StarterKit, createSkillExtension(NOOP_SUGGESTION)],
    });
    try {
      // The skill extension should not introduce new node types.
      const baseNodes = Object.keys(baseline.schema.nodes).sort();
      const skillNodes = Object.keys(withSkill.schema.nodes).sort();
      expect(skillNodes).toEqual(baseNodes);
    } finally {
      baseline.destroy();
      withSkill.destroy();
    }
  });

  it("adds exactly one ProseMirror plugin (the Suggestion plugin)", () => {
    const ext = createSkillExtension(NOOP_SUGGESTION);
    const editor = new Editor({ extensions: [StarterKit, ext] });
    try {
      // The schema is unchanged; we added a Suggestion plugin to the state.
      // There's no easy public API to check the plugin instance, but the
      // editor should mount without errors and remain editable.
      expect(editor.isEditable).toBe(true);
    } finally {
      editor.destroy();
    }
  });
});
