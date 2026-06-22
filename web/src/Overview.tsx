import { useEffect, useState } from "react";
import { Agent, AgentMetrics, getFleet, getLogStats, getMetrics, logout } from "./api";
import { SummaryCards } from "./SummaryCards";
import { ProcessCard } from "./ProcessCard";
import { Logo } from "./Logo";
import { AddAppModal } from "./AddAppModal";

type Series = Record<string, Record<string, { cpu: number[]; mem: number[] }>>;

export function Overview({ onLogout }: { onLogout: () => void }) {
  const [agents, setAgents] = useState<Agent[]>([]);
  const [metrics, setMetrics] = useState<Series>({});
  const [errors, setErrors] = useState<Record<string, Record<string, number>>>({});
  const [showAdd, setShowAdd] = useState(false);

  useEffect(() => {
    let stop = false;
    async function tick() {
      try {
        const f = await getFleet();
        if (stop) return;
        setAgents(f);
        const errMap: Record<string, Record<string, number>> = {};
        for (const a of f.filter((x) => x.connected)) {
          try {
            const counts = await getLogStats(a.name);
            const per: Record<string, number> = {};
            for (const [label, c] of Object.entries(counts)) {
              const name = label.includes("#") ? label.slice(0, label.lastIndexOf("#")) : label;
              per[name] = (per[name] ?? 0) + c;
            }
            errMap[a.name] = per;
          } catch { /* best-effort */ }
        }
        if (!stop) setErrors(errMap);
      } catch { if (!stop) onLogout(); }
    }
    tick();
    const id = setInterval(tick, 2000);
    return () => { stop = true; clearInterval(id); };
  }, [onLogout]);

  useEffect(() => {
    let stop = false;
    async function tick() {
      try {
        const data: AgentMetrics[] = await getMetrics(5 * 60 * 1000);
        if (stop) return;
        const next: Series = {};
        for (const a of data) {
          next[a.agent] = {};
          for (const p of a.procs) next[a.agent][p.name] = { cpu: p.buckets.map((b) => b.cpu_avg), mem: p.buckets.map((b) => b.mem_avg) };
        }
        setMetrics(next);
      } catch { /* best-effort */ }
    }
    tick();
    const id = setInterval(tick, 10000);
    return () => { stop = true; clearInterval(id); };
  }, []);

  return (
    <div className="app">
      <div className="topbar">
        <Logo />
        <div className="topbar-actions">
          <button className="btn" onClick={() => setShowAdd(true)}>+ add app</button>
          <button className="btn" onClick={() => { window.location.hash = "#/credentials"; }}>credentials</button>
          <button className="btn" onClick={() => { window.location.hash = "#/notifications"; }}>notifications</button>
          <button className="btn" onClick={async () => { await logout(); onLogout(); }}>sign out</button>
        </div>
      </div>
      {showAdd && (
        <AddAppModal
          agents={agents}
          onClose={() => setShowAdd(false)}
          onAdded={() => {}}
        />
      )}
      <SummaryCards agents={agents} />
      {agents.length === 0 && <p className="empty">no agents connected.</p>}
      {agents.map((a) => (
        <div key={a.name}>
          <div className="agent-head">
            <span className={`dot ${a.connected ? "online" : "stopped"}`}></span>
            <span className="name">{a.name}</span>
            {!a.connected && <span className="seen">offline</span>}
          </div>
          {a.procs.length === 0 && <p className="empty">no processes.</p>}
          {a.procs.map((p) => (
            <ProcessCard key={`${p.name}-${p.pid}`} agent={a.name} proc={p} connected={a.connected}
              cpuSeries={metrics[a.name]?.[p.name]?.cpu ?? []} memSeries={metrics[a.name]?.[p.name]?.mem ?? []}
              errors={errors[a.name]?.[p.name] ?? 0} />
          ))}
        </div>
      ))}
    </div>
  );
}
