import { useEffect, useState } from "react";
import { Agent, AgentMetrics, getFleet, getLogStats, getMetrics, logout } from "./api";
import { SummaryCards } from "./SummaryCards";
import { ProcessCard } from "./ProcessCard";
import { Logo } from "./Logo";
import { AddAppModal } from "./AddAppModal";
import { ConnectAgentModal } from "./ConnectAgentModal";
import { RestartAllButton } from "./RestartAllButton";

type Series = Record<string, Record<string, { cpu: number[]; mem: number[] }>>;

function fmtUptime(bootUnix?: number): string {
  if (!bootUnix) return "";
  const s = Math.floor(Date.now() / 1000) - bootUnix;
  if (s <= 0) return "";
  const d = Math.floor(s / 86400), h = Math.floor((s % 86400) / 3600);
  return d > 0 ? `up ${d}d ${h}h` : `up ${h}h`;
}

function agentMeta(a: Agent): string {
  const parts: string[] = [];
  if (a.hostname) parts.push(a.hostname);
  if (a.os || a.arch) parts.push([a.os, a.arch].filter(Boolean).join("/"));
  if (a.marshal_version) parts.push(`marshal ${a.marshal_version}`);
  if (a.ip) parts.push(a.ip);
  const up = fmtUptime(a.host_boot_unix);
  if (up) parts.push(up);
  if (!a.connected) parts.unshift("offline");
  return parts.join(" · ");
}

function fmtBps(bps: number): string {
  if (bps < 1024) return `${bps.toFixed(0)} B/s`;
  if (bps < 1024 * 1024) return `${(bps / 1024).toFixed(1)} KB/s`;
  return `${(bps / (1024 * 1024)).toFixed(1)} MB/s`;
}

function hostMeta(a: Agent): string | null {
  const h = a.host;
  if (!h) return null;
  const gb = (b: number) => (b / (1024 * 1024 * 1024)).toFixed(1);
  return [
    `cpu ${h.cpu_percent.toFixed(0)}%`,
    `load ${h.load1.toFixed(2)}/${h.load5.toFixed(2)}/${h.load15.toFixed(2)}`,
    `mem ${gb(h.mem_used)}/${gb(h.mem_total)}gb (${h.mem_used_pct.toFixed(0)}%)`,
    `↓${fmtBps(h.net_rx_bps)} ↑${fmtBps(h.net_tx_bps)}`,
  ].join(" · ");
}

export function Overview({ onLogout }: { onLogout: () => void }) {
  const [agents, setAgents] = useState<Agent[]>([]);
  const [metrics, setMetrics] = useState<Series>({});
  const [errors, setErrors] = useState<Record<string, Record<string, number>>>({});
  const [showAdd, setShowAdd] = useState(false);
  const [showConnect, setShowConnect] = useState(false);

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
          <button className="btn" onClick={() => setShowConnect(true)}>+ connect agent</button>
          <button className="btn" onClick={() => { window.location.hash = "#/credentials"; }}>credentials</button>
          <button className="btn" onClick={() => { window.location.hash = "#/notifications"; }}>notifications</button>
          <button className="btn" onClick={() => { window.location.hash = "#/errors"; }}>errors</button>
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
      {showConnect && <ConnectAgentModal onClose={() => setShowConnect(false)} />}
      <SummaryCards agents={agents} />
      {agents.length === 0 && <p className="empty">no agents connected.</p>}
      {agents.map((a) => (
        <div key={a.name}>
          <div className="agent-head">
            <span className={`dot ${a.connected ? "online" : "stopped"}`}></span>
            <span className="name">{a.name}</span>
            <span className="seen">{agentMeta(a)}</span>
            {hostMeta(a) && <span className="seen host-meta">{hostMeta(a)}</span>}
            {a.procs.length > 0 && <RestartAllButton agent={a.name} connected={a.connected} />}
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
