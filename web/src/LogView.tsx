import { useEffect, useRef, useState } from "react";
import { LogLine } from "./api";

export function LogView({ lines, search }: { lines: LogLine[]; search: string }) {
  const ref = useRef<HTMLDivElement>(null);
  const [stick, setStick] = useState(true);

  const needle = search.trim().toLowerCase();
  const shown = needle ? lines.filter((l) => l.text.toLowerCase().includes(needle)) : lines;

  useEffect(() => {
    if (stick && ref.current) {
      ref.current.scrollTop = ref.current.scrollHeight;
    }
  }, [shown, stick]);

  function onScroll() {
    const el = ref.current;
    if (!el) return;
    const atBottom = el.scrollHeight - el.scrollTop - el.clientHeight < 24;
    setStick(atBottom);
  }

  return (
    <div className="logview-wrap">
      <div className="logview" ref={ref} onScroll={onScroll}>
        {shown.length === 0 ? (
          <p className="chart-empty">No log lines.</p>
        ) : (
          shown.map((l, i) => (
            <div key={i} className={l.stderr ? "logline err" : "logline"}>
              <span className="logts">{new Date(l.ts).toLocaleTimeString()}</span>
              <span className="logtext">{l.text}</span>
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
