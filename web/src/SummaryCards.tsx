import { Agent } from "./api";

function mib(b: number): string { return `${(b / 1048576).toFixed(1)}`; }

export function SummaryCards({ agents }: { agents: Agent[] }) {
  const online = agents.filter((a) => a.connected).length;
  const procs = agents.flatMap((a) => a.procs);
  const up = procs.filter((p) => p.state === "online").length;
  const errored = procs.filter((p) => p.state === "errored").length;
  const cpu = procs.reduce((s, p) => s + p.cpu, 0) * 100;
  const mem = procs.reduce((s, p) => s + p.mem, 0);
  return (
    <div className="summary">
      <div className="stat-card"><div className="stat-label">agents online</div><div className="stat-value">{online}<small> / {agents.length}</small></div></div>
      <div className="stat-card"><div className="stat-label">processes</div><div className="stat-value">{up}<small> / {procs.length} up</small></div></div>
      <div className="stat-card"><div className="stat-label">total cpu</div><div className="stat-value cyan">{cpu.toFixed(0)}<small>%</small></div></div>
      <div className="stat-card"><div className="stat-label">total memory</div><div className="stat-value">{mib(mem)}<small> mb</small></div></div>
      <div className="stat-card"><div className="stat-label">errors</div><div className={`stat-value ${errored > 0 ? "danger" : ""}`}>{errored}</div></div>
    </div>
  );
}
