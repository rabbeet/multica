import { screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it } from "vitest";
import { AlertsPage } from "./alerts-page";
import { previewAlertFixtures } from "../fixtures";
import { renderWithI18n } from "../../test/i18n";

describe("AlertsPage", () => {
  it("includes every current alert policy group", () => {
    renderWithI18n(<AlertsPage alerts={previewAlertFixtures} preview />);

    expect(previewAlertFixtures).toHaveLength(23);
    for (const name of [
      "Нет кликов",
      "Рост задержки поставщика",
      "Падение success rate продукта",
      "Падение output продукта",
      "Hub-oneways: пустые результаты",
      "Hub-oneways: падение scheduled",
      "Hub-oneways: плохие маршруты",
      "Обработка product pipeline остановилась",
      "Всплеск PRICE_CHANGED",
      "Всплеск INVALID",
      "Всплеск ERROR",
      "Отсутствует op_status",
      "СБП: рост отмен",
      "СБП: деградация создания",
      "Аномалии фильтрации продукта",
    ]) {
      expect(screen.getByRole("heading", { name })).toBeInTheDocument();
    }
  });

  it("shows data source, refresh cadence, and analysis window as chips", () => {
    renderWithI18n(<AlertsPage alerts={previewAlertFixtures} preview />);

    const mysqlAlert = screen.getByRole("heading", { name: "Нет кликов" }).closest("article");
    expect(mysqlAlert).toHaveTextContent("MySQL");
    expect(mysqlAlert).toHaveTextContent("Every minute");
    expect(mysqlAlert).toHaveTextContent("Adaptive window");

    const postgresAlert = screen.getByRole("heading", { name: "Деградация 3-D Secure" }).closest("article");
    expect(postgresAlert).toHaveTextContent("PostgreSQL");

    const grafanaAlert = screen.getByRole("heading", { name: "Пустые результаты у поставщика" }).closest("article");
    expect(grafanaAlert).toHaveTextContent("Grafana / Prometheus");

    const clickhouseAlert = screen.getByRole("heading", { name: "Аномалии фильтрации продукта" }).closest("article");
    expect(clickhouseAlert).toHaveTextContent("ClickHouse");
  });

  it("filters the mobile alert list by search", async () => {
    const user = userEvent.setup();
    renderWithI18n(<AlertsPage alerts={previewAlertFixtures} />);

    expect(screen.getAllByRole("article")).toHaveLength(previewAlertFixtures.length);

    await user.type(screen.getByRole("searchbox", { name: "Search alerts" }), "подтверждений СБП");

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
