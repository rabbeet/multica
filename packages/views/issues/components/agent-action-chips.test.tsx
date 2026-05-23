import { describe, it, expect, vi, beforeEach } from "vitest";
import { render, fireEvent, waitFor, act } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { I18nextProvider } from "react-i18next";
import { createInstance } from "i18next";
import { AuthorContextProvider } from "../../editor/context/author-context";
import { AgentQuestionChips, AgentCommandButton } from "./agent-action-chips";
import { useOfflineCommentQueue } from "@multica/core/issues/stores";
import { RESOURCES } from "../../locales";

// `useCreateComment` is the canonical mutation hook the chips fire (see
// eng-review M3 — we must NOT call api.createComment directly). The mock
// captures invocations so each test can assert the (content, parentId,
// type) tuple matches the chip the user tapped.
const createCommentMock = vi.fn().mockResolvedValue({
  id: "reply-id",
  parent_id: "parent-comment-id",
});
vi.mock("@multica/core/issues/mutations", () => ({
  useCreateComment: () => ({
    mutate: (
      vars: { content: string; type?: string; parentId?: string },
      opts?: { onSuccess?: (c: unknown) => void; onError?: (e: unknown) => void },
    ) => {
      Promise.resolve(createCommentMock(vars)).then(
        (resp) => opts?.onSuccess?.(resp),
        (err) => opts?.onError?.(err),
      );
    },
    mutateAsync: createCommentMock,
  }),
}));

vi.mock("sonner", () => ({ toast: { error: vi.fn(), success: vi.fn() } }));

function renderWithProviders(
  ui: React.ReactNode,
  ctx: { isAgent: boolean; commentId: string | null; issueId: string | null } = {
    isAgent: true,
    commentId: "parent-comment-id",
    issueId: "issue-1",
  },
) {
  const i18n = createInstance();
  i18n.init({
    lng: "en",
    fallbackLng: "en",
    interpolation: { escapeValue: false },
    resources: { en: RESOURCES.en },
  });
  const qc = new QueryClient({
    defaultOptions: { queries: { retry: false, gcTime: 0 } },
  });
  return render(
    <QueryClientProvider client={qc}>
      <I18nextProvider i18n={i18n}>
        <AuthorContextProvider value={ctx}>{ui}</AuthorContextProvider>
      </I18nextProvider>
    </QueryClientProvider>,
  );
}

beforeEach(() => {
  createCommentMock.mockClear();
  createCommentMock.mockResolvedValue({ id: "reply-id", parent_id: "parent-comment-id" });
  useOfflineCommentQueue.getState()._reset();
  // Default to online in tests; offline tests flip it explicitly.
  Object.defineProperty(global.navigator, "onLine", {
    configurable: true,
    value: true,
  });
});

// ---------------------------------------------------------------------------
// <AgentQuestionChips/>
// ---------------------------------------------------------------------------

describe("AgentQuestionChips", () => {
  const baseProps = {
    ordinal: 1,
    title: "URL name",
    defaultVariant: "rbtd_bg_sym",
    variants: ["rbtd_bg_sym", "rbtd_bg", "rbtd_anchor"],
  };

  it("renders one button per variant plus the custom-answer chip", () => {
    const { getByLabelText, getByText } = renderWithProviders(
      <AgentQuestionChips {...baseProps} />,
    );
    expect(getByLabelText("Select rbtd_bg_sym")).toBeTruthy();
    expect(getByLabelText("Select rbtd_bg")).toBeTruthy();
    expect(getByLabelText("Select rbtd_anchor")).toBeTruthy();
    expect(getByText("custom answer…")).toBeTruthy();
  });

  it("flags the default variant with a 'default' suffix", () => {
    const { getByLabelText } = renderWithProviders(
      <AgentQuestionChips {...baseProps} />,
    );
    const defaultBtn = getByLabelText("Select rbtd_bg_sym");
    expect(defaultBtn.textContent).toContain("default");
  });

  it("calls useCreateComment with the tapped variant + parent_id", async () => {
    const { getByLabelText } = renderWithProviders(
      <AgentQuestionChips {...baseProps} />,
    );
    fireEvent.click(getByLabelText("Select rbtd_bg"));
    await waitFor(() => {
      expect(createCommentMock).toHaveBeenCalledWith({
        content: "rbtd_bg",
        parentId: "parent-comment-id",
        type: "comment",
      });
    });
  });

  it("morphs to the 'answered' badge after a successful reply", async () => {
    const { getByLabelText, findByText } = renderWithProviders(
      <AgentQuestionChips {...baseProps} />,
    );
    fireEvent.click(getByLabelText("Select rbtd_anchor"));
    expect(await findByText(/answered: rbtd_anchor/i)).toBeTruthy();
  });

  it("stops chip-zone clicks from bubbling to a parent row tap handler", () => {
    const outerClick = vi.fn();
    const { getByLabelText } = render(
      <QueryClientProvider client={new QueryClient()}>
        <I18nextProvider
          i18n={(() => {
            const i18n = createInstance();
            i18n.init({ lng: "en", resources: { en: RESOURCES.en } });
            return i18n;
          })()}
        >
          <AuthorContextProvider
            value={{ isAgent: true, commentId: "p1", issueId: "i1" }}
          >
            <div onClick={outerClick}>
              <AgentQuestionChips {...baseProps} />
            </div>
          </AuthorContextProvider>
        </I18nextProvider>
      </QueryClientProvider>,
    );
    fireEvent.click(getByLabelText("Select rbtd_bg"));
    expect(outerClick).not.toHaveBeenCalled();
  });

  it("ArrowRight focuses the next chip (keyboard navigation)", async () => {
    const user = userEvent.setup();
    const { getByLabelText } = renderWithProviders(
      <AgentQuestionChips {...baseProps} />,
    );
    const first = getByLabelText("Select rbtd_bg_sym") as HTMLButtonElement;
    first.focus();
    expect(document.activeElement).toBe(first);
    await user.keyboard("{ArrowRight}");
    expect(document.activeElement).toBe(getByLabelText("Select rbtd_bg"));
  });

  it("when offline, enqueues the intent and renders the offline state", async () => {
    Object.defineProperty(global.navigator, "onLine", { configurable: true, value: false });
    const { getByLabelText, container } = renderWithProviders(
      <AgentQuestionChips {...baseProps} />,
    );
    fireEvent.click(getByLabelText("Select rbtd_bg"));
    // API NOT called when offline.
    expect(createCommentMock).not.toHaveBeenCalled();
    // Queue holds one entry.
    expect(useOfflineCommentQueue.getState().pending).toHaveLength(1);
    // Chip shows the offline state attribute, queried via the data-state hook.
    expect(container.querySelector('[data-state="offline"]')).toBeTruthy();
  });

  it("custom-answer chip opens an input and submits typed text as content", async () => {
    const user = userEvent.setup();
    const { getByText, getByPlaceholderText } = renderWithProviders(
      <AgentQuestionChips {...baseProps} />,
    );
    fireEvent.click(getByText("custom answer…"));
    const input = getByPlaceholderText("Type your answer…") as HTMLInputElement;
    await user.type(input, "своя реплика");
    fireEvent.click(getByText("Send"));
    await waitFor(() => {
      expect(createCommentMock).toHaveBeenCalledWith({
        content: "своя реплика",
        parentId: "parent-comment-id",
        type: "comment",
      });
    });
  });

  it("renders chips disabled when AuthorContext lacks issueId/commentId", () => {
    const { getByLabelText } = renderWithProviders(<AgentQuestionChips {...baseProps} />, {
      isAgent: true,
      commentId: null,
      issueId: null,
    });
    const btn = getByLabelText("Select rbtd_bg") as HTMLButtonElement;
    expect(btn.disabled).toBe(true);
  });
});

// ---------------------------------------------------------------------------
// <AgentCommandButton/>
// ---------------------------------------------------------------------------

describe("AgentCommandButton", () => {
  it("renders 'Run all N' for multi-command blocks", () => {
    const { getByText } = renderWithProviders(
      <AgentCommandButton
        ordinal={1}
        commands={["a", "b", "c", "d"]}
        groupLabel="Recovery"
      />,
    );
    expect(getByText(/Run all 4/i)).toBeTruthy();
  });

  it("renders 'Run: <cmd>' for a single-command block", () => {
    const { getByText } = renderWithProviders(
      <AgentCommandButton ordinal={1} commands={["php artisan refresh"]} groupLabel="" />,
    );
    expect(getByText(/Run: php artisan refresh/)).toBeTruthy();
  });

  it("posts the canonical 'запусти' token on tap", async () => {
    const { getByText } = renderWithProviders(
      <AgentCommandButton ordinal={1} commands={["a", "b"]} groupLabel="Recovery" />,
    );
    fireEvent.click(getByText(/Run all 2/i));
    await waitFor(() => {
      expect(createCommentMock).toHaveBeenCalledWith({
        content: "запусти",
        parentId: "parent-comment-id",
        type: "comment",
      });
    });
  });

  it("morphs to 'ack-sent' state after the reply lands", async () => {
    const { getByText, findByText } = renderWithProviders(
      <AgentCommandButton ordinal={1} commands={["one"]} groupLabel="" />,
    );
    fireEvent.click(getByText(/Run: one/));
    expect(await findByText(/answered: запусти/i)).toBeTruthy();
  });
});

// ---------------------------------------------------------------------------
// State-attribute snapshot — anchors the state matrix from the plan
// ---------------------------------------------------------------------------

describe("data-state attribute (chip state matrix)", () => {
  it("emits data-state='idle' before any tap", () => {
    const { container } = renderWithProviders(
      <AgentQuestionChips
        ordinal={1}
        title="x"
        defaultVariant={null}
        variants={["a", "b"]}
      />,
    );
    const chips = container.querySelectorAll('[data-state="idle"]');
    expect(chips.length).toBeGreaterThan(0);
  });

  it("emits data-state='error' after a failed mutation", async () => {
    createCommentMock.mockRejectedValueOnce(new Error("boom"));
    const { container, getByLabelText } = renderWithProviders(
      <AgentQuestionChips
        ordinal={1}
        title="x"
        defaultVariant={null}
        variants={["a", "b"]}
      />,
    );
    fireEvent.click(getByLabelText("Select a"));
    await waitFor(() => {
      expect(container.querySelector('[data-state="error"]')).toBeTruthy();
    });
  });
});
