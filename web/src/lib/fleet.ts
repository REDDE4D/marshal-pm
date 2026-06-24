import type { Agent } from "../api";
import { statusOf } from "./status";

export type FleetSummary = {
  agents: number;
  online: number;
  errored: number;
  running: number;
  totalProcs: number;
  avgCpu: number | null;
  memUsed: number | null;
  memTotal: number | null;
  restarts24h: number;
  erroredName: string | null;
};

export function fleetSummary(agents: Agent[]): FleetSummary {
  let online = 0;
  let errored = 0;
  let running = 0;
  let totalProcs = 0;
  let restarts24h = 0;
  let cpuSum = 0;
  let cpuCount = 0;
  let memUsedSum = 0;
  let memTotalSum = 0;
  let hasHost = false;
  let erroredName: string | null = null;

  for (const agent of agents) {
    // Determine if this agent has any errored proc
    let agentErrored = false;
    for (const proc of agent.procs) {
      const s = statusOf(proc.state);
      if (s.kind === "errored") {
        agentErrored = true;
        if (erroredName === null) {
          erroredName = proc.name;
        }
      }
      if (s.kind === "online") {
        running++;
      }
      totalProcs++;
      restarts24h += proc.restarts_24h;
    }

    if (agentErrored) {
      errored++;
    } else if (agent.connected) {
      online++;
    }

    if (agent.host != null) {
      hasHost = true;
      cpuSum += agent.host.cpu_percent;
      cpuCount++;
      memUsedSum += agent.host.mem_used;
      memTotalSum += agent.host.mem_total;
    }
  }

  return {
    agents: agents.length,
    online,
    errored,
    running,
    totalProcs,
    restarts24h,
    avgCpu: hasHost ? cpuSum / cpuCount : null,
    memUsed: hasHost ? memUsedSum : null,
    memTotal: hasHost ? memTotalSum : null,
    erroredName,
  };
}
