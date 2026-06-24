import { useEffect, useState } from "react";
import { logsDownloadURL } from "./api";
import { Segment } from "./components/Controls";
import { matchFilter } from "./lib/logs";
import { useLogStream } from "./hooks/useLogStream";
import { LogView } from "./LogView";

const STREAM_OPTS = [
  { value: "all", label: "all" },
  { value: "stdout", label: "stdout" },
  { value: "stderr", label: "stderr" },
];

const LIMIT_OPTS = [
  { value: 500, label: "500" },
  { value: 1000, label: "1000" },
];

export function LogPanel({ agent, proc }: { agent: string; proc: string }) {
  const [stream, setStream] = useState("all");
  const [limit, setLimit] = useState(500);
  const [filterRaw, setFilterRaw] = useState("");
  const [filter, setFilter] = useState("");

  // Debounce filter ~250ms
  useEffect(() => {
    const t = setTimeout(() => setFilter(filterRaw), 250);
    return () => clearTimeout(t);
  }, [filterRaw]);

  const { lines } = useLogStream(agent, proc, { stream, limit, q: "" });

  // Apply client-side filter
  const filteredLines = filter
    ? lines.filter((l) => matchFilter(l.text, filter))
    : lines;

  const downloadUrl = logsDownloadURL(agent, proc, { stream, q: filter });

  return (
    <div>
      <div className="logbar">
        <Segment options={STREAM_OPTS} value={stream} onChange={setStream} />
        <input
          className="inp"
          style={{ flex: 1 }}
          placeholder="filter… (text or /regex/)"
          value={filterRaw}
          onChange={(e) => setFilterRaw(e.target.value)}
        />
        <Segment options={LIMIT_OPTS} value={limit} onChange={setLimit} />
        <a className="btn ghost" href={downloadUrl}>
          ⤓ download
        </a>
      </div>
      <LogView lines={filteredLines} />
    </div>
  );
}
