import { describe, it, expect, vi, afterEach } from "vitest";
import { putNotifSettings } from "./api";

function mockFetch(status: number, body: string) {
  return vi.fn(async () =>
    new Response(body, { status, headers: { "Content-Type": "text/plain" } }),
  );
}

afterEach(() => vi.restoreAllMocks());

describe("putNotifSettings", () => {
  it("reports success on 200", async () => {
    vi.stubGlobal("fetch", mockFetch(200, JSON.stringify({ ok: true })));
    const r = await putNotifSettings({ cooldown_seconds: 300 });
    expect(r.ok).toBe(true);
  });

  it("surfaces the server error on 503 (notifications disabled)", async () => {
    vi.stubGlobal("fetch", mockFetch(503, "notifications unavailable\n"));
    const r = await putNotifSettings({ cooldown_seconds: 300 });
    expect(r.ok).toBe(false);
    expect(r.error).toContain("notifications unavailable");
  });

  it("surfaces the server error on 400 (bad request)", async () => {
    vi.stubGlobal("fetch", mockFetch(400, "bad request\n"));
    const r = await putNotifSettings({ cooldown_seconds: 300 });
    expect(r.ok).toBe(false);
    expect(r.error).toBeTruthy();
  });
});
