import type { LogLine } from "../api";

/** Classify a log line as error, warn, or info. */
export function classifyLevel(line: LogLine): "error" | "warn" | "info" {
  if (line.stderr) return "error";
  if (/\b(warn|warning)\b/i.test(line.text)) return "warn";
  return "info";
}

/**
 * Test whether `text` matches `query`.
 * - Empty query → always true.
 * - `/pattern/` form → regex match (invalid regex falls back to literal substring).
 * - Otherwise → case-insensitive substring match.
 */
export function matchFilter(text: string, query: string): boolean {
  if (query === "") return true;

  const reMatch = query.match(/^\/(.*)\/$/);
  if (reMatch) {
    try {
      return new RegExp(reMatch[1]).test(text);
    } catch {
      // Invalid regex — fall back to literal substring search on the inner pattern
      return text.includes(reMatch[1]);
    }
  }

  return text.toLowerCase().includes(query.toLowerCase());
}
