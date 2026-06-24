export type StatusResult = {
  kind: "online" | "errored" | "stopped";
  word: string;
  dotClass: "on" | "er" | "st";
};

export function statusOf(state: string): StatusResult {
  const word = state;
  if (state === "online" || state === "running") {
    return { kind: "online", word, dotClass: "on" };
  }
  if (state === "errored" || state === "failed") {
    return { kind: "errored", word, dotClass: "er" };
  }
  return { kind: "stopped", word, dotClass: "st" };
}
