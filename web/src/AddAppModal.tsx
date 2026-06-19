import { useState, type FormEvent } from "react";
import { Agent, CommandSource, addApp } from "./api";

type EnvRow = { key: string; value: string };

export function AddAppModal({
  agents,
  onClose,
  onAdded,
}: {
  agents: Agent[];
  onClose: () => void;
  onAdded: () => void;
}) {
  const connected = agents.filter((a) => a.connected);
  const [agent, setAgent] = useState(connected.length === 1 ? connected[0].name : "");
  const [name, setName] = useState("");
  const [cmd, setCmd] = useState("");
  const [args, setArgs] = useState("");
  const [cwd, setCwd] = useState("");
  const [instances, setInstances] = useState("");
  const [showAdv, setShowAdv] = useState(false);
  const [env, setEnv] = useState<EnvRow[]>([]);
  const [restart, setRestart] = useState("always");
  const [maxRestarts, setMaxRestarts] = useState("");
  const [killTimeout, setKillTimeout] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");

  const canSubmit = agent !== "" && name.trim() !== "" && cmd.trim() !== "" && !busy;

  async function submit(e: FormEvent) {
    e.preventDefault();
    if (!canSubmit) return;
    setBusy(true);
    setError("");
    const source: CommandSource = { type: "command", name: name.trim(), cmd: cmd.trim() };
    const argList = args.split(/\s+/).map((s) => s.trim()).filter(Boolean);
    if (argList.length) source.args = argList;
    if (cwd.trim()) source.cwd = cwd.trim();
    if (instances.trim()) source.instances = Number(instances);
    const envMap: Record<string, string> = {};
    for (const row of env) if (row.key.trim()) envMap[row.key.trim()] = row.value;
    if (Object.keys(envMap).length) source.env = envMap;
    if (restart !== "always") source.restart = restart;
    if (maxRestarts.trim()) source.max_restarts = Number(maxRestarts);
    if (killTimeout.trim()) source.kill_timeout = killTimeout.trim();
    const res = await addApp(agent, source);
    setBusy(false);
    if (res.ok) {
      onAdded();
      onClose();
    } else {
      setError(res.error || "error");
    }
  }

  return (
    <div className="modal-backdrop" onClick={onClose}>
      <form className="modal" onClick={(e) => e.stopPropagation()} onSubmit={submit}>
        <div className="modal-head">
          <span className="modal-title">add app</span>
          <button type="button" className="ctl-btn" onClick={onClose}>✕</button>
        </div>

        <label className="field">
          target agent
          {connected.length === 0 ? (
            <span className="hint">no agents connected</span>
          ) : (
            <select value={agent} onChange={(e) => setAgent(e.target.value)}>
              <option value="" disabled>
                select…
              </option>
              {connected.map((a) => (
                <option key={a.name} value={a.name}>
                  {a.name}
                </option>
              ))}
            </select>
          )}
        </label>

        <label className="field">
          name
          <input value={name} onChange={(e) => setName(e.target.value)} placeholder="web" />
        </label>
        <label className="field">
          command
          <input value={cmd} onChange={(e) => setCmd(e.target.value)} placeholder="/usr/bin/node" />
        </label>
        <label className="field">
          args
          <input value={args} onChange={(e) => setArgs(e.target.value)} placeholder="server.js --port 3000" />
        </label>
        <label className="field">
          working dir
          <input value={cwd} onChange={(e) => setCwd(e.target.value)} placeholder="/srv/app" />
        </label>
        <label className="field">
          instances
          <input value={instances} onChange={(e) => setInstances(e.target.value)} placeholder="1" inputMode="numeric" />
        </label>

        <button type="button" className="adv-toggle" onClick={() => setShowAdv((v) => !v)}>
          {showAdv ? "▾" : "▸"} advanced
        </button>
        {showAdv && (
          <div className="adv">
            <div className="field">
              env
              {env.map((row, i) => (
                <div className="env-row" key={i}>
                  <input
                    placeholder="KEY"
                    value={row.key}
                    onChange={(e) =>
                      setEnv((rs) => rs.map((r, j) => (j === i ? { ...r, key: e.target.value } : r)))
                    }
                  />
                  <input
                    placeholder="value"
                    value={row.value}
                    onChange={(e) =>
                      setEnv((rs) => rs.map((r, j) => (j === i ? { ...r, value: e.target.value } : r)))
                    }
                  />
                  <button type="button" className="ctl-btn" onClick={() => setEnv((rs) => rs.filter((_, j) => j !== i))}>
                    ✕
                  </button>
                </div>
              ))}
              <button type="button" className="ctl-btn" onClick={() => setEnv((rs) => [...rs, { key: "", value: "" }])}>
                + env var
              </button>
            </div>
            <label className="field">
              restart
              <select value={restart} onChange={(e) => setRestart(e.target.value)}>
                <option value="always">always</option>
                <option value="on-failure">on-failure</option>
                <option value="no">no</option>
              </select>
            </label>
            <label className="field">
              max restarts
              <input value={maxRestarts} onChange={(e) => setMaxRestarts(e.target.value)} placeholder="16" inputMode="numeric" />
            </label>
            <label className="field">
              kill timeout
              <input value={killTimeout} onChange={(e) => setKillTimeout(e.target.value)} placeholder="5s" />
            </label>
          </div>
        )}

        {error && <div className="modal-error">{error}</div>}
        <div className="modal-foot">
          <button type="button" className="btn" onClick={onClose}>
            cancel
          </button>
          <button type="submit" className="btn primary" disabled={!canSubmit}>
            {busy ? "adding…" : "add app"}
          </button>
        </div>
      </form>
    </div>
  );
}
