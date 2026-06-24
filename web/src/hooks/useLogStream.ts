import { useEffect, useRef, useState } from "react";
import { getLogs, LogLine } from "../api";

const LOG_CAP = 5000;

export interface LogStreamOpts {
  stream: string;
  limit: number;
  q: string;
  enabled?: boolean;
}

export function useLogStream(
  agent: string,
  proc: string,
  opts: LogStreamOpts,
): { lines: LogLine[]; clear: () => void } {
  const { stream, limit, q, enabled = true } = opts;
  const [lines, setLines] = useState<LogLine[]>([]);
  const cursorRef = useRef<number>(0);

  // Reset when key params change
  useEffect(() => {
    setLines([]);
    cursorRef.current = 0;
  }, [agent, proc, stream, limit, q]);

  useEffect(() => {
    if (!enabled) return;

    let stop = false;
    let first = true;

    async function tick() {
      try {
        const res = await getLogs(agent, proc, {
          stream,
          limit,
          after: first ? 0 : cursorRef.current,
          q,
        });
        if (stop) return;
        if (res.cursor) cursorRef.current = res.cursor;
        first = false;
        if (res.lines.length > 0) {
          setLines((prev) => {
            const next = prev.concat(res.lines);
            return next.length > LOG_CAP ? next.slice(next.length - LOG_CAP) : next;
          });
        }
      } catch {
        /* best-effort */
      }
    }

    tick();
    const id = setInterval(tick, 1500);
    return () => {
      stop = true;
      clearInterval(id);
    };
  }, [agent, proc, stream, limit, q, enabled]);

  function clear() {
    setLines([]);
    cursorRef.current = 0;
  }

  return { lines, clear };
}
