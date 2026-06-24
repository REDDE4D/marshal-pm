import { useState } from "react";
import { control } from "./api";

// RestartAllButton restarts every app on one agent via the existing restart op
// with the "all" selector. Confirm-then-fire, mirroring ControlButtons.
export function RestartAllButton({ agent, connected }: { agent: string; connected: boolean }) {
  const [pending, setPending] = useState(false);
  const [busy, setBusy] = useState(false);
  const [msg, setMsg] = useState("");

  async function fire() {
    setPending(false); setBusy(true); setMsg("");
    const res = await control(agent, "all", "restart");
    setBusy(false);
    setMsg(res.ok ? "✓" : res.error || "error");
    window.setTimeout(() => setMsg(""), 4000);
  }

  if (busy) return <span className="ctl"><span className="ctl-msg">…</span></span>;
  if (pending) {
    return (
      <span className="ctl">
        <button className="ctl-confirm" onClick={fire}>confirm restart all</button>
        <button className="ctl-btn" onClick={() => setPending(false)}>✕</button>
      </span>
    );
  }
  return (
    <span className="ctl">
      <button className="ctl-btn" disabled={!connected} onClick={() => setPending(true)}>restart all</button>
      {msg && <span className="ctl-msg">{msg}</span>}
    </span>
  );
}
