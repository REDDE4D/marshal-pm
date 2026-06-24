import { useEffect, useRef, useState } from "react";
import { LogLine } from "./api";
import { classifyLevel } from "./lib/logs";

function levelClass(line: LogLine): string {
  const level = classifyLevel(line);
  if (level === "error") return "er";
  if (level === "warn") return "tx warn";
  return "tx";
}

function fmtTime(ts: number): string {
  return new Date(ts).toLocaleTimeString("en-US", {
    hour: "2-digit",
    minute: "2-digit",
    second: "2-digit",
    hour12: false,
  });
}

export function LogView({ lines }: { lines: LogLine[] }) {
  const ref = useRef<HTMLDivElement>(null);
  const [stick, setStick] = useState(true);

  useEffect(() => {
    if (stick && ref.current) {
      ref.current.scrollTop = ref.current.scrollHeight;
    }
  }, [lines, stick]);

  function onScroll() {
    const el = ref.current;
    if (!el) return;
    const atBottom = el.scrollHeight - el.scrollTop - el.clientHeight < 24;
    setStick(atBottom);
  }

  return (
    <div className="logview-wrap">
      <div className="logbox" ref={ref} onScroll={onScroll}>
        {lines.length === 0 ? (
          <div className="tx" style={{ color: "var(--dim)", padding: "4px 0" }}>No log lines.</div>
        ) : (
          lines.map((l, i) => (
            <div key={i}>
              <span className="ts">{fmtTime(l.ts)}</span>{" "}
              <span className={levelClass(l)}>{l.text}</span>
            </div>
          ))
        )}
      </div>
      {!stick && (
        <button className="jump" onClick={() => setStick(true)}>
          Jump to latest ↓
        </button>
      )}
    </div>
  );
}
