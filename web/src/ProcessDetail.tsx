import { useEffect, useState } from "react";
import { AgentMetrics, Bucket, LogLine, getFleet, getLogs, getLogStats, getMetricsForProc, logout } from "./api";
import { MetricChart } from "./MetricChart";
import { LogView } from "./LogView";
import { ControlButtons } from "./ControlButtons";
import { Logo } from "./Logo";
import { navigate } from "./router";
import { FileBrowser } from "./FileBrowser";

const WINDOWS = [
  { label: "15m", ms: 15 * 60 * 1000 },
  { label: "1h", ms: 60 * 60 * 1000 },
  { label: "6h", ms: 6 * 60 * 60 * 1000 },
];
const STREAMS = ["all", "stdout", "stderr"];
const LOG_LIMITS = [100, 500, 1000];
const LOG_CAP = 5000;

function uptime(ms: number): string {
  if (ms <= 0) return "—";
  const s = Math.floor(ms / 1000), h = Math.floor(s / 3600), m = Math.floor((s % 3600) / 60);
  if (h > 0) return `${h}h ${m}m`;
  if (m > 0) return `${m}m ${s % 60}s`;
  return `${s}s`;
}
function mib(b: number): string { return b <= 0 ? "—" : `${(b / 1048576).toFixed(1)}`; }

export function ProcessDetail({ agent, proc, onLogout }: { agent: string; proc: string; onLogout: () => void }) {
  const [p, setP] = useState<{ state: string; pid: number; uptime_ms: number; restarts: number; cpu: number; mem: number; source?: "command" | "git" } | null>(null);
  const [connected, setConnected] = useState(false);
  const [errCount, setErrCount] = useState(0);
  const [windowMs, setWindowMs] = useState(WINDOWS[0].ms);
  const [detail, setDetail] = useState<Bucket[]>([]);
  const [stream, setStream] = useState("all");
  const [limit, setLimit] = useState(500);
  const [lines, setLines] = useState<LogLine[]>([]);
  const [search, setSearch] = useState("");
  const [searchDeb, setSearchDeb] = useState("");
  useEffect(() => { const id = setTimeout(() => setSearchDeb(search), 250); return () => clearTimeout(id); }, [search]);

  useEffect(() => {
    let stop = false;
    async function tick() {
      try {
        const agents = await getFleet();
        if (stop) return;
        const a = agents.find((x) => x.name === agent);
        setConnected(a?.connected ?? false);
        const pr = a?.procs.find((x) => x.name === proc) ?? null;
        setP(pr);
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

  const state = p?.state ?? "—";
  const started = p && p.uptime_ms > 0 ? new Date(Date.now() - p.uptime_ms).toLocaleTimeString() : "—";

  return (
    <div className="app">
      <div className="topbar"><Logo /><button className="btn" onClick={async () => { await logout(); onLogout(); }}>sign out</button></div>
      <div className="crumb">
        <a href="#/" onClick={(e) => { e.preventDefault(); navigate("#/"); }}>← fleet</a>
        <span className="sep">/</span><span>{agent}</span><span className="sep">/</span><span className="cur">{proc}</span>
      </div>

      <div className="card">
        <div className="dhead">
          <div className="dtitle">
            <span className={`dot ${state === "online" ? "online" : state === "errored" ? "errored" : "stopped"}`}></span>
            <span className="pname">{proc}</span>
            <span className={`pstate ${state === "errored" ? "errored" : state === "online" ? "" : "stopped"}`}>{state}</span>
          </div>
          <ControlButtons agent={agent} proc={proc} state={state} connected={connected} />
        </div>
        <div className="stat-tiles">
          <div className="tile"><div className="stat-label">cpu</div><div className="v cyan">{p ? (p.cpu * 100).toFixed(1) : "—"}<small>%</small></div></div>
          <div className="tile"><div className="stat-label">memory</div><div className="v">{p ? mib(p.mem) : "—"}<small> mb</small></div></div>
          <div className="tile"><div className="stat-label">uptime</div><div className="v">{p ? uptime(p.uptime_ms) : "—"}</div></div>
          <div className="tile"><div className="stat-label">errors · 5m</div><div className={`v ${errCount > 0 ? "danger" : ""}`}>{errCount}</div></div>
        </div>
        <div className="dmeta">pid {p?.pid || "—"} · {p?.restarts ?? 0} restarts · started {started}</div>
      </div>

      <div className="card">
        <div className="card-head">
          <span className="lbl">metrics</span>
          <span className="seg win">{WINDOWS.map((w) => <button key={w.label} className={`win ${windowMs === w.ms ? "active" : ""}`} onClick={() => setWindowMs(w.ms)}>{w.label}</button>)}</span>
        </div>
        <div className="charts2">
          <div><div className="chart-cap">cpu %</div><MetricChart buckets={detail} metric="cpu" /></div>
          <div><div className="chart-cap">memory mb</div><MetricChart buckets={detail} metric="mem" /></div>
        </div>
      </div>

      {p?.source === "git" && (
        <div className="card">
          <div className="card-head"><span className="lbl">files</span></div>
          <FileBrowser agent={agent} app={proc} />
        </div>
      )}

      <div className="card">
        <div className="log-controls">
          <span className="lbl" style={{ marginRight: "auto", fontSize: 11, color: "var(--dim)" }}>logs</span>
          <span className="seg">{STREAMS.map((s) => <button key={s} className={stream === s ? "active" : ""} onClick={() => setStream(s)}>{s}</button>)}</span>
          <span className="seg">{LOG_LIMITS.map((n) => <button key={n} className={limit === n ? "active" : ""} onClick={() => setLimit(n)}>{n}</button>)}</span>
          <input className="log-search" placeholder="search…" value={search} onChange={(e) => setSearch(e.target.value)} />
        </div>
        <LogView lines={lines} />
      </div>
    </div>
  );
}
