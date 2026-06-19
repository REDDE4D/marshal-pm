import { Proc, redeploy } from "./api";
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

function stateClass(state: string): string {
  if (state === "online") return "";
  if (state === "errored" || state === "failed") return "errored";
  return "stopped";
}

function dotClass(state: string): string {
  if (state === "online") return "online";
  if (state === "errored" || state === "failed") return "errored";
  return "stopped";
}

export function ProcessCard({ agent, proc, connected, cpuSeries, memSeries, errors }:
  { agent: string; proc: Proc; connected: boolean; cpuSeries: number[]; memSeries: number[]; errors: number }) {
  const state = proc.state;
  const dot = dotClass(state);
  const meta = state === "online"
    ? `${agent} · pid ${proc.pid || "—"} · up ${uptime(proc.uptime_ms)} · ${proc.restarts} restarts`
    : `${agent} · ${state} · ${proc.restarts} restarts`;

  const isDeploying = state === "cloning" || state === "building";
  const isFailed = state === "failed";

  function handleRedeploy(e: React.MouseEvent) {
    e.preventDefault();
    e.stopPropagation();
    redeploy(agent, proc.name).catch(() => {});
  }

  return (
    <a className={`pcard ${state === "errored" || state === "failed" ? "errored" : ""}`} href={procHref(agent, proc.name)}
       onClick={(e) => { e.preventDefault(); navigate(procHref(agent, proc.name)); }}>
      <div className="pcard-head">
        <div className="pcard-id">
          <span className={`dot ${dot}`}></span>
          <span className="pname">{proc.name}</span>
          {isDeploying ? (
            <span className="pstate state-deploying stopped">
              {state === "cloning" ? "deploying…" : "building…"}
            </span>
          ) : isFailed ? (
            <span className="pstate state-failed errored">failed</span>
          ) : (
            <span className={`pstate ${stateClass(state)}`}>{state}</span>
          )}
        </div>
        <div className="ctl" onClick={(e) => e.stopPropagation()}>
          {proc.source === "git" && state === "online" && (
            <button className="btn-redeploy ctl-btn" onClick={handleRedeploy}>
              ↺ redeploy
            </button>
          )}
          <ControlButtons agent={agent} proc={proc.name} state={state} connected={connected} />
        </div>
      </div>
      {isFailed && proc.detail && (
        <div className="pcard-failed-detail">{proc.detail}</div>
      )}
      <div className="pcard-meta">{meta}</div>
      {!isDeploying && (
        <div className="pcard-metrics">
          <span className="metric"><span className="mlabel">cpu</span><Sparkline points={cpuSeries} color="#2DD4BF" /><span className="mval">{(proc.cpu * 100).toFixed(0)}%</span></span>
          <span className="metric"><span className="mlabel">mem</span><Sparkline points={memSeries} color="#5B6BD8" /><span className="mval">{mib(proc.mem)}</span></span>
          {errors > 0 && <span className="err-badge">⚠ {errors} errors</span>}
          <span className="pcard-link">view details →</span>
        </div>
      )}
    </a>
  );
}
