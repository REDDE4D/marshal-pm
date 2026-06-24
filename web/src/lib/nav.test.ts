import { describe, it, expect } from "vitest";
import { navItemFor } from "./nav";
describe("navItemFor", () => {
  it("overview → fleet", () => expect(navItemFor({ name: "overview" })).toBe("fleet"));
  it("detail → fleet", () => expect(navItemFor({ name: "detail", agent: "a", proc: "p" })).toBe("fleet"));
  it("errors → errors", () => expect(navItemFor({ name: "errors" })).toBe("errors"));
  it("logs → logs", () => expect(navItemFor({ name: "logs" })).toBe("logs"));
  it("notifications → notif", () => expect(navItemFor({ name: "notifications" })).toBe("notif"));
  it("credentials → creds", () => expect(navItemFor({ name: "credentials" })).toBe("creds"));
});
