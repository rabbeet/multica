/**
 * Inline answer-chip + ack-button primitives for agent-authored comments.
 *
 * Mounted by `readonly-content.tsx`'s `components.div` override when it
 * encounters `<div data-type="agentQuestion">` / `<div data-type="agentCommandBlock">`
 * markers injected by `preprocess-agent-actions.ts`. Both primitives:
 *
 *   - Read `commentId` / `issueId` from `useAuthorContext()` (the
 *     enclosing agent comment provides them via
 *     `<AuthorContextProvider/>` in `comment-card.tsx`).
 *   - Post the user's choice as a threaded reply via `useCreateComment`
 *     (uses `parent_id = commentId`). The hook handles timeline cache
 *     invalidation + WS sync.
 *   - Wrap their interactive surface in `onClick={stopPropagation}` so
 *     clicks never bubble up to a parent row/card and trigger
 *     unintended navigation (Mission Control row in PR2 dispatches a
 *     drill-down on row body tap).
 *   - Drive the state machine described in the plan (revision 4 —
 *     §State matrix): idle → pressing → pending → success | error |
 *     offline. The pressing / pending / success morph is all CSS — see
 *     keyframes wired through `apps/web/app/globals.css`.
 *
 * Both components are pure UI plus a single mutation; there is no
 * subscription to other state so they're cheap to mount many times
 * (PUL-222-style agent comments routinely carry 3+ questions).
 */

"use client";

import { useCallback, useRef, useState, useEffect, useId, type KeyboardEvent, type MouseEvent } from "react";
import { Check, Loader2, RefreshCw, WifiOff } from "lucide-react";
import { toast } from "sonner";
import { Button } from "@multica/ui/components/ui/button";
import { cn } from "@multica/ui/lib/utils";
import { useCreateComment } from "@multica/core/issues/mutations";
import {
  useOfflineCommentQueue,
  installOfflineCommentDrain,
  type QueuedComment,
} from "@multica/core/issues/stores";
import { api } from "@multica/core/api";
import { useAuthorContext } from "../../editor/context/author-context";
import { formatAnsweredContent } from "../../editor/utils/answer-markers";
import { useT } from "../../i18n";

/**
 * One-shot install of the offline-queue drainer. The first chip that
 * mounts on the page calls this and the listener stays attached for the
 * tab's lifetime; subsequent calls are no-ops thanks to the module-level
 * guard in `installOfflineCommentDrain`.
 *
 * Lives here in PR1 because chips are the only producers of queued
 * comments. PR2's Mission Control will host an explicit app-level
 * provider and this hook can be removed in favour of that mount.
 */
function useOfflineDrainOnce(): void {
  useEffect(() => {
    installOfflineCommentDrain(async (item: QueuedComment) => {
      await api.createComment(item.issueId, item.content, "comment", item.parentId);
    });
  }, []);
}

// ---------------------------------------------------------------------------
// State machine
// ---------------------------------------------------------------------------

type ChipState =
  | { kind: "idle" }
  | { kind: "pending"; variant: string }
  | { kind: "success"; variant: string }
  | { kind: "error"; variant: string }
  | { kind: "offline"; variant: string };

// ---------------------------------------------------------------------------
// Custom-answer inline input
// ---------------------------------------------------------------------------

function CustomAnswerInput({
  onSubmit,
  onCancel,
  ariaLabel,
}: {
  onSubmit: (text: string) => void;
  onCancel: () => void;
  ariaLabel: string;
}) {
  const { t } = useT("issues");
  const [text, setText] = useState("");
  const inputRef = useRef<HTMLInputElement>(null);

  useEffect(() => {
    inputRef.current?.focus();
  }, []);

  const submit = useCallback(() => {
    const trimmed = text.trim();
    if (trimmed) onSubmit(trimmed);
  }, [text, onSubmit]);

  return (
    <div className="mt-1.5 flex items-center gap-1.5">
      <input
        ref={inputRef}
        type="text"
        value={text}
        onChange={(e) => setText(e.target.value)}
        onKeyDown={(e) => {
          if (e.key === "Enter") {
            e.preventDefault();
            submit();
          } else if (e.key === "Escape") {
            onCancel();
          }
        }}
        placeholder={t(($) => $.agent_action.custom_answer_placeholder)}
        aria-label={ariaLabel}
        className="h-7 flex-1 min-w-0 rounded-md border border-input bg-background px-2 text-sm outline-none focus-visible:border-ring focus-visible:ring-2 focus-visible:ring-ring/30"
      />
      <Button size="sm" variant="default" onClick={submit} disabled={!text.trim()}>
        {t(($) => $.agent_action.send)}
      </Button>
      <Button size="sm" variant="ghost" onClick={onCancel}>
        {t(($) => $.agent_action.cancel)}
      </Button>
    </div>
  );
}

// ---------------------------------------------------------------------------
// Shared keyboard navigation for chip rows
// ---------------------------------------------------------------------------

function focusSibling(
  container: HTMLElement | null,
  current: HTMLElement,
  direction: 1 | -1,
) {
  if (!container) return;
  const focusable = Array.from(
    container.querySelectorAll<HTMLElement>('[data-chip-focusable="true"]'),
  );
  const idx = focusable.indexOf(current);
  if (idx === -1) return;
  const next = focusable[(idx + direction + focusable.length) % focusable.length];
  next?.focus();
}

function handleChipKeyDown(
  e: KeyboardEvent<HTMLButtonElement>,
  containerRef: React.RefObject<HTMLDivElement | null>,
) {
  if (e.key === "ArrowRight" || e.key === "ArrowDown") {
    e.preventDefault();
    focusSibling(containerRef.current, e.currentTarget, 1);
  } else if (e.key === "ArrowLeft" || e.key === "ArrowUp") {
    e.preventDefault();
    focusSibling(containerRef.current, e.currentTarget, -1);
  }
}

// ---------------------------------------------------------------------------
// <AgentQuestionChips/>
// ---------------------------------------------------------------------------

export interface AgentQuestionChipsProps {
  ordinal: number;
  title: string;
  defaultVariant: string | null;
  variants: string[];
}

export function AgentQuestionChips({
  ordinal,
  title,
  defaultVariant,
  variants,
}: AgentQuestionChipsProps) {
  const { t } = useT("issues");
  const { commentId, issueId } = useAuthorContext();
  const [state, setState] = useState<ChipState>({ kind: "idle" });
  const [customMode, setCustomMode] = useState(false);
  const containerRef = useRef<HTMLDivElement>(null);
  const groupAriaId = useId();

  // No issueId / commentId → renders disabled (e.g. mounted in a
  // preview/storybook context). Better than crashing the whole markdown.
  const canMutate = !!issueId && !!commentId;

  const createComment = useCreateComment(issueId ?? "");
  const enqueueOffline = useOfflineCommentQueue((s) => s.enqueue);
  useOfflineDrainOnce();

  // Double-tap guard: while any chip is pending, all chips in this
  // group should reject taps. Otherwise the user can rapid-tap chip A
  // then chip B in the morph window and end up with two threaded
  // replies — the agent would then have to decide which one to take.
  const isAnyPending = state.kind === "pending";

  const submit = useCallback(
    (variant: string) => {
      if (!canMutate || !commentId || !issueId) return;
      if (state.kind === "pending") return; // belt-and-braces against rapid taps
      // PUL-240 — prefix the reply content with a hidden answer marker
      // so the TL;DR header + Mission Control row badge can compute
      // per-question open-counts precisely. Question id = parent
      // comment id + the detected question's ordinal.
      const questionId = `${commentId}:${ordinal}`;
      const content = formatAnsweredContent(questionId, variant);
      if (typeof navigator !== "undefined" && navigator.onLine === false) {
        enqueueOffline({ issueId, content, parentId: commentId });
        setState({ kind: "offline", variant });
        return;
      }
      setState({ kind: "pending", variant });
      createComment.mutate(
        { content, parentId: commentId, type: "comment" },
        {
          onSuccess: () => setState({ kind: "success", variant }),
          onError: () => {
            setState({ kind: "error", variant });
            toast.error(t(($) => $.agent_action.send_failed));
          },
        },
      );
    },
    [canMutate, commentId, issueId, ordinal, createComment, enqueueOffline, t, state.kind],
  );

  // Success state: collapsed to a single inline badge.
  if (state.kind === "success") {
    return (
      <div
        data-agent-action="answered"
        className="agent-chip-row mt-1.5 inline-flex items-center gap-1.5 rounded-md bg-brand/10 px-2 py-0.5 text-xs text-foreground"
        aria-live="polite"
      >
        <Check className="size-3.5 text-brand" aria-hidden />
        <span>{t(($) => $.agent_action.answered, { variant: state.variant })}</span>
      </div>
    );
  }

  return (
    <div
      ref={containerRef}
      role="group"
      aria-labelledby={groupAriaId}
      data-agent-action="question"
      className="agent-chip-row mt-1.5 flex flex-wrap items-center gap-1.5"
      // PUL-231 / eng-review M2 fix: chip-zone never bubbles taps to the
      // surrounding row (Mission Control PR2 dispatches drill-down on
      // outer-row tap; without this the user would mistap a chip and
      // get navigated away mid-answer).
      onClick={(e: MouseEvent) => e.stopPropagation()}
    >
      <span id={groupAriaId} className="sr-only">
        {/* i18next selector-API typing on multi-placeholder keys rejects
            inline object literals here in some configurations — passing
            a pre-built record sidesteps the overload resolution issue and
            keeps the rendered string identical. */}
        {(() => {
          const params: Record<string, string> = { ordinal: String(ordinal), title };
          return t(($) => $.agent_action.aria_question_group, params);
        })()}
      </span>

      {variants.map((variant) => {
        const isDefault = variant === defaultVariant;
        // Each chip projects its own slice of the row-level state — only
        // the variant the user actually tapped renders the loading / error
        // affordance, even though the disabled state covers all chips.
        const isPending = state.kind === "pending" && state.variant === variant;
        const isError = state.kind === "error" && state.variant === variant;
        const isOffline = state.kind === "offline" && state.variant === variant;
        return (
          <Button
            key={variant}
            data-chip-focusable="true"
            data-state={
              isPending ? "pending" : isError ? "error" : isOffline ? "offline" : "idle"
            }
            variant={isDefault ? "default" : "outline"}
            size="sm"
            disabled={!canMutate || isAnyPending}
            onClick={() => submit(variant)}
            onKeyDown={(e) => handleChipKeyDown(e, containerRef)}
            aria-label={t(($) => $.agent_action.aria_select_variant, { variant })}
            className={cn(
              "agent-chip",
              isError && "ring-2 ring-destructive/40",
              isOffline && "opacity-70",
            )}
          >
            {isPending ? (
              <Loader2 className="size-3 animate-spin" aria-hidden />
            ) : isError ? (
              <RefreshCw className="size-3" aria-hidden />
            ) : isOffline ? (
              <WifiOff className="size-3" aria-hidden />
            ) : null}
            <span>{variant}</span>
            {isDefault && (
              <span className="ml-1 inline-flex items-center gap-0.5 text-[10px] uppercase tracking-wide opacity-70">
                <Check className="size-2.5" aria-hidden />
                {t(($) => $.agent_action.default_suffix)}
              </span>
            )}
          </Button>
        );
      })}

      {!customMode ? (
        <Button
          data-chip-focusable="true"
          variant="ghost"
          size="sm"
          disabled={!canMutate || isAnyPending}
          onClick={() => setCustomMode(true)}
          onKeyDown={(e) => handleChipKeyDown(e, containerRef)}
          aria-label={t(($) => $.agent_action.aria_custom_answer)}
          className="agent-chip"
        >
          {t(($) => $.agent_action.custom_answer)}
        </Button>
      ) : (
        <CustomAnswerInput
          onSubmit={(text) => {
            setCustomMode(false);
            submit(text);
          }}
          onCancel={() => setCustomMode(false)}
          ariaLabel={t(($) => $.agent_action.aria_custom_answer)}
        />
      )}
    </div>
  );
}

// ---------------------------------------------------------------------------
// <AgentCommandButton/>
// ---------------------------------------------------------------------------

export interface AgentCommandButtonProps {
  /** Ordinal of the command block within the source comment — currently
   *  unused at render time (single button) but threaded through so
   *  Mission Control (PR2) can stable-key rows by this value. */
  ordinal: number;
  commands: string[];
  groupLabel: string;
}

/**
 * Canonical reply token for ack-gated command blocks. Matches the agent
 * pickup expectation (see Pulse `RefreshEndpoints` cmd convention) — a
 * single-word "go" signal in the threaded reply is enough to authorise
 * the run.
 */
const COMMAND_ACK_TOKEN = "запусти";

export function AgentCommandButton({
  ordinal: _ordinal,
  commands,
  groupLabel,
}: AgentCommandButtonProps) {
  const { t } = useT("issues");
  const { commentId, issueId } = useAuthorContext();
  const [state, setState] = useState<ChipState>({ kind: "idle" });
  const containerRef = useRef<HTMLDivElement>(null);
  const groupAriaId = useId();

  const canMutate = !!issueId && !!commentId;
  const createComment = useCreateComment(issueId ?? "");
  const enqueueOffline = useOfflineCommentQueue((s) => s.enqueue);
  useOfflineDrainOnce();

  const submit = useCallback(() => {
    if (!canMutate || !commentId || !issueId) return;
    if (state.kind === "pending") return;
    const content = COMMAND_ACK_TOKEN;
    if (typeof navigator !== "undefined" && navigator.onLine === false) {
      enqueueOffline({ issueId, content, parentId: commentId });
      setState({ kind: "offline", variant: content });
      return;
    }
    setState({ kind: "pending", variant: content });
    createComment.mutate(
      { content, parentId: commentId, type: "comment" },
      {
        onSuccess: () => setState({ kind: "success", variant: content }),
        onError: () => {
          setState({ kind: "error", variant: content });
          toast.error(t(($) => $.agent_action.send_failed));
        },
      },
    );
  }, [canMutate, commentId, issueId, createComment, enqueueOffline, t, state.kind]);

  if (state.kind === "success") {
    return (
      <div
        data-agent-action="ack-sent"
        className="agent-chip-row mt-1.5 inline-flex items-center gap-1.5 rounded-md bg-brand/10 px-2 py-0.5 text-xs text-foreground"
        aria-live="polite"
      >
        <Check className="size-3.5 text-brand" aria-hidden />
        <span>{t(($) => $.agent_action.answered, { variant: COMMAND_ACK_TOKEN })}</span>
      </div>
    );
  }

  const isPending = state.kind === "pending";
  const isError = state.kind === "error";
  const isOffline = state.kind === "offline";

  const buttonLabel =
    commands.length === 1
      ? t(($) => $.agent_action.run_single, { command: commands[0]! })
      : t(($) => $.agent_action.run_all, { count: commands.length });

  return (
    <div
      ref={containerRef}
      role="group"
      aria-labelledby={groupAriaId}
      data-agent-action="command"
      className="agent-chip-row mt-1.5 flex items-center gap-2"
      onClick={(e: MouseEvent) => e.stopPropagation()}
    >
      <span id={groupAriaId} className="sr-only">
        {t(($) => $.agent_action.aria_command_group)}
        {groupLabel ? ` — ${groupLabel}` : ""}
      </span>
      <Button
        data-chip-focusable="true"
        data-state={isPending ? "pending" : isError ? "error" : isOffline ? "offline" : "idle"}
        variant="default"
        size="sm"
        disabled={!canMutate || isPending}
        onClick={submit}
        aria-label={t(($) => $.agent_action.aria_run_commands, { count: commands.length })}
        className={cn(
          "agent-chip",
          isError && "ring-2 ring-destructive/40",
          isOffline && "opacity-70",
        )}
      >
        {isPending ? (
          <Loader2 className="size-3.5 animate-spin" aria-hidden />
        ) : isError ? (
          <RefreshCw className="size-3.5" aria-hidden />
        ) : isOffline ? (
          <WifiOff className="size-3.5" aria-hidden />
        ) : null}
        <span>{buttonLabel}</span>
      </Button>
    </div>
  );
}
