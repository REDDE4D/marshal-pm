import { describe, it, expect } from "vitest";
import { statusOf } from "./status";

describe("statusOf", () => {
  it("online/running → online", () => {
    for (const s of ["online", "running"]) {
      const r = statusOf(s);
      expect(r.kind).toBe("online");
      expect(r.dotClass).toBe("on");
    }
  });
  it("errored/failed → errored", () => {
    for (const s of ["errored", "failed"]) expect(statusOf(s).kind).toBe("errored");
  });
  it("stopped/unknown → stopped", () => {
    expect(statusOf("stopped").kind).toBe("stopped");
    expect(statusOf("whatever").kind).toBe("stopped");
  });
  it("word echoes the state", () => expect(statusOf("online").word).toBe("online"));
});
