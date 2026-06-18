import { useEffect, useRef, useState } from "react";
import { LogLine } from "./api";

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
      <div className="logview" ref={ref} onScroll={onScroll}>
        {lines.length === 0 ? (
          <p className="chart-empty">No log lines.</p>
        ) : (
          lines.map((l, i) => (
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
