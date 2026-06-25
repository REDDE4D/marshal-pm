import { useEffect, useState } from "react";
import { Agent, AgentMetrics, control, getFleet, getMetrics } from "./api";
import { AddAppModal } from "./AddAppModal";
import { ConnectAgentModal } from "./ConnectAgentModal";
import { LiveLogModal } from "./LiveLogModal";
import { RestartAllButton } from "./RestartAllButton";
import { MetricCluster, Cell } from "./components/Cluster";
import {
  SectionHeader,
  LedgerHeader,
  LedgerRow,
  QuickAction,
} from "./components/Ledger";
import { StatusGlyph } from "./components/StatusGlyph";
import { Sparkline } from "./Sparkline";
import { fleetSummary } from "./lib/fleet";
import { statusOf } from "./lib/status";
import { relativeTime, formatBytes } from "./lib/format";
import { navigate, procHref } from "./router";
import { EmptyState } from "./components/EmptyState";
import { Button } from "./components/Controls";

type Series = Record<string, Record<string, { cpu: number[]; mem: number[] }>>;

// PCOLS matches the demo3 process ledger column layout (9 columns)
const PCOLS = "26px 1.5fr 0.95fr 0.55fr 0.6fr 0.75fr 0.8fr 0.45fr 1fr";

export function Overview({ onLogout }: { onLogout: () => void }) {
  const [agents, setAgents] = useState<Agent[]>([]);
  const [metrics, setMetrics] = useState<Series>({});
  const [showAdd, setShowAdd] = useState(false);
  const [showConnect, setShowConnect] = useState(false);
  const [liveLog, setLiveLog] = useState<{ agent: string; proc: string } | null>(null);

  // 2s fleet poll
  useEffect(() => {
    let stop = false;
    async function tick() {
      try {
        const f = await getFleet();
        if (stop) return;
        setAgents(f);
      } catch { if (!stop) onLogout(); }
    }
    tick();
    const id = setInterval(tick, 2000);
    return () => { stop = true; clearInterval(id); };
  }, [onLogout]);

  // 10s metrics poll — builds per-agent/proc CPU+mem series
  useEffect(() => {
    let stop = false;
    async function tick() {
      try {
        const data: AgentMetrics[] = await getMetrics(5 * 60 * 1000);
        if (stop) return;
        const next: Series = {};
        for (const a of data) {
          next[a.agent] = {};
          for (const p of a.procs)
            next[a.agent][p.name] = {
              cpu: p.buckets.map((b) => b.cpu_avg),
              mem: p.buckets.map((b) => b.mem_avg),
            };
        }
        setMetrics(next);
      } catch { /* best-effort */ }
    }
    tick();
    const id = setInterval(tick, 10000);
    return () => { stop = true; clearInterval(id); };
  }, []);

  const fs = fleetSummary(agents);

  // Restarts-24h sub: count of procs with restarts_24h > 0
  const procsWithRestarts = agents
    .flatMap((a) => a.procs)
    .filter((p) => p.restarts_24h > 0).length;

  return (
    <>
      {/* Action row: add app / connect agent — kept in Overview (not the shell context-bar)
          because these modals depend on Overview's agents state. Intentional deviation from
          demo bar placement; restyled in Task 17. */}
      <div className="actions" style={{ display: "flex", justifyContent: "flex-end", gap: "0.5rem", marginBottom: "0.75rem" }}>
        <Button variant="ghost" size="sm" onClick={() => setShowAdd(true)}>+ add app</Button>
        <Button variant="ghost" size="sm" onClick={() => setShowConnect(true)}>+ connect agent</Button>
      </div>

      {/* Fleet cluster */}
      <MetricCluster cols={6}>
        <Cell
          label="Agents"
          value={fs.agents}
          sub={`${fs.online} online · ${fs.errored} errored`}
        />
        <Cell
          label="Running"
          value={fs.running}
          sub={`of ${fs.totalProcs} processes`}
        />
        <Cell
          label="Errored"
          value={fs.errored}
          color="rose"
          sub={fs.erroredName ?? "none"}
        />
        <Cell
          label="Avg CPU"
          value={fs.avgCpu === null ? "—" : Math.round(fs.avgCpu)}
          unit={fs.avgCpu === null ? undefined : "%"}
          color="teal"
          sub="across fleet"
        />
        <Cell
          label="Total Mem"
          value={fs.memUsed === null ? "—" : formatBytes(fs.memUsed)}
          color="indigo"
          sub={fs.memTotal === null ? "" : "of " + formatBytes(fs.memTotal)}
        />
        <Cell
          label="Restarts 24h"
          value={fs.restarts24h}
          color="amber"
          sub={`${procsWithRestarts} proc${procsWithRestarts !== 1 ? "s" : ""}`}
        />
      </MetricCluster>

      {/* Empty fleet */}
      {agents.length === 0 && (
        <EmptyState message="No agents connected." action={<span>Use &ldquo;+ connect agent&rdquo; above to enroll your first agent.</span>} />
      )}

      {/* Per-agent sections */}
      {agents.map((a, ai) => {
        const idx = String(ai + 1).padStart(2, "0");
        const agentErrored = a.procs.some((p) => statusOf(p.state).kind === "errored");

        // Build agent meta line — drop empty pieces, no leading/trailing " · "
        const metaParts: string[] = [];
        if (a.ip) metaParts.push(a.ip);
        const osParts = [a.os, a.arch].filter(Boolean);
        if (osParts.length) metaParts.push(osParts.join("/"));
        if (a.last_seen_unix) metaParts.push(`seen ${relativeTime(a.last_seen_unix)}`);
        const metaLine = metaParts.join(" · ");

        return (
          <div key={a.name}>
            <SectionHeader
              index={idx}
              title={a.name}
              glyph={<span className={"glyph " + (agentErrored ? "g-er" : "g-on")} />}
              right={<RestartAllButton agent={a.name} connected={a.connected} />}
              count={`${a.procs.length} proc`}
            />

            {metaLine && (
              <div
                className="d"
                style={{ marginLeft: "calc(26px + 0.75rem)", marginTop: "-0.25rem", marginBottom: "0.5rem", fontSize: "0.72rem" }}
              >
                {metaLine}
              </div>
            )}

            {a.procs.length === 0 && (
              <p className="empty" style={{ marginLeft: "calc(26px + 0.75rem)" }}>
                no processes.
              </p>
            )}

            {a.procs.length > 0 && (
              <>
                <LedgerHeader cols={PCOLS}>
                  <span />
                  <span>Process</span>
                  <span>Status</span>
                  <span className="rr">PID</span>
                  <span className="rr">CPU</span>
                  <span className="rr">MEM</span>
                  <span className="rr">Uptime</span>
                  <span className="rr">↻</span>
                  <span>Trend</span>
                </LedgerHeader>

                {a.procs.map((proc, pi) => {
                  const isErrored = statusOf(proc.state).kind === "errored";
                  const cpuSeries = metrics[a.name]?.[proc.name]?.cpu ?? [];

                  const rowActions: QuickAction[] = [
                    {
                      icon: "▤",
                      label: "Log",
                      onClick: () => setLiveLog({ agent: a.name, proc: proc.name }),
                    },
                    {
                      icon: "▸",
                      label: "Restart",
                      onClick: () => control(a.name, proc.name, "restart").catch(() => {}),
                    },
                    {
                      icon: "⟲",
                      label: "Reload",
                      variant: "warn",
                      onClick: () => control(a.name, proc.name, "reload").catch(() => {}),
                    },
                    {
                      icon: "■",
                      label: "Stop",
                      variant: "dgr",
                      onClick: () => control(a.name, proc.name, "stop").catch(() => {}),
                    },
                  ];

                  // proc.mem is in bytes; display in MiB
                  const memMB = proc.mem > 0 ? (proc.mem / 1048576).toFixed(1) : null;
                  // proc.cpu is already a percentage (per-core %, summed over the group).
                  const cpuPct = proc.cpu.toFixed(0);
                  // uptime: proc.uptime_ms → seconds since boot for relativeTime
                  const uptimeSec = Math.floor(Date.now() / 1000) - Math.floor(proc.uptime_ms / 1000);

                  return (
                    <LedgerRow
                      key={`${proc.name}-${proc.pid}`}
                      cols={PCOLS}
                      onClick={() => navigate(procHref(a.name, proc.name))}
                      actions={rowActions}
                    >
                      {/* Index */}
                      <span className="ix">{String(pi + 1).padStart(2, "0")}</span>

                      {/* Process name + source/detail */}
                      <div>
                        <div className="nm">{proc.name}</div>
                        <div className="sub">
                          {proc.source}
                          {proc.detail ? ` · ${proc.detail}` : ""}
                        </div>
                      </div>

                      {/* Status glyph */}
                      <StatusGlyph state={proc.state} />

                      {/* PID */}
                      <span className="rr">
                        <span className="v">{isErrored ? <span className="d">—</span> : (proc.pid || "—")}</span>
                      </span>

                      {/* CPU */}
                      <span className="rr">
                        {isErrored
                          ? <span className="v d">—</span>
                          : (
                            <span className="v teal">
                              {cpuPct}<span className="un">%</span>
                            </span>
                          )}
                      </span>

                      {/* MEM */}
                      <span className="rr">
                        {isErrored
                          ? <span className="v d">—</span>
                          : (
                            <span className="v indigo">
                              {memMB ?? "—"}{memMB && <span className="un">MB</span>}
                            </span>
                          )}
                      </span>

                      {/* Uptime */}
                      <span className="rr">
                        {isErrored
                          ? <span className="v d">—</span>
                          : (
                            <span className="v olive">
                              {proc.uptime_ms > 0 ? relativeTime(uptimeSec) : "—"}
                            </span>
                          )}
                      </span>

                      {/* Restarts 24h */}
                      <span className="rr">
                        <span className={"v" + (proc.restarts_24h > 0 ? " amber" : "")}>
                          {proc.restarts_24h}
                        </span>
                      </span>

                      {/* CPU Sparkline (trend) */}
                      <Sparkline
                        points={cpuSeries}
                        color={isErrored ? "var(--rose)" : "var(--teal)"}
                      />
                    </LedgerRow>
                  );
                })}
              </>
            )}
          </div>
        );
      })}

      {/* Modals — kept inside Overview; they depend on agents state */}
      {showAdd && (
        <AddAppModal
          agents={agents}
          onClose={() => setShowAdd(false)}
          onAdded={() => {}}
        />
      )}
      {showConnect && <ConnectAgentModal onClose={() => setShowConnect(false)} />}
      {liveLog && (
        <LiveLogModal
          agent={liveLog.agent}
          proc={liveLog.proc}
          onClose={() => setLiveLog(null)}
        />
      )}
    </>
  );
}
