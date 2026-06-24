import { useState } from "react";
import { control } from "./api";

// Backend ops are restart/stop. The UI "start" (shown when a process is
// stopped/errored) issues a restart, which revives the managed process.
type Op = "restart" | "stop" | "reload";

export function ControlButtons({ agent, proc, state, connected }: { agent: string; proc: string; state: string; connected: boolean }) {
  const [pending, setPending] = useState<{ op: Op; label: string } | null>(null);
  const [busy, setBusy] = useState(false);
  const [msg, setMsg] = useState("");
  const running = !["stopped", "errored"].includes(state);

  async function fire(op: Op) {
    setPending(null); setBusy(true); setMsg("");
    const res = await control(agent, proc, op);
    setBusy(false);
    setMsg(res.ok ? "✓" : res.error || "error");
    window.setTimeout(() => setMsg(""), 4000);
  }
  function ask(op: Op, label: string) {
    setMsg(""); setPending({ op, label });
    window.setTimeout(() => setPending((p) => (p?.op === op ? null : p)), 3000);
  }

  if (busy) return <span className="ctl"><span className="ctl-msg">…</span></span>;
  if (pending) {
    return (
      <span className="ctl" onClick={(e) => { e.stopPropagation(); e.preventDefault(); }}>
        <button className="ctl-confirm" onClick={() => fire(pending.op)}>confirm {pending.label}</button>
        <button className="ctl-btn" onClick={() => setPending(null)}>✕</button>
      </span>
    );
  }
  return (
    <span className="ctl" onClick={(e) => { e.stopPropagation(); e.preventDefault(); }}>
      {/* start: revive a stopped/errored proc via restart; no confirm (it's already down) */}
      <button className="ctl-btn" disabled={!connected || running} onClick={() => fire("restart")}>start</button>
      <button className="ctl-btn" disabled={!connected || !running} onClick={() => ask("restart", "restart")}>restart</button>
      <button className="ctl-btn" disabled={!connected || !running} onClick={() => ask("reload", "reload")}>reload</button>
      <button className="ctl-btn danger" disabled={!connected || !running} onClick={() => ask("stop", "stop")}>stop</button>
      {msg && <span className="ctl-msg">{msg}</span>}
    </span>
  );
}
