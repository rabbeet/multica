import { fireEvent, render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it, vi } from "vitest";
import { I18nextProvider } from "react-i18next";
import { createInstance } from "i18next";
import type { ReactElement } from "react";
import type { Label } from "@multica/core/types";
import { LabelQuickFilters } from "./label-quick-filters";
import { RESOURCES } from "../../locales";

function label(id: string, name: string, color: string): Label {
  return {
    id,
    workspace_id: "workspace-1",
    name,
    color,
    created_at: "2026-08-03T00:00:00Z",
    updated_at: "2026-08-03T00:00:00Z",
  };
}

const labels = [
  label("klavdiya", "Клавдия", "#7c3aed"),
  label("telegram", "Источник: Telegram", "#229ed9"),
  label("vadim", "Автор: Vadim Zaytsev", "#64748b"),
];

function renderQuickFilters(ui: ReactElement) {
  const i18n = createInstance();
  i18n.init({
    lng: "en",
    fallbackLng: "en",
    interpolation: { escapeValue: false },
    resources: { en: RESOURCES.en },
  });
  return render(<I18nextProvider i18n={i18n}>{ui}</I18nextProvider>);
}

describe("LabelQuickFilters", () => {
  it("shows selected labels as removable filter chips", () => {
    const onToggle = vi.fn();

    renderQuickFilters(
      <LabelQuickFilters
        labels={labels}
        selected={["klavdiya", "telegram"]}
        onToggle={onToggle}
        onClear={vi.fn()}
      />,
    );

    expect(
      screen.getByRole("toolbar", { name: "Label filters" }),
    ).toBeInTheDocument();
    expect(screen.getByText("Клавдия")).toBeInTheDocument();
    expect(screen.getByText("Источник: Telegram")).toBeInTheDocument();

    fireEvent.click(
      screen.getByRole("button", { name: "Remove Клавдия filter" }),
    );
    expect(onToggle).toHaveBeenCalledWith("klavdiya");
  });

  it("opens a label picker and toggles an unselected label", () => {
    const onToggle = vi.fn();

    renderQuickFilters(
      <LabelQuickFilters
        labels={labels}
        selected={[]}
        onToggle={onToggle}
        onClear={vi.fn()}
      />,
    );

    fireEvent.click(screen.getByRole("button", { name: "Labels" }));
    fireEvent.click(
      screen.getByRole("menuitemcheckbox", { name: /Автор: Vadim Zaytsev/ }),
    );

    expect(onToggle).toHaveBeenCalledWith("vadim");
  });

  it("searches labels in the picker", () => {
    renderQuickFilters(
      <LabelQuickFilters
        labels={labels}
        selected={[]}
        onToggle={vi.fn()}
        onClear={vi.fn()}
      />,
    );

    fireEvent.click(screen.getByRole("button", { name: "Labels" }));
    fireEvent.change(screen.getByPlaceholderText("Filter..."), {
      target: { value: "telegram" },
    });

    expect(
      screen.getByRole("menuitemcheckbox", { name: /Источник: Telegram/ }),
    ).toBeInTheDocument();
    expect(
      screen.queryByRole("menuitemcheckbox", { name: /Автор: Vadim Zaytsev/ }),
    ).toBeNull();
  });

  it("closes on Escape from search and resets search before reopening", async () => {
    const user = userEvent.setup();
    renderQuickFilters(
      <LabelQuickFilters
        labels={labels}
        selected={[]}
        onToggle={vi.fn()}
        onClear={vi.fn()}
      />,
    );

    const trigger = screen.getByRole("button", { name: "Labels" });
    fireEvent.click(trigger);
    const search = screen.getByPlaceholderText("Filter...");
    fireEvent.change(search, { target: { value: "telegram" } });
    await user.keyboard("{Escape}");

    expect(screen.queryByPlaceholderText("Filter...")).toBeNull();

    fireEvent.click(trigger);
    expect(screen.getByPlaceholderText("Filter...")).toHaveValue("");
    expect(
      screen.getByRole("menuitemcheckbox", { name: /Автор: Vadim Zaytsev/ }),
    ).toBeInTheDocument();
    await user.keyboard("{Escape}");
  });

  it("gives the search input a localized accessible label", () => {
    renderQuickFilters(
      <LabelQuickFilters
        labels={labels}
        selected={[]}
        onToggle={vi.fn()}
        onClear={vi.fn()}
      />,
    );

    fireEvent.click(screen.getByRole("button", { name: "Labels" }));

    expect(
      screen.getByRole("textbox", { name: "Search labels" }),
    ).toBeInTheDocument();
  });

  it("clears all selected label filters from the toolbar", () => {
    const onClear = vi.fn();

    renderQuickFilters(
      <LabelQuickFilters
        labels={labels}
        selected={["klavdiya", "telegram", "vadim"]}
        onToggle={vi.fn()}
        onClear={onClear}
      />,
    );

    fireEvent.click(
      screen.getByRole("button", { name: "Clear label filters" }),
    );
    expect(onClear).toHaveBeenCalledOnce();
  });

  it("can clear a stale selected label that no longer exists", () => {
    const onClear = vi.fn();

    renderQuickFilters(
      <LabelQuickFilters
        labels={[]}
        selected={["deleted-label"]}
        onToggle={vi.fn()}
        onClear={onClear}
      />,
    );

    fireEvent.click(
      screen.getByRole("button", { name: "Clear label filters" }),
    );
    expect(onClear).toHaveBeenCalledOnce();
  });
});
