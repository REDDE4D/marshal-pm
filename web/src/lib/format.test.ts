import { describe, it, expect } from "vitest";
import { relativeTime, formatBytes, formatDateShort } from "./format";

describe("relativeTime", () => {
  const now = 1_700_000_000; // fixed
  it("seconds", () => expect(relativeTime(now - 2, now)).toBe("2s"));
  it("minutes", () => expect(relativeTime(now - 8 * 60, now)).toBe("8m"));
  it("hours", () => expect(relativeTime(now - 2 * 3600, now)).toBe("2h"));
  it("days with hours", () => expect(relativeTime(now - (6 * 86400 + 2 * 3600), now)).toBe("6d 2h"));
  it("exact days drops zero hours", () => expect(relativeTime(now - 21 * 86400, now)).toBe("21d"));
  it("zero/absent → dash", () => expect(relativeTime(0, now)).toBe("—"));
  it("future clamps to 0s", () => expect(relativeTime(now + 5, now)).toBe("0s"));
});
describe("formatBytes", () => {
  it("MB", () => expect(formatBytes(212 * 1024 * 1024)).toBe("212 MB"));
  it("GB", () => expect(formatBytes(1.2 * 1024 ** 3)).toBe("1.2 GB"));
  it("zero", () => expect(formatBytes(0)).toBe("0 B"));
});
describe("formatDateShort", () => {
  it("renders Mon D", () => expect(formatDateShort(1_718_000_000)).toMatch(/[A-Z][a-z]{2} \d{1,2}/));
});
