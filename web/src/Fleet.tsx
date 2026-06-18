import { Fragment, useEffect, useState } from "react";
import {
  Agent,
  AgentMetrics,
  Bucket,
  LogLine,
  control,
  getFleet,
  getLogs,
  getMetrics,
  getMetricsForProc,
  logout,
} from "./api";
import { Sparkline } from "./Sparkline";
import { MetricChart } from "./MetricChart";
import { LogView } from "./LogView";

function uptime(ms: number): string {
  if (ms <= 0) return "—";
  const s = Math.floor(ms / 1000);
  const h = Math.floor(s / 3600);
  const m = Math.floor((s % 3600) / 60);
  if (h > 0) return `${h}h ${m}m`;
  if (m > 0) return `${m}m ${s % 60}s`;
  return `${s}s`;
}

function mib(bytes: number): string {
  if (bytes <= 0) return "—";
  return `${(bytes / (1024 * 1024)).toFixed(1)} MB`;
}

function ProcActions({ agent, proc, disabled }: { agent: string; proc: string; disabled: boolean }) {
  const [pending, setPending] = useState<"restart" | "stop" | null>(null);
  const [busy, setBusy] = useState(false);
  const [msg, setMsg] = useState("");

  function ask(action: "restart" | "stop") {
    setMsg("");
    setPending(action);
    window.setTimeout(() => setPending((p) => (p === action ? null : p)), 3000);
  }

  async function fire(action: "restart" | "stop") {
    setPending(null);
    setBusy(true);
    setMsg("");
    const res = await control(agent, proc, action);
    setBusy(false);
    setMsg(res.ok ? "✓" : res.error || "error");
    window.setTimeout(() => setMsg(""), 4000);
  }

  if (disabled) return <span className="muted">—</span>;
  if (busy) return <span className="muted">…</span>;

  return (
    <span className="actions" onClick={(e) => e.stopPropagation()}>
      {pending ? (
        <>
          <button className="confirm" onClick={() => fire(pending)}>
            Confirm {pending}?
          </button>
          <button onClick={() => setPending(null)}>✕</button>
        </>
      ) : (
        <>
          <button onClick={() => ask("restart")}>Restart</button>
          <button onClick={() => ask("stop")}>Stop</button>
        </>
      )}
      {msg && <span className="action-msg">{msg}</span>}
    </span>
  );
}

const WINDOWS: { label: string; ms: number }[] = [
  { label: "5m", ms: 5 * 60 * 1000 },
  { label: "1h", ms: 60 * 60 * 1000 },
  { label: "6h", ms: 6 * 60 * 60 * 1000 },
  { label: "24h", ms: 24 * 60 * 60 * 1000 },
];

const LOG_LIMITS = [100, 500, 1000];
const LOG_CAP = 5000;
const STREAMS = ["all", "stdout", "stderr"];

export function Fleet({ onLogout }: { onLogout: () => void }) {
  const [agents, setAgents] = useState<Agent[]>([]);
  const [err, setErr] = useState("");
  const [metrics, setMetrics] = useState<Record<string, Record<string, { cpu: number[]; mem: number[] }>>>({});
  const [expanded, setExpanded] = useState<{ agent: string; proc: string } | null>(null);
  const [windowMs, setWindowMs] = useState(WINDOWS[1].ms); // default 1h
  const [detail, setDetail] = useState<Bucket[]>([]);
  const [tab, setTab] = useState<"charts" | "logs">("charts");
  const [logStream, setLogStream] = useState("all");
  const [logLimit, setLogLimit] = useState(500);
  const [logLines, setLogLines] = useState<LogLine[]>([]);
  const [logSearch, setLogSearch] = useState("");
  const [logSearchDebounced, setLogSearchDebounced] = useState("");
  useEffect(() => {
    const id = setTimeout(() => setLogSearchDebounced(logSearch), 250);
    return () => clearTimeout(id);
  }, [logSearch]);

  useEffect(() => {
    let stop = false;
    async function tick() {
      try {
        const f = await getFleet();
        if (!stop) {
          setAgents(f);
          setErr("");
        }
      } catch {
        if (!stop) onLogout();
      }
    }
    tick();
    const id = setInterval(tick, 2000);
    return () => {
      stop = true;
      clearInterval(id);
    };
  }, [onLogout]);

  useEffect(() => {
    let stop = false;
    async function tick() {
      try {
        const data: AgentMetrics[] = await getMetrics(5 * 60 * 1000);
        if (stop) return;
        const next: Record<string, Record<string, { cpu: number[]; mem: number[] }>> = {};
        for (const a of data) {
          next[a.agent] = {};
          for (const p of a.procs) {
            next[a.agent][p.name] = {
              cpu: p.buckets.map((b) => b.cpu_avg),
              mem: p.buckets.map((b) => b.mem_avg),
            };
          }
        }
        setMetrics(next);
      } catch {
        // metrics are best-effort; the fleet poll owns auth/logout.
      }
    }
    tick();
    const id = setInterval(tick, 10000);
    return () => {
      stop = true;
      clearInterval(id);
    };
  }, []);

  useEffect(() => {
    if (!expanded) {
      setDetail([]);
      return;
    }
    let stop = false;
    async function tick() {
      try {
        const data = await getMetricsForProc(expanded!.agent, expanded!.proc, windowMs, 0);
        if (!stop) setDetail(data[0]?.procs[0]?.buckets ?? []);
      } catch {
        // best-effort; fleet poll owns auth.
      }
    }
    tick();
    const id = setInterval(tick, 10000);
    return () => {
      stop = true;
      clearInterval(id);
    };
  }, [expanded, windowMs]);

  useEffect(() => {
    if (!expanded || tab !== "logs") {
      setLogLines([]);
      return;
    }
    let stop = false;
    let cursor = 0;
    let first = true;
    setLogLines([]);
    async function tick() {
      try {
        const res = await getLogs(expanded!.agent, expanded!.proc, {
          stream: logStream,
          limit: logLimit,
          after: first ? 0 : cursor,
          q: logSearchDebounced,
        });
        if (stop) return;
        cursor = res.cursor || cursor;
        first = false;
        if (res.lines.length > 0) {
          setLogLines((prev) => {
            const next = prev.concat(res.lines);
            return next.length > LOG_CAP ? next.slice(next.length - LOG_CAP) : next;
          });
        }
      } catch {
        // best-effort; the fleet poll owns auth/logout.
      }
    }
    tick();
    const id = setInterval(tick, 1500);
    return () => {
      stop = true;
      clearInterval(id);
    };
  }, [expanded, tab, logStream, logLimit, logSearchDebounced]);

  async function doLogout() {
    await logout();
    onLogout();
  }

  return (
    <div className="fleet">
      <header>
        <h1>Fleet</h1>
        <button onClick={doLogout}>Sign out</button>
      </header>
      {err && <p className="error">{err}</p>}
      {agents.length === 0 && <p className="empty">No agents connected.</p>}
      {agents.map((a) => (
        <section key={a.name} className="agent">
          <h2>
            {a.name}{" "}
            <span className={a.connected ? "badge online" : "badge offline"}>
              {a.connected ? "online" : "offline"}
            </span>
          </h2>
          <table>
            <thead>
              <tr>
                <th>Process</th>
                <th>State</th>
                <th>PID</th>
                <th>Uptime</th>
                <th>Restarts</th>
                <th>CPU</th>
                <th>Mem</th>
                <th>Actions</th>
              </tr>
            </thead>
            <tbody>
              {a.procs.map((p) => {
                const isOpen = expanded?.agent === a.name && expanded?.proc === p.name;
                return (
                  <Fragment key={`${p.name}-${p.pid}`}>
                    <tr
                      className={isOpen ? "proc open" : "proc"}
                      onClick={() => setExpanded(isOpen ? null : { agent: a.name, proc: p.name })}
                    >
                      <td>{p.name}</td>
                      <td>{p.state}</td>
                      <td>{p.pid || "—"}</td>
                      <td>{uptime(p.uptime_ms)}</td>
                      <td>{p.restarts}</td>
                      <td>
                        {(p.cpu * 100).toFixed(1)}%
                        <Sparkline points={metrics[a.name]?.[p.name]?.cpu ?? []} color="#4ade80" />
                      </td>
                      <td>
                        {mib(p.mem)}
                        <Sparkline points={metrics[a.name]?.[p.name]?.mem ?? []} color="#60a5fa" />
                      </td>
                      <td>
                        <ProcActions agent={a.name} proc={p.name} disabled={!a.connected} />
                      </td>
                    </tr>
                    {isOpen && (
                      <tr className="detail">
                        <td colSpan={8}>
                          <div className="tabs">
                            <button
                              className={tab === "charts" ? "active" : ""}
                              onClick={(e) => {
                                e.stopPropagation();
                                setTab("charts");
                              }}
                            >
                              Charts
                            </button>
                            <button
                              className={tab === "logs" ? "active" : ""}
                              onClick={(e) => {
                                e.stopPropagation();
                                setTab("logs");
                              }}
                            >
                              Logs
                            </button>
                          </div>
                          {tab === "charts" ? (
                            <>
                              <div className="windows">
                                {WINDOWS.map((wnd) => (
                                  <button
                                    key={wnd.label}
                                    className={windowMs === wnd.ms ? "active" : ""}
                                    onClick={(e) => {
                                      e.stopPropagation();
                                      setWindowMs(wnd.ms);
                                    }}
                                  >
                                    {wnd.label}
                                  </button>
                                ))}
                              </div>
                              <div className="charts">
                                <div>
                                  <h4>CPU</h4>
                                  <MetricChart buckets={detail} metric="cpu" />
                                </div>
                                <div>
                                  <h4>Memory</h4>
                                  <MetricChart buckets={detail} metric="mem" />
                                </div>
                              </div>
                            </>
                          ) : (
                            <div className="logs-panel" onClick={(e) => e.stopPropagation()}>
                              <div className="log-controls">
                                <div className="seg">
                                  {STREAMS.map((s) => (
                                    <button
                                      key={s}
                                      className={logStream === s ? "active" : ""}
                                      onClick={() => setLogStream(s)}
                                    >
                                      {s}
                                    </button>
                                  ))}
                                </div>
                                <div className="seg">
                                  {LOG_LIMITS.map((n) => (
                                    <button
                                      key={n}
                                      className={logLimit === n ? "active" : ""}
                                      onClick={() => setLogLimit(n)}
                                    >
                                      {n}
                                    </button>
                                  ))}
                                </div>
                                <input
                                  className="log-search"
                                  placeholder="search…"
                                  value={logSearch}
                                  onChange={(e) => setLogSearch(e.target.value)}
                                />
                              </div>
                              <LogView lines={logLines} />
                            </div>
                          )}
                        </td>
                      </tr>
                    )}
                  </Fragment>
                );
              })}
              {a.procs.length === 0 && (
                <tr>
                  <td colSpan={8} className="empty">
                    No processes.
                  </td>
                </tr>
              )}
            </tbody>
          </table>
        </section>
      ))}
    </div>
  );
}
