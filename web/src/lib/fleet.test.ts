import { describe, it, expect } from "vitest";
import type { Agent } from "../api";
import { fleetSummary } from "./fleet";

function makeAgent(overrides: Partial<Agent> & { procs?: Agent["procs"] }): Agent {
  return {
    name: "agent",
    connected: true,
    last_seen_unix: 0,
    procs: [],
    ...overrides,
  };
}

describe("fleetSummary", () => {
  it("counts agents, online/errored, running procs, totalProcs, restarts24h, erroredName", () => {
    const agents: Agent[] = [
      makeAgent({
        name: "alpha",
        connected: true,
        host: { cpu_percent: 40, load1: 0, load5: 0, load15: 0, mem_total: 8_000_000_000, mem_used: 2_000_000_000, mem_used_pct: 25, net_rx_bps: 0, net_tx_bps: 0 },
        procs: [
          { name: "web", state: "online", pid: 1, uptime_ms: 1000, restarts: 0, cpu: 5, mem: 100, threads: 1, open_fds: 10, exit_code: 0, restarts_24h: 1 },
          { name: "worker", state: "running", pid: 2, uptime_ms: 2000, restarts: 0, cpu: 3, mem: 80, threads: 1, open_fds: 8, exit_code: 0, restarts_24h: 0 },
        ],
      }),
      makeAgent({
        name: "beta",
        connected: true,
        // no host — tests host-less aggregation
        procs: [
          { name: "cron", state: "errored", pid: 0, uptime_ms: 0, restarts: 3, cpu: 0, mem: 0, threads: 0, open_fds: -1, exit_code: 1, restarts_24h: 3 },
        ],
      }),
    ];

    const s = fleetSummary(agents);
    expect(s.agents).toBe(2);
    // alpha has 0 errored procs → online; beta has an errored proc → errored
    expect(s.online).toBe(1);
    expect(s.errored).toBe(1);
    // running = procs whose statusOf.kind === "online" → "online" + "running" procs on alpha
    expect(s.running).toBe(2);
    expect(s.totalProcs).toBe(3);
    expect(s.restarts24h).toBe(4); // 1 + 0 + 3
    // avgCpu: only alpha reports host → 40 / 1 = 40
    expect(s.avgCpu).toBeCloseTo(40);
    // memUsed/memTotal: only alpha
    expect(s.memUsed).toBe(2_000_000_000);
    expect(s.memTotal).toBe(8_000_000_000);
    // first errored proc name
    expect(s.erroredName).toBe("cron");
  });

  it("host-less fleet → avgCpu/memUsed/memTotal all null", () => {
    const agents: Agent[] = [
      makeAgent({
        name: "gamma",
        connected: true,
        procs: [
          { name: "app", state: "online", pid: 1, uptime_ms: 500, restarts: 0, cpu: 2, mem: 50, threads: 1, open_fds: 5, exit_code: 0, restarts_24h: 0 },
        ],
      }),
    ];
    const s = fleetSummary(agents);
    expect(s.avgCpu).toBeNull();
    expect(s.memUsed).toBeNull();
    expect(s.memTotal).toBeNull();
    expect(s.agents).toBe(1);
    expect(s.online).toBe(1);
    expect(s.errored).toBe(0);
    expect(s.running).toBe(1);
    expect(s.totalProcs).toBe(1);
    expect(s.erroredName).toBeNull();
  });

  it("empty fleet", () => {
    const s = fleetSummary([]);
    expect(s.agents).toBe(0);
    expect(s.online).toBe(0);
    expect(s.errored).toBe(0);
    expect(s.running).toBe(0);
    expect(s.totalProcs).toBe(0);
    expect(s.restarts24h).toBe(0);
    expect(s.avgCpu).toBeNull();
    expect(s.memUsed).toBeNull();
    expect(s.memTotal).toBeNull();
    expect(s.erroredName).toBeNull();
  });
});
