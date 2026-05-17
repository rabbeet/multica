// Step 14 regression test (PUL-161 eng-review D8=A).
//
// CRITICAL invariant: a `/skill-name ` literal that flows through the
// Tiptap editor must come out of `editor.getMarkdown()` byte-identical. If a
// future change wraps the skill in a node spec (e.g. a `Mention`-style
// extension with `type: "skill"`), this test fails. The agent on the other
// end of the comment expects plain text — anything else means the slash
// command stops working silently.
//
// We assert two paths:
//   (a) loading markdown that already contains `/skill-name ...` does NOT
//       transform it into anything node-like;
//   (b) the same content after a focus + insertContent round-trip is still
//       the same literal.

import { describe, it, expect, afterEach } from "vitest";
import { Editor } from "@tiptap/core";
import StarterKit from "@tiptap/starter-kit";
import { Markdown } from "@tiptap/markdown";

interface MarkdownEditor extends Editor {
  getMarkdown(): string;
}

function makeEditor(content: string): MarkdownEditor {
  const element = document.createElement("div");
  document.body.appendChild(element);
  return new Editor({
    element,
    extensions: [StarterKit, Markdown],
    content,
    contentType: "markdown",
  } as ConstructorParameters<typeof Editor>[0]) as MarkdownEditor;
}

let openEditor: MarkdownEditor | null = null;

afterEach(() => {
  openEditor?.destroy();
  openEditor = null;
});

describe("skill markdown roundtrip", () => {
  it("(a) loading `/plan-and-implement привет` returns it verbatim", () => {
    const input = "/plan-and-implement привет";
    openEditor = makeEditor(input);
    const out = openEditor.getMarkdown().trim();
    expect(out).toBe(input);
  });

  it("(b) inserting `/skill-name ` at the caret keeps it as plain text in markdown", () => {
    openEditor = makeEditor("");
    openEditor.chain().focus().insertContent("/plan-and-implement ").run();
    const out = openEditor.getMarkdown().trim();
    // Allow for trailing whitespace differences but the substring must be
    // present and not wrapped in any markdown link or HTML.
    expect(out).toContain("/plan-and-implement");
    expect(out).not.toMatch(/mention:\/\//);
    expect(out).not.toMatch(/<skill/);
    expect(out).not.toMatch(/\[\/plan-and-implement\]\(/);
  });

  it("(c) `/foo-bar` after other text remains plain text", () => {
    const input = "see /foo-bar для контекста";
    openEditor = makeEditor(input);
    const out = openEditor.getMarkdown().trim();
    expect(out).toBe(input);
  });
});
