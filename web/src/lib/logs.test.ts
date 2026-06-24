import { describe, it, expect } from "vitest";
import { classifyLevel, matchFilter } from "./logs";

const ln = (text: string, stderr = false) => ({ ts: 0, name: "p", instance: 0, stderr, text });

describe("classifyLevel", () => {
  it("stderr → error", () => expect(classifyLevel(ln("boom", true))).toBe("error"));
  it("warn word → warn", () => expect(classifyLevel(ln("WARNING: disk low"))).toBe("warn"));
  it("plain → info", () => expect(classifyLevel(ln("listening on :8080"))).toBe("info"));
  // Additional cases from task brief
  it("warn keyword → warn", () => expect(classifyLevel(ln("warn: something"))).toBe("warn"));
  it("stdout stderr false → not error", () => expect(classifyLevel(ln("foo", false))).toBe("info"));
});

describe("matchFilter", () => {
  it("empty → all", () => expect(matchFilter("anything", "")).toBe(true));
  it("substring ci", () => expect(matchFilter("GET /Jobs", "jobs")).toBe(true));
  it("regex", () => expect(matchFilter("GET /v1/jobs 200", "/\\d{3}/")).toBe(true));
  // invalid regex → whole query (slashes included) treated as case-insensitive literal substring
  it("bad regex: whole query IS substring → true", () => expect(matchFilter("see /a(b/ here", "/a(b/")).toBe(true));
  it("bad regex: whole query NOT substring → false", () => expect(matchFilter("a(b", "/a(b/")).toBe(false));
  it("bad regex: case-insensitive fallback → true", () => expect(matchFilter("X /A(B/ y", "/a(b/")).toBe(true));
  // Additional cases from task brief
  it("no match → false", () => expect(matchFilter("hello", "xyz")).toBe(false));
  it("regex no match → false", () => expect(matchFilter("baz", "/fo+/")).toBe(false));
  it("bad regex literal no match → false", () => expect(matchFilter("test", "/[invalid")).toBe(false));
  it("case insensitive match", () => expect(matchFilter("hello world", "WORLD")).toBe(true));
});
