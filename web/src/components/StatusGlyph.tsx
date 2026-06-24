import { statusOf } from "../lib/status";

const dotClassToText: Record<"on" | "er" | "st", string> = {
  on: "on-t",
  er: "er-t",
  st: "stp-t",
};

export function StatusGlyph({ state }: { state: string }) {
  const s = statusOf(state);
  const textClass = dotClassToText[s.dotClass];
  return (
    <span className="st">
      <span className={"sq " + s.dotClass}></span>
      <span className={textClass}>{s.word}</span>
    </span>
  );
}
