import { Proc } from "./api";
import { Sparkline } from "./Sparkline";
import { ControlButtons } from "./ControlButtons";
import { navigate, procHref } from "./router";

function uptime(ms: number): string {
  if (ms <= 0) return "—";
  const s = Math.floor(ms / 1000), h = Math.floor(s / 3600), m = Math.floor((s % 3600) / 60);
  if (h > 0) return `${h}h ${m}m`;
  if (m > 0) return `${m}m ${s % 60}s`;
  return `${s}s`;
}
function mib(b: number): string { return b <= 0 ? "—" : `${(b / 1048576).toFixed(1)}mb`; }

export function ProcessCard({ agent, proc, connected, cpuSeries, memSeries, errors }:
  { agent: string; proc: Proc; connected: boolean; cpuSeries: number[]; memSeries: number[]; errors: number }) {
  const state = proc.state;
  const dot = state === "online" ? "online" : state === "errored" ? "errored" : "stopped";
  const meta = state === "online"
    ? `${agent} · pid ${proc.pid || "—"} · up ${uptime(proc.uptime_ms)} · ${proc.restarts} restarts`
    : `${agent} · ${state} · ${proc.restarts} restarts`;
  return (
    <a className={`pcard ${state === "errored" ? "errored" : ""}`} href={procHref(agent, proc.name)}
       onClick={(e) => { e.preventDefault(); navigate(procHref(agent, proc.name)); }}>
      <div className="pcard-head">
        <div className="pcard-id">
          <span className={`dot ${dot}`}></span>
          <span className="pname">{proc.name}</span>
          <span className={`pstate ${state === "errored" ? "errored" : state === "online" ? "" : "stopped"}`}>{state}</span>
        </div>
        <ControlButtons agent={agent} proc={proc.name} state={state} connected={connected} />
      </div>
      <div className="pcard-meta">{meta}</div>
      <div className="pcard-metrics">
        <span className="metric"><span className="mlabel">cpu</span><Sparkline points={cpuSeries} color="#2DD4BF" /><span className="mval">{(proc.cpu * 100).toFixed(0)}%</span></span>
        <span className="metric"><span className="mlabel">mem</span><Sparkline points={memSeries} color="#5B6BD8" /><span className="mval">{mib(proc.mem)}</span></span>
        {errors > 0 && <span className="err-badge">⚠ {errors} errors</span>}
        <span className="pcard-link">view details →</span>
      </div>
    </a>
  );
}
