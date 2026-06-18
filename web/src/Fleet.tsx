import { useEffect, useState } from "react";
import {
  Agent,
  AgentMetrics,
  getFleet,
  getMetrics,
  logout,
} from "./api";
import { Sparkline } from "./Sparkline";
import { Logo } from "./Logo";
import { navigate, procHref } from "./router";

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

export function Fleet({ onLogout }: { onLogout: () => void }) {
  const [agents, setAgents] = useState<Agent[]>([]);
  const [err, setErr] = useState("");
  const [metrics, setMetrics] = useState<Record<string, Record<string, { cpu: number[]; mem: number[] }>>>({});

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

  async function doLogout() {
    await logout();
    onLogout();
  }

  return (
    <div className="fleet">
      <header>
        <Logo />
        <button onClick={doLogout}>sign out</button>
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
              </tr>
            </thead>
            <tbody>
              {a.procs.map((p) => (
                <tr key={`${p.name}-${p.pid}`} className="proc" style={{ cursor: "pointer" }} onClick={() => navigate(procHref(a.name, p.name))}>
                  <td>{p.name}</td>
                  <td>{p.state}</td>
                  <td>{p.pid || "—"}</td>
                  <td>{uptime(p.uptime_ms)}</td>
                  <td>{p.restarts}</td>
                  <td>{(p.cpu * 100).toFixed(1)}%<Sparkline points={metrics[a.name]?.[p.name]?.cpu ?? []} color="#2DD4BF" /></td>
                  <td>{mib(p.mem)}<Sparkline points={metrics[a.name]?.[p.name]?.mem ?? []} color="#5B6BD8" /></td>
                </tr>
              ))}
              {a.procs.length === 0 && (
                <tr>
                  <td colSpan={7} className="empty">
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
