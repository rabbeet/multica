"use client";

import {
  forwardRef,
  useCallback,
  useEffect,
  useImperativeHandle,
  useMemo,
  useRef,
  useState,
} from "react";
import { ReactRenderer } from "@tiptap/react";
import { computePosition, offset, flip, shift } from "@floating-ui/dom";
import type { QueryClient } from "@tanstack/react-query";
import type { SuggestionOptions, SuggestionProps } from "@tiptap/suggestion";
import { getCurrentWsId } from "@multica/core/platform";
import { workspaceKeys } from "@multica/core/workspace/queries";
import { isImeComposing } from "@multica/core/utils";
import type { SkillSummary } from "@multica/core/types";
import { useT } from "../../i18n";
import {
  getSkillRecencyMap,
  recordSkillUsage,
  sortSkillsByRecency,
} from "./skill-recency";

// ---------------------------------------------------------------------------
// Types
// ---------------------------------------------------------------------------

export interface SkillItem {
  id: string;
  name: string;
  description?: string;
}

interface SkillListProps {
  items: SkillItem[];
  query: string;
  command: (item: SkillItem) => void;
}

export interface SkillListRef {
  onKeyDown: (props: { event: KeyboardEvent }) => boolean;
}

// ---------------------------------------------------------------------------
// SkillList — the popup rendered inside the editor
// ---------------------------------------------------------------------------

const MAX_ITEMS = 20;

export const SkillList = forwardRef<SkillListRef, SkillListProps>(
  function SkillList({ items, command }, ref) {
    const { t } = useT("editor");
    const [selectedIndex, setSelectedIndex] = useState(0);
    const itemRefs = useRef<(HTMLButtonElement | null)[]>([]);

    const displayItems = useMemo(() => items.slice(0, MAX_ITEMS), [items]);

    useEffect(() => {
      setSelectedIndex(0);
    }, [displayItems]);

    useEffect(() => {
      itemRefs.current[selectedIndex]?.scrollIntoView({ block: "nearest" });
    }, [selectedIndex]);

    const selectItem = useCallback(
      (index: number) => {
        const item = displayItems[index];
        if (!item) return;
        command(item);
      },
      [displayItems, command],
    );

    useImperativeHandle(ref, () => ({
      onKeyDown: ({ event }) => {
        // IME composing — don't intercept Enter/Arrow as picker actions;
        // those keys belong to the IME.
        if (isImeComposing(event)) return false;
        if (event.key === "ArrowUp") {
          if (displayItems.length === 0) return true;
          setSelectedIndex(
            (i) => (i + displayItems.length - 1) % displayItems.length,
          );
          return true;
        }
        if (event.key === "ArrowDown") {
          if (displayItems.length === 0) return true;
          setSelectedIndex((i) => (i + 1) % displayItems.length);
          return true;
        }
        if (event.key === "Enter") {
          if (displayItems.length === 0) return true;
          selectItem(selectedIndex);
          return true;
        }
        return false;
      },
    }));

    if (displayItems.length === 0) {
      return (
        <div className="rounded-md border bg-popover p-2 text-xs text-muted-foreground shadow-md">
          {t(($) => $.skill.no_results)}
        </div>
      );
    }

    return (
      <div className="rounded-md border bg-popover py-1 shadow-md w-72 max-h-[300px] overflow-y-auto">
        <div className="px-3 py-1.5 text-xs font-medium text-muted-foreground">
          {t(($) => $.skill.group_label)}
        </div>
        {displayItems.map((item, idx) => (
          <SkillRow
            key={item.id}
            item={item}
            selected={idx === selectedIndex}
            onSelect={() => selectItem(idx)}
            buttonRef={(el) => {
              itemRefs.current[idx] = el;
            }}
          />
        ))}
      </div>
    );
  },
);

// ---------------------------------------------------------------------------
// SkillRow — single item in the list
// ---------------------------------------------------------------------------

function SkillRow({
  item,
  selected,
  onSelect,
  buttonRef,
}: {
  item: SkillItem;
  selected: boolean;
  onSelect: () => void;
  buttonRef: (el: HTMLButtonElement | null) => void;
}) {
  return (
    <button
      ref={buttonRef}
      type="button"
      className={`flex w-full flex-col items-start gap-0.5 px-3 py-1.5 text-left text-xs transition-colors ${
        selected ? "bg-accent" : "hover:bg-accent/50"
      }`}
      onClick={onSelect}
    >
      <span className="font-medium">/{item.name}</span>
      {item.description ? (
        <span className="truncate text-muted-foreground w-full">
          {item.description}
        </span>
      ) : null}
    </button>
  );
}

// ---------------------------------------------------------------------------
// Suggestion config factory
// ---------------------------------------------------------------------------

function skillSummaryToItem(s: SkillSummary): SkillItem {
  return { id: s.id, name: s.name, description: s.description };
}

export function createSkillSuggestion(
  qc: QueryClient,
): Omit<SuggestionOptions<SkillItem>, "editor"> {
  // Renderer/popup instances live in this closure so each ContentEditor owns
  // its own TipTap suggestion popup lifecycle.
  let renderer: ReactRenderer<SkillListRef> | null = null;
  let popup: HTMLDivElement | null = null;

  function readSkills(query: string): SkillItem[] {
    const wsId = getCurrentWsId();
    if (!wsId) return [];

    const cached: SkillSummary[] =
      qc.getQueryData(workspaceKeys.skills(wsId)) ?? [];
    const q = query.toLowerCase();

    const filtered = cached
      .filter((s) => s.name.toLowerCase().includes(q))
      .map(skillSummaryToItem);

    const recency = getSkillRecencyMap(wsId);
    return sortSkillsByRecency(filtered, recency);
  }

  return {
    char: "/",

    items: ({ query }) => readSkills(query),

    command: ({ editor, range, props }) => {
      // CRITICAL invariant: insert as plain text, NOT a ProseMirror node.
      // Wrapping in a node would break the markdown roundtrip — the server
      // would see `<skill .../>` instead of the literal `/skill-name ` that
      // the agent expects. `insertContentAt(range, "<string>")` guarantees
      // text-only insertion.
      editor
        .chain()
        .focus()
        .insertContentAt(range, `/${props.name} `)
        .run();
      const wsId = getCurrentWsId();
      if (wsId) recordSkillUsage(wsId, props.id);
    },

    render: () => {
      return {
        onStart: (props: SuggestionProps<SkillItem>) => {
          renderer = new ReactRenderer(SkillList, {
            props: {
              items: props.items,
              query: props.query,
              command: props.command,
            },
            editor: props.editor,
          });

          popup = document.createElement("div");
          popup.style.position = "fixed";
          popup.style.zIndex = "50";
          popup.appendChild(renderer.element);
          document.body.appendChild(popup);

          updatePosition(popup, props.clientRect);
        },

        onUpdate: (props: SuggestionProps<SkillItem>) => {
          renderer?.updateProps({
            items: props.items,
            query: props.query,
            command: props.command,
          });
          if (popup) updatePosition(popup, props.clientRect);
        },

        onKeyDown: (props: { event: KeyboardEvent }) => {
          if (props.event.key === "Escape") {
            cleanup();
            return true;
          }
          return renderer?.ref?.onKeyDown(props) ?? false;
        },

        onExit: () => {
          cleanup();
        },
      };

      function updatePosition(
        el: HTMLDivElement,
        clientRect: (() => DOMRect | null) | null | undefined,
      ) {
        if (!clientRect) return;
        const virtualEl = {
          getBoundingClientRect: () => clientRect() ?? new DOMRect(),
        };
        computePosition(virtualEl, el, {
          placement: "bottom-start",
          strategy: "fixed",
          middleware: [offset(4), flip(), shift({ padding: 8 })],
        }).then(({ x, y }) => {
          el.style.left = `${x}px`;
          el.style.top = `${y}px`;
        });
      }

      function cleanup() {
        renderer?.destroy();
        renderer = null;
        popup?.remove();
        popup = null;
      }
    },
  };
}

