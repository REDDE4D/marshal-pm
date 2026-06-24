import { useEffect, useRef, useState } from "react";
import { logsDownloadURL } from "./api";
import { Modal } from "./components/Modal";
import { Chip, Segment } from "./components/Controls";
import { useLogStream } from "./hooks/useLogStream";
import { classifyLevel, matchFilter } from "./lib/logs";

type Level = "info" | "warn" | "error";
type Stream = "all" | "stdout" | "stderr";

const STREAM_OPTIONS: { value: Stream; label: string }[] = [
  { value: "all", label: "all" },
  { value: "stdout", label: "stdout" },
  { value: "stderr", label: "stderr" },
];

const PAUSE_OPTIONS: { value: boolean; label: string }[] = [
  { value: false, label: "● live" },
  { value: true, label: "pause" },
];

const ALL_LEVELS: Level[] = ["info", "warn", "error"];

export function LiveLogModal({
  agent,
  proc,
  onClose,
}: {
  agent: string;
  proc: string;
  onClose: () => void;
}) {
  const [paused, setPaused] = useState(false);
  const [stream, setStream] = useState<Stream>("all");
  const [filter, setFilter] = useState("");
  const [debouncedFilter, setDebouncedFilter] = useState("");
  const [levelSet, setLevelSet] = useState<Set<Level>>(new Set(ALL_LEVELS));

  // Debounce the filter input ~250ms
  useEffect(() => {
    const id = setTimeout(() => setDebouncedFilter(filter), 250);
    return () => clearTimeout(id);
  }, [filter]);

  const { lines } = useLogStream(agent, proc, {
    stream,
    limit: 1000,
    q: "",
    enabled: !paused,
  });

  // Auto-scroll to bottom when not paused
  const boxRef = useRef<HTMLDivElement>(null);
  useEffect(() => {
    if (!paused && boxRef.current) {
      boxRef.current.scrollTop = boxRef.current.scrollHeight;
    }
  }, [lines, paused]);

  // Apply both text/regex filter and level filter client-side
  const visibleLines = lines.filter(
    (l) =>
      levelSet.has(classifyLevel(l)) &&
      matchFilter(l.text, debouncedFilter),
  );

  function toggleLevel(lvl: Level) {
    setLevelSet((prev) => {
      const next = new Set(prev);
      if (next.has(lvl)) {
        next.delete(lvl);
      } else {
        next.add(lvl);
      }
      return next;
    });
  }

  function fmtTime(ts: number): string {
    const d = new Date(ts);
    const hh = String(d.getHours()).padStart(2, "0");
    const mm = String(d.getMinutes()).padStart(2, "0");
    const ss = String(d.getSeconds()).padStart(2, "0");
    return `${hh}:${mm}:${ss}`;
  }

  const downloadURL = logsDownloadURL(agent, proc, { stream, q: "" });

  return (
    <Modal
      title={`Live log · ${proc}`}
      onClose={onClose}
      modalStyle={{ width: "780px", maxWidth: "calc(100vw - 32px)" }}
    >
      {/* Live / pause toggle in the header area — rendered inside modal body
          since Modal.tsx doesn't expose a header slot. We replicate the demo
          layout: filter bar + logbox. The mhead already has title + close. */}
      <div style={{ display: "flex", justifyContent: "flex-end", padding: "0 18px 0", marginTop: "-8px" }}>
        <Segment
          options={PAUSE_OPTIONS}
          value={paused}
          onChange={setPaused}
        />
      </div>

      {/* Filter bar */}
      <div className="logbar">
        <input
          className="inp"
          style={{ flex: 1 }}
          placeholder="filter… (text or /regex/)"
          value={filter}
          onChange={(e) => setFilter(e.target.value)}
        />
        <Segment
          options={STREAM_OPTIONS}
          value={stream}
          onChange={setStream}
        />
        <div style={{ display: "flex", gap: "4px" }}>
          {ALL_LEVELS.map((lvl) => (
            <Chip
              key={lvl}
              label={lvl}
              on={levelSet.has(lvl)}
              onClick={() => toggleLevel(lvl)}
            />
          ))}
        </div>
        <a
          className="btn ghost"
          href={downloadURL}
          download
          title="download logs"
          aria-label="download logs"
        >
          ⤓
        </a>
      </div>

      {/* Log box */}
      <div className="logbox" ref={boxRef}>
        {visibleLines.map((l, i) => {
          const lvl = classifyLevel(l);
          const spanClass = lvl === "error" ? "er" : lvl === "warn" ? "warn" : "tx";
          return (
            <div key={`${l.ts}-${i}`}>
              <span className="ts">{fmtTime(l.ts)}</span>{" "}
              <span className={spanClass}>{l.text}</span>
            </div>
          );
        })}
        {visibleLines.length === 0 && (
          <div className="tx" style={{ color: "var(--dim)", padding: "4px 0" }}>
            {lines.length === 0 ? "No log lines yet." : "No lines match the current filter."}
          </div>
        )}
        {visibleLines.length > 0 && <span className="cur" />}
      </div>
    </Modal>
  );
}
