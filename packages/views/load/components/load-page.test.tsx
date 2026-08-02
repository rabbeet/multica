import { screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it } from "vitest";
import { renderWithI18n } from "../../test/i18n";
import { LoadPage } from "./load-page";
import { previewLoadSnapshot } from "../fixtures";

describe("LoadPage", () => {
  it("combines the overview and server-first mobile concepts", async () => {
    const user = userEvent.setup();
    renderWithI18n(<LoadPage snapshot={previewLoadSnapshot} preview />);

    expect(screen.getByRole("status")).toHaveTextContent("Preview data");
    for (const period of ["1h", "6h", "24h", "7d"]) {
      expect(screen.getByRole("button", { name: period })).toBeDisabled();
    }
    expect(screen.getByRole("tab", { name: "Overview" })).toHaveAttribute("aria-selected", "true");
    expect(screen.getByText("api-prod-03 is CPU constrained")).toBeInTheDocument();

    await user.click(screen.getByRole("tab", { name: "Servers" }));

    expect(screen.getByRole("table", { name: "Node load map" })).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Open api-prod-03" })).toBeInTheDocument();
  });

  it("renders bottlenecks without a detail target as non-interactive", () => {
    renderWithI18n(<LoadPage snapshot={previewLoadSnapshot} preview />);

    expect(screen.getByText("Checkout queue is growing").closest("button")).toBeNull();
  });

  it("localizes server performance details for zh-Hans", async () => {
    const user = userEvent.setup();
    renderWithI18n(<LoadPage snapshot={previewLoadSnapshot} preview />, { locale: "zh-Hans" });

    expect(screen.getByText("系统稳定")).toBeInTheDocument();
    expect(screen.getByText("Checkout 队列正在增长")).toBeInTheDocument();
    await user.click(screen.getByRole("tab", { name: "服务器" }));
    await user.click(screen.getByRole("button", { name: "Open api-prod-03" }));

    const dialog = screen.getByRole("dialog", { name: "api-prod-03" });
    expect(within(dialog).getByText("资源")).toBeInTheDocument();
    expect(within(dialog).getByText("CPU 限流")).toBeInTheDocument();
    expect(within(dialog).getByText("影响")).toBeInTheDocument();
    expect(within(dialog).getByText("网络")).toBeInTheDocument();
  });

  it("renders critical KPI and node tones as destructive", async () => {
    const user = userEvent.setup();
    const criticalSnapshot = {
      ...previewLoadSnapshot,
      kpis: [{ ...previewLoadSnapshot.kpis[0]!, tone: "critical" as const }],
      nodes: [{ ...previewLoadSnapshot.nodes[0]!, tone: "critical" as const }],
    };
    renderWithI18n(<LoadPage snapshot={criticalSnapshot} />);

    expect(screen.getByText("normal")).toHaveClass("text-destructive");
    await user.click(screen.getByRole("tab", { name: "Servers" }));
    const card = screen.getByRole("button", { name: "Open api-prod-03" }).closest("article");
    expect(card?.querySelector(".bg-destructive")).not.toBeNull();
  });

  it("opens a full-screen server inspector with performance details", async () => {
    const user = userEvent.setup();
    renderWithI18n(<LoadPage snapshot={previewLoadSnapshot} preview />);

    await user.click(screen.getByRole("tab", { name: "Servers" }));
    await user.click(screen.getByRole("button", { name: "Open api-prod-03" }));

    const dialog = screen.getByRole("dialog", { name: "api-prod-03" });
    expect(within(dialog).getByText("CPU throttling")).toBeInTheDocument();
    expect(within(dialog).getByText("12%")).toBeInTheDocument();
    expect(within(dialog).getByText("410 ms")).toBeInTheDocument();
  });
});
