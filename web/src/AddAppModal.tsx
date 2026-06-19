import { useState, useEffect, type FormEvent } from "react";
import { Agent, CommandSource, GitSource, CredentialMeta, addApp, listCredentials } from "./api";

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
  const [sourceType, setSourceType] = useState<"command" | "git">("command");
  const [name, setName] = useState("");
  const [cmd, setCmd] = useState("");
  const [args, setArgs] = useState("");
  const [cwd, setCwd] = useState("");
  const [repo, setRepo] = useState("");
  const [ref, setRef] = useState("");
  const [build, setBuild] = useState("");
  const [subdir, setSubdir] = useState("");
  const [instances, setInstances] = useState("");
  const [showAdv, setShowAdv] = useState(false);
  const [env, setEnv] = useState<EnvRow[]>([]);
  const [restart, setRestart] = useState("always");
  const [maxRestarts, setMaxRestarts] = useState("");
  const [killTimeout, setKillTimeout] = useState("");
  const [creds, setCreds] = useState<CredentialMeta[]>([]);
  const [credential, setCredential] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");

  useEffect(() => { listCredentials().then(setCreds); }, []);

  const canSubmit =
    agent !== "" &&
    name.trim() !== "" &&
    cmd.trim() !== "" &&
    (sourceType === "command" || repo.trim() !== "") &&
    !busy;

  async function submit(e: FormEvent) {
    e.preventDefault();
    if (!canSubmit) return;
    setBusy(true);
    setError("");
    const argList = args.split(/\s+/).map((s) => s.trim()).filter(Boolean);
    const envMap: Record<string, string> = {};
    for (const row of env) if (row.key.trim()) envMap[row.key.trim()] = row.value;

    let source: CommandSource | GitSource;
    if (sourceType === "git") {
      const gs: GitSource = { type: "git", name: name.trim(), cmd: cmd.trim(), repo: repo.trim() };
      if (argList.length) gs.args = argList;
      if (instances.trim()) gs.instances = Number(instances);
      if (Object.keys(envMap).length) gs.env = envMap;
      if (restart !== "always") gs.restart = restart;
      if (ref.trim()) gs.ref = ref.trim();
      if (build.trim()) gs.build = build.trim();
      if (subdir.trim()) gs.subdir = subdir.trim();
      if (credential) gs.credential = credential;
      source = gs;
    } else {
      const cs: CommandSource = { type: "command", name: name.trim(), cmd: cmd.trim() };
      if (argList.length) cs.args = argList;
      if (cwd.trim()) cs.cwd = cwd.trim();
      if (instances.trim()) cs.instances = Number(instances);
      if (Object.keys(envMap).length) cs.env = envMap;
      if (restart !== "always") cs.restart = restart;
      if (maxRestarts.trim()) cs.max_restarts = Number(maxRestarts);
      if (killTimeout.trim()) cs.kill_timeout = killTimeout.trim();
      source = cs;
    }

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

        <div className="source-toggle seg">
          <button
            type="button"
            className={sourceType === "command" ? "active" : ""}
            onClick={() => setSourceType("command")}
          >
            command
          </button>
          <button
            type="button"
            className={sourceType === "git" ? "active" : ""}
            onClick={() => setSourceType("git")}
          >
            from git
          </button>
        </div>

        <label className="field">
          name
          <input value={name} onChange={(e) => setName(e.target.value)} placeholder="web" />
        </label>

        {sourceType === "git" && (
          <label className="field">
            repo
            <input value={repo} onChange={(e) => setRepo(e.target.value)} placeholder="https://github.com/user/repo" />
          </label>
        )}
        {sourceType === "git" && (
          <label className="field">
            ref
            <input value={ref} onChange={(e) => setRef(e.target.value)} placeholder="default branch" />
          </label>
        )}
        {sourceType === "git" && (
          <label className="field">
            build command
            <input value={build} onChange={(e) => setBuild(e.target.value)} placeholder="auto-detect" />
          </label>
        )}
        {sourceType === "git" && (
          <label className="field">
            credential
            <select value={credential} onChange={(e) => setCredential(e.target.value)}>
              <option value="">None (public repo / host auth)</option>
              {creds.map((c) => (
                <option key={c.name} value={c.name}>{c.name} ({c.username})</option>
              ))}
            </select>
          </label>
        )}

        <label className="field">
          {sourceType === "git" ? "start command" : "command"}
          <input value={cmd} onChange={(e) => setCmd(e.target.value)} placeholder="/usr/bin/node" />
        </label>
        <label className="field">
          args
          <input value={args} onChange={(e) => setArgs(e.target.value)} placeholder="server.js --port 3000" />
        </label>
        {sourceType === "command" && (
          <label className="field">
            working dir
            <input value={cwd} onChange={(e) => setCwd(e.target.value)} placeholder="/srv/app" />
          </label>
        )}
        <label className="field">
          instances
          <input value={instances} onChange={(e) => setInstances(e.target.value)} placeholder="1" inputMode="numeric" />
        </label>

        <button type="button" className="adv-toggle" onClick={() => setShowAdv((v) => !v)}>
          {showAdv ? "▾" : "▸"} advanced
        </button>
        {showAdv && (
          <div className="adv">
            {sourceType === "git" && (
              <label className="field">
                subdir
                <input value={subdir} onChange={(e) => setSubdir(e.target.value)} placeholder="packages/server" />
              </label>
            )}
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
            {sourceType === "command" && (
              <>
                <label className="field">
                  max restarts
                  <input value={maxRestarts} onChange={(e) => setMaxRestarts(e.target.value)} placeholder="16" inputMode="numeric" />
                </label>
                <label className="field">
                  kill timeout
                  <input value={killTimeout} onChange={(e) => setKillTimeout(e.target.value)} placeholder="5s" />
                </label>
              </>
            )}
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
