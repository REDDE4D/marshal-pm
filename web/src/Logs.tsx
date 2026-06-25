import { useEffect, useState } from "react";
import { Agent, getFleet } from "./api";
import { LogPanel } from "./LogPanel";
import { SectionHeader } from "./components/Ledger";

export function Logs({ agent: initAgent, proc: initProc }: { agent?: string; proc?: string }) {
  const [fleet, setFleet] = useState<Agent[]>([]);
  const [loading, setLoading] = useState(true);
  const [selectedAgent, setSelectedAgent] = useState(initAgent ?? "");
  const [selectedProc, setSelectedProc] = useState(initProc ?? "");

  // One-shot fleet fetch (then poll every 10s for updates)
  useEffect(() => {
    let stop = false;
    async function load() {
      try {
        const agents = await getFleet();
        if (stop) return;
        setFleet(agents);

        // Set defaults if no selection yet
        setSelectedAgent((prev) => {
          if (prev) return prev;
          const first = agents.find((a) => a.connected);
          return first?.name ?? agents[0]?.name ?? "";
        });
      } catch {
        /* best-effort */
      } finally {
        if (!stop) setLoading(false);
      }
    }
    load();
    const id = setInterval(load, 10000);
    return () => { stop = true; clearInterval(id); };
  }, []);

  // When agent selection changes, default proc to first proc of that agent
  useEffect(() => {
    if (!selectedAgent) return;
    const a = fleet.find((x) => x.name === selectedAgent);
    if (!a) return;
    setSelectedProc((prev) => {
      // Only auto-set if no proc was provided initially or if we switched agent
      if (prev && a.procs.some((p) => p.name === prev)) return prev;
      return a.procs[0]?.name ?? "";
    });
  }, [selectedAgent, fleet]);

  const agentObj = fleet.find((a) => a.name === selectedAgent);
  const procs = agentObj?.procs ?? [];

  const title = selectedAgent && selectedProc
    ? `${selectedAgent} / ${selectedProc}`
    : selectedAgent || "—";

  if (loading) {
    return <div className="chart-empty">Loading…</div>;
  }

  if (fleet.length === 0) {
    return <div className="chart-empty">No agents connected.</div>;
  }

  return (
    <>
      <div className="crumb">
        <b>Logs</b>
        {title && <><span className="s">·</span> <span style={{ color: "var(--dim)" }}>{title}</span></>}
      </div>

      {/* Agent + proc selectors */}
      <div className="logbar" style={{ marginBottom: 0, borderBottom: "none" }}>
        <select
          className="inp"
          value={selectedAgent}
          onChange={(e) => {
            setSelectedAgent(e.target.value);
            setSelectedProc("");
          }}
          style={{ minWidth: 140 }}
        >
          {fleet.map((a) => (
            <option key={a.name} value={a.name}>
              {a.name}{a.connected ? "" : " (off)"}
            </option>
          ))}
        </select>
        <select
          className="inp"
          value={selectedProc}
          onChange={(e) => setSelectedProc(e.target.value)}
          style={{ minWidth: 140 }}
          disabled={procs.length === 0}
          title={procs.length === 0 ? "No processes for this agent" : undefined}
        >
          {procs.length === 0
            ? <option value="">—</option>
            : procs.map((p) => (
              <option key={p.name} value={p.name}>{p.name}</option>
            ))
          }
        </select>
      </div>

      <SectionHeader index="01" title="Live log" />

      {selectedAgent && selectedProc ? (
        <LogPanel agent={selectedAgent} proc={selectedProc} />
      ) : (
        <div className="chart-empty">Select an agent and process to view logs.</div>
      )}
    </>
  );
}
