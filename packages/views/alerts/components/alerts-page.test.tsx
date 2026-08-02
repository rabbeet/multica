import { screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it } from "vitest";
import { AlertsPage } from "./alerts-page";
import { previewAlertFixtures } from "../fixtures";
import { renderWithI18n } from "../../test/i18n";

describe("AlertsPage", () => {
  it("filters the mobile alert list by search", async () => {
    const user = userEvent.setup();
    renderWithI18n(<AlertsPage alerts={previewAlertFixtures} />);

    expect(screen.getAllByRole("article")).toHaveLength(previewAlertFixtures.length);

    await user.type(screen.getByRole("searchbox", { name: "Search alerts" }), "СБП");

    expect(screen.getAllByRole("article")).toHaveLength(1);
    expect(screen.getByRole("heading", { name: "Деградация подтверждений СБП" })).toBeInTheDocument();
  });

  it("filters rules by severity", async () => {
    const user = userEvent.setup();
    renderWithI18n(<AlertsPage alerts={previewAlertFixtures} />);

    await user.click(screen.getByRole("button", { name: /Critical/ }));

    expect(screen.getAllByRole("article")).toHaveLength(1);
    expect(screen.getByRole("heading", { name: "Обработка поисков остановилась" })).toBeInTheDocument();
  });

  it("opens an alert inspector and shows its delivery contract", async () => {
    const user = userEvent.setup();
    renderWithI18n(<AlertsPage alerts={previewAlertFixtures} />);

    await user.click(screen.getByRole("button", { name: "Open Пустые результаты у поставщика" }));

    const dialog = screen.getByRole("dialog", { name: "Пустые результаты у поставщика" });
    expect(dialog).toBeInTheDocument();
    expect(within(dialog).getByText("empty_results_30m / all_queries_30m")).toBeInTheDocument();

    await user.click(within(dialog).getByRole("tab", { name: "Delivery" }));

    expect(within(dialog).getByText("Что-то пошло так")).toBeInTheDocument();
    expect(within(dialog).getByText("Recovery in the same thread")).toBeInTheDocument();
    expect(within(dialog).getByText("Operations Dark chart · 1600×900")).toBeInTheDocument();
  });

  it("marks fixture data as preview and disables operational controls", () => {
    renderWithI18n(<AlertsPage alerts={previewAlertFixtures} preview />);

    expect(screen.getByText("Preview data · not connected to the alert control plane")).toBeInTheDocument();
    expect(screen.queryByText("All quiet")).not.toBeInTheDocument();
    expect(screen.queryByText("100%")).not.toBeInTheDocument();

    const toggle = screen.getByRole("switch", { name: "Pause Пустые результаты у поставщика" });
    expect(toggle).toHaveAttribute("aria-disabled", "true");
    expect(toggle).toHaveAttribute("tabindex", "-1");
    expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
  });
});
