import { useEffect, useState } from "react";
import { AgentMetrics, Bucket, LogLine, getFleet, getLogs, getLogStats, getMetricsForProc, control } from "./api";
import { MetricChart } from "./MetricChart";
import { navigate, logsHref } from "./router";
import { FileBrowser } from "./FileBrowser";
import { LiveLogModal } from "./LiveLogModal";
import { MetricCluster, Cell } from "./components/Cluster";
import { SectionHeader } from "./components/Ledger";
import { Segment } from "./components/Controls";
import { StatusGlyph } from "./components/StatusGlyph";
import { relativeTime, formatDateShort } from "./lib/format";

const WINDOWS = [
  { label: "15m", ms: 15 * 60 * 1000 },
  { label: "1h", ms: 60 * 60 * 1000 },
  { label: "6h", ms: 6 * 60 * 60 * 1000 },
];
const LOG_CAP = 5000;

function mib(b: number): string { return b <= 0 ? "—" : `${(b / 1048576).toFixed(1)}`; }

type Sub = "over" | "files";

export function ProcessDetail({ agent, proc, onLogout }: { agent: string; proc: string; onLogout: () => void }) {
  const [p, setP] = useState<{
    state: string;
    pid: number;
    uptime_ms: number;
    restarts: number;
    restarts_24h: number;
    cpu: number;
    mem: number;
    threads: number;
    open_fds: number;
    source?: "command" | "git";
    credential?: string;
    detail?: string;
  } | null>(null);
  const [connected, setConnected] = useState(false);
  const [errCount, setErrCount] = useState(0);
  const [windowMs, setWindowMs] = useState(WINDOWS[0].ms);
  const [detail, setDetail] = useState<Bucket[]>([]);
  const [stream] = useState("all");
  const [limit] = useState(500);
  const [lines, setLines] = useState<LogLine[]>([]);
  const [searchDeb] = useState("");
  const [sub, setSub] = useState<Sub>("over");
  const [showLive, setShowLive] = useState(false);

  useEffect(() => {
    let stop = false;
    async function tick() {
      try {
        const agents = await getFleet();
        if (stop) return;
        const a = agents.find((x) => x.name === agent);
        setConnected(a?.connected ?? false);
        const pr = a?.procs.find((x) => x.name === proc) ?? null;
        if (pr) {
          setP({
            state: pr.state,
            pid: pr.pid,
            uptime_ms: pr.uptime_ms,
            restarts: pr.restarts,
            restarts_24h: pr.restarts_24h,
            cpu: pr.cpu,
            mem: pr.mem,
            threads: pr.threads,
            open_fds: pr.open_fds,
            source: pr.source,
            credential: pr.credential,
            detail: pr.detail,
          });
        } else {
          setP(null);
        }
      } catch { if (!stop) onLogout(); return; }
      try {
        const counts = await getLogStats(agent);
        if (!stop) {
          let n = 0;
          for (const [label, c] of Object.entries(counts)) if (label === proc || label.startsWith(proc + "#")) n += c;
          setErrCount(n);
        }
      } catch { /* best-effort */ }
    }
    tick();
    const id = setInterval(tick, 2000);
    return () => { stop = true; clearInterval(id); };
  }, [agent, proc, onLogout]);

  useEffect(() => {
    let stop = false;
    async function tick() {
      try {
        const data: AgentMetrics[] = await getMetricsForProc(agent, proc, windowMs, 0);
        if (!stop) setDetail(data[0]?.procs[0]?.buckets ?? []);
      } catch { /* best-effort */ }
    }
    tick();
    const id = setInterval(tick, 10000);
    return () => { stop = true; clearInterval(id); };
  }, [agent, proc, windowMs]);

  useEffect(() => {
    let stop = false, cursor = 0, first = true;
    setLines([]);
    async function tick() {
      try {
        const res = await getLogs(agent, proc, { stream, limit, after: first ? 0 : cursor, q: searchDeb });
        if (stop) return;
        cursor = res.cursor || cursor; first = false;
        if (res.lines.length > 0) setLines((prev) => { const next = prev.concat(res.lines); return next.length > LOG_CAP ? next.slice(next.length - LOG_CAP) : next; });
      } catch { /* best-effort */ }
    }
    tick();
    const id = setInterval(tick, 1500);
    return () => { stop = true; clearInterval(id); };
  }, [agent, proc, stream, limit, searchDeb]);

  const nowSec = Date.now() / 1000;
  const state = p?.state ?? "—";
  const uptimeSec = p && p.uptime_ms > 0 ? Math.floor(nowSec - p.uptime_ms / 1000) : 0;

  // Cluster derived values
  const cpuPeak = detail.length
    ? Math.max(...detail.map((b) => b.cpu_max ?? b.cpu_avg ?? 0))
    : null;
  const memPeak = detail.length
    ? Math.max(...detail.map((b) => b.mem_max ?? b.mem_avg ?? 0))
    : null;
  const windowLabel = WINDOWS.find((w) => w.ms === windowMs)?.label ?? "";

  // Meta line parts
  const metaParts = [
    p?.pid ? `pid ${p.pid}` : null,
    p?.detail || null,
  ].filter(Boolean);

  // Header meta: "state · pid N · detail/cwd"
  const headerMeta = [state, ...metaParts].join(" · ");

  function doControl(action: "restart" | "stop" | "reload") {
    control(agent, proc, action).catch(() => {});
  }

  const showFiles = p?.source === "git";

  return (
    <>
      {/* Breadcrumb */}
      <div className="crumb">
        <a href="#/" onClick={(e) => { e.preventDefault(); navigate("#/"); }}>fleet</a>
        <span className="s">/</span>
        <a href="#/" onClick={(e) => { e.preventDefault(); navigate("#/"); }}>{agent}</a>
        <span className="s">/</span>
        <b>{proc}</b>
      </div>

      {/* Status header */}
      <div className="detail-hd">
        {p && <StatusGlyph state={p.state} />}
        <span className="detail-name mono">{proc}</span>
        <span className="detail-meta">{headerMeta}</span>
        <div className="detail-actions">
          <button className="btn" onClick={() => setShowLive(true)}>▤ live log</button>
          <button className="btn" disabled={!connected} onClick={() => doControl("restart")}>▸ restart</button>
          <button className="btn warn" disabled={!connected} onClick={() => {
            if (window.confirm("Reload will apply config changes without a full restart. Continue?")) doControl("reload");
          }}>⟲ reload</button>
          <button className="btn dgr" disabled={!connected} onClick={() => doControl("stop")}>■ stop</button>
        </div>
      </div>

      {/* Subtabs */}
      <div className="subtabs">
        <span className={`subtab${sub === "over" ? " on" : ""}`} onClick={() => setSub("over")}>Overview</span>
        {showFiles && (
          <span className={`subtab${sub === "files" ? " on" : ""}`} onClick={() => setSub("files")}>Files</span>
        )}
        <span className="subtab" onClick={() => navigate(logsHref(agent, proc))}>Logs ›</span>
      </div>

      {/* Overview subview */}
      {sub === "over" && (
        <div className="subview">
          {/* Metric cluster */}
          <MetricCluster cols={6}>
            <Cell
              label="CPU"
              color="teal"
              value={p ? p.cpu.toFixed(1) : "—"}
              unit="%"
              sub={cpuPeak !== null ? `peak ${cpuPeak.toFixed(1)}% · ${windowLabel}` : ""}
            />
            <Cell
              label="Memory"
              color="indigo"
              value={p ? mib(p.mem) : "—"}
              unit="MB"
              sub={memPeak !== null ? `peak ${Math.round(memPeak / 1048576)} MB` : ""}
            />
            <Cell
              label="Uptime"
              color="olive"
              value={uptimeSec > 0 ? relativeTime(uptimeSec, nowSec) : "—"}
              sub={uptimeSec > 0 ? `since ${formatDateShort(uptimeSec)}` : ""}
            />
            <Cell
              label="Restarts"
              color="amber"
              value={p?.restarts ?? "—"}
              sub={p && p.restarts_24h > 0 ? `${p.restarts_24h} in 24h` : "stable"}
            />
            <Cell
              label="Errors 5m"
              color="rose"
              value={errCount}
              sub=""
            />
            <Cell
              label="Threads"
              color="sky"
              value={p?.threads ?? "—"}
              sub={p ? `fds ${p.open_fds === -1 ? "—" : p.open_fds}` : ""}
            />
          </MetricCluster>

          {/* Metrics section */}
          <SectionHeader
            index="01"
            title="Metrics"
            right={
              <Segment
                options={WINDOWS.map((w) => ({ value: w.ms, label: w.label }))}
                value={windowMs}
                onChange={setWindowMs}
              />
            }
          />
          <div className="charts">
            <div className="chart">
              <div className="cap"><span>CPU %</span></div>
              <MetricChart buckets={detail} metric="cpu" />
            </div>
            <div className="chart">
              <div className="cap"><span>Memory MB</span></div>
              <MetricChart buckets={detail} metric="mem" />
            </div>
          </div>

          {/* Recent logs section */}
          <SectionHeader
            index="02"
            title="Recent logs"
            right={
              <a className="lnk" onClick={() => navigate(logsHref(agent, proc))}>live ›</a>
            }
          />
          <div className="logbox">
            {lines.slice(-8).map((l, i) => (
              <div key={i}>
                <span className="ts">{new Date(l.ts).toLocaleTimeString()}</span>{" "}
                <span className={l.stderr ? "er" : "tx"}>{l.text}</span>
              </div>
            ))}
            {lines.length === 0 && <div className="tx" style={{ color: "var(--dim)", padding: "4px 0" }}>No log lines yet.</div>}
          </div>
        </div>
      )}

      {/* Files subview */}
      {sub === "files" && showFiles && (
        <div className="subview">
          <FileBrowser agent={agent} app={proc} credential={p?.credential} />
        </div>
      )}

      {/* Logs subview: clicking the Logs subtab navigates away, so this never renders */}

      {/* Live-log modal */}
      {showLive && (
        <LiveLogModal
          agent={agent}
          proc={proc}
          onClose={() => setShowLive(false)}
        />
      )}
    </>
  );
}
