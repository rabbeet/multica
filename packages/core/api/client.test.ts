import { afterEach, describe, expect, it, vi } from "vitest";
import { ApiClient, ApiError, parseExportFilename } from "./client";

afterEach(() => {
  vi.unstubAllGlobals();
});

describe("ApiClient", () => {
  it("preserves HTTP status on failed requests", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn().mockResolvedValue(
        new Response(JSON.stringify({ error: "workspace slug already exists" }), {
          status: 409,
          statusText: "Conflict",
          headers: { "Content-Type": "application/json" },
        }),
      ),
    );

    const client = new ApiClient("https://api.example.test");

    try {
      await client.createWorkspace({ name: "Test", slug: "test" });
      throw new Error("expected createWorkspace to fail");
    } catch (error) {
      expect(error).toBeInstanceOf(ApiError);
      expect(error).toMatchObject({
        message: "workspace slug already exists",
        status: 409,
        statusText: "Conflict",
      });
    }
  });

  it("uses the expected HTTP contract for autopilot endpoints", async () => {
    const fetchMock = vi.fn().mockImplementation(() => Promise.resolve(
      new Response(JSON.stringify({ autopilots: [], runs: [], total: 0 }), {
        status: 200,
        headers: { "Content-Type": "application/json" },
      }),
    ));
    vi.stubGlobal("fetch", fetchMock);

    const client = new ApiClient("https://api.example.test");

    await client.listAutopilots({ status: "active" });
    await client.getAutopilot("ap-1");
    await client.createAutopilot({
      title: "Daily triage",
      assignee_id: "agent-1",
      execution_mode: "create_issue",
    });
    await client.updateAutopilot("ap-1", { status: "paused" });
    await client.deleteAutopilot("ap-1");
    await client.triggerAutopilot("ap-1");
    await client.listAutopilotRuns("ap-1", { limit: 10, offset: 20 });
    await client.createAutopilotTrigger("ap-1", {
      kind: "schedule",
      cron_expression: "0 9 * * *",
      timezone: "UTC",
    });
    await client.updateAutopilotTrigger("ap-1", "tr-1", { enabled: false });
    await client.deleteAutopilotTrigger("ap-1", "tr-1");

    const calls = fetchMock.mock.calls.map(([url, init]) => ({
      url,
      method: init?.method ?? "GET",
      body: init?.body,
    }));

    expect(calls).toMatchObject([
      { url: "https://api.example.test/api/autopilots?status=active", method: "GET" },
      { url: "https://api.example.test/api/autopilots/ap-1", method: "GET" },
      {
        url: "https://api.example.test/api/autopilots",
        method: "POST",
        body: JSON.stringify({
          title: "Daily triage",
          assignee_id: "agent-1",
          execution_mode: "create_issue",
        }),
      },
      {
        url: "https://api.example.test/api/autopilots/ap-1",
        method: "PATCH",
        body: JSON.stringify({ status: "paused" }),
      },
      { url: "https://api.example.test/api/autopilots/ap-1", method: "DELETE" },
      { url: "https://api.example.test/api/autopilots/ap-1/trigger", method: "POST" },
      { url: "https://api.example.test/api/autopilots/ap-1/runs?limit=10&offset=20", method: "GET" },
      {
        url: "https://api.example.test/api/autopilots/ap-1/triggers",
        method: "POST",
        body: JSON.stringify({
          kind: "schedule",
          cron_expression: "0 9 * * *",
          timezone: "UTC",
        }),
      },
      {
        url: "https://api.example.test/api/autopilots/ap-1/triggers/tr-1",
        method: "PATCH",
        body: JSON.stringify({ enabled: false }),
      },
      { url: "https://api.example.test/api/autopilots/ap-1/triggers/tr-1", method: "DELETE" },
    ]);
  });

  it("emits X-Client-* headers when identity is configured", async () => {
    const fetchMock = vi.fn().mockResolvedValue(
      new Response(JSON.stringify([]), {
        status: 200,
        headers: { "Content-Type": "application/json" },
      }),
    );
    vi.stubGlobal("fetch", fetchMock);

    const client = new ApiClient("https://api.example.test", {
      identity: { platform: "desktop", version: "1.2.3", os: "macos" },
    });
    await client.listWorkspaces();

    const headers = fetchMock.mock.calls[0]![1]!.headers as Record<string, string>;
    expect(headers["X-Client-Platform"]).toBe("desktop");
    expect(headers["X-Client-Version"]).toBe("1.2.3");
    expect(headers["X-Client-OS"]).toBe("macos");
  });

  it("omits X-Client-* headers when identity is not configured", async () => {
    const fetchMock = vi.fn().mockResolvedValue(
      new Response(JSON.stringify([]), {
        status: 200,
        headers: { "Content-Type": "application/json" },
      }),
    );
    vi.stubGlobal("fetch", fetchMock);

    const client = new ApiClient("https://api.example.test");
    await client.listWorkspaces();

    const headers = fetchMock.mock.calls[0]![1]!.headers as Record<string, string>;
    expect(headers["X-Client-Platform"]).toBeUndefined();
    expect(headers["X-Client-Version"]).toBeUndefined();
    expect(headers["X-Client-OS"]).toBeUndefined();
  });
});

// PUL-266: PDF export filename parser. Pure function; tests cover the
// shapes the server actually emits plus the fallback paths the UI
// relies on (null when missing, unquoted vs quoted forms).
describe("parseExportFilename", () => {
  it("returns null for missing or empty headers", () => {
    expect(parseExportFilename(null)).toBeNull();
    expect(parseExportFilename(undefined)).toBeNull();
    expect(parseExportFilename("")).toBeNull();
  });

  it("extracts a quoted filename", () => {
    expect(
      parseExportFilename(`attachment; filename="PUL-266.pdf"`),
    ).toBe("PUL-266.pdf");
  });

  it("extracts an unquoted filename", () => {
    expect(parseExportFilename(`attachment; filename=PUL-266.pdf`)).toBe(
      "PUL-266.pdf",
    );
  });

  it("handles thread-style filenames with multiple dashes", () => {
    expect(
      parseExportFilename(
        `attachment; filename="PUL-266-thread-deadbeef.pdf"`,
      ),
    ).toBe("PUL-266-thread-deadbeef.pdf");
  });

  it("returns null when no filename field is present", () => {
    expect(parseExportFilename("attachment")).toBeNull();
  });
});

describe("ApiClient.exportIssuePdf", () => {
  it("returns the body as a Blob and the server-suggested filename", async () => {
    const pdfBlob = new Blob([new Uint8Array([0x25, 0x50, 0x44, 0x46])], {
      type: "application/pdf",
    });
    const fetchMock = vi.fn().mockResolvedValue(
      new Response(pdfBlob, {
        status: 200,
        headers: {
          "Content-Type": "application/pdf",
          "Content-Disposition": `attachment; filename="PUL-266.pdf"`,
        },
      }),
    );
    vi.stubGlobal("fetch", fetchMock);

    const client = new ApiClient("https://api.example.test");
    const { blob, filename } = await client.exportIssuePdf("PUL-266");

    expect(filename).toBe("PUL-266.pdf");
    expect(blob.type).toBe("application/pdf");
    expect(fetchMock.mock.calls[0]![0]).toBe(
      "https://api.example.test/api/issues/PUL-266/export.pdf",
    );
  });

  it("appends ?thread=<id> when threadId is set", async () => {
    const fetchMock = vi.fn().mockResolvedValue(
      new Response(new Blob([new Uint8Array([0x25, 0x50, 0x44, 0x46])]), {
        status: 200,
        headers: {
          "Content-Type": "application/pdf",
          "Content-Disposition": `attachment; filename="PUL-266-thread-abcdef01.pdf"`,
        },
      }),
    );
    vi.stubGlobal("fetch", fetchMock);

    const client = new ApiClient("https://api.example.test");
    await client.exportIssuePdf("PUL-266", { threadId: "abc-123" });

    expect(fetchMock.mock.calls[0]![0]).toBe(
      "https://api.example.test/api/issues/PUL-266/export.pdf?thread=abc-123",
    );
  });

  it("falls back to <id>.pdf when the server omits Content-Disposition", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn().mockResolvedValue(
        new Response(new Blob([new Uint8Array([0x25, 0x50, 0x44, 0x46])]), {
          status: 200,
          headers: { "Content-Type": "application/pdf" },
        }),
      ),
    );

    const client = new ApiClient("https://api.example.test");
    const { filename } = await client.exportIssuePdf("PUL-266");

    expect(filename).toBe("PUL-266.pdf");
  });

  it("throws an ApiError with the server's message on non-2xx", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn().mockResolvedValue(
        new Response(
          JSON.stringify({
            error: "ticket too large for single PDF; try thread export",
          }),
          {
            status: 413,
            statusText: "Payload Too Large",
            headers: { "Content-Type": "application/json" },
          },
        ),
      ),
    );

    const client = new ApiClient("https://api.example.test");
    try {
      await client.exportIssuePdf("PUL-266");
      throw new Error("expected exportIssuePdf to throw");
    } catch (error) {
      expect(error).toBeInstanceOf(ApiError);
      expect(error).toMatchObject({
        status: 413,
        message: "ticket too large for single PDF; try thread export",
      });
    }
  });
});
