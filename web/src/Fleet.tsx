import { useEffect, useState } from "react";
import { Agent, getFleet, logout } from "./api";

function uptime(ms: number): string {
  if (ms <= 0) return "—";
  const s = Math.floor(ms / 1000);
  const h = Math.floor(s / 3600);
  const m = Math.floor((s % 3600) / 60);
  if (h > 0) return `${h}h ${m}m`;
  if (m > 0) return `${m}m ${s % 60}s`;
  return `${s}s`;
}

export function Fleet({ onLogout }: { onLogout: () => void }) {
  const [agents, setAgents] = useState<Agent[]>([]);
  const [err, setErr] = useState("");

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
              </tr>
            </thead>
            <tbody>
              {a.procs.map((p) => (
                <tr key={`${p.name}-${p.pid}`}>
                  <td>{p.name}</td>
                  <td>{p.state}</td>
                  <td>{p.pid || "—"}</td>
                  <td>{uptime(p.uptime_ms)}</td>
                  <td>{p.restarts}</td>
                </tr>
              ))}
              {a.procs.length === 0 && (
                <tr>
                  <td colSpan={5} className="empty">
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
