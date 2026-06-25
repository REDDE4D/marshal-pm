import { useState, useEffect, type FormEvent } from "react";
import { Agent, CommandSource, GitSource, CredentialMeta, addApp, listCredentials } from "./api";
import { Modal } from "./components/Modal";
import { Field, Input, Button, Segment } from "./components/Controls";

type EnvRow = { key: string; value: string };

const FORM_ID = "add-app-form";

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

  const disabledReason =
    agent === "" ? "Select an agent" :
    name.trim() === "" ? "Enter a name" :
    cmd.trim() === "" ? "Enter a command" :
    (sourceType === "git" && repo.trim() === "") ? "Enter a repo URL" :
    busy ? "Submitting…" :
    undefined;

  const footer = (
    <>
      <Button variant="ghost" type="button" onClick={onClose}>cancel</Button>
      <Button type="submit" form={FORM_ID} disabledReason={disabledReason}>
        {busy ? "adding…" : "add app"}
      </Button>
    </>
  );

  return (
    <Modal title="Add application" onClose={onClose} footer={footer}>
      <form id={FORM_ID} onSubmit={submit}>
        {error && <div className="modal-error">{error}</div>}
        <Field label="target agent">
          {connected.length === 0 ? (
            <span className="hint">no agents connected</span>
          ) : (
            <select className="inp" value={agent} onChange={(e) => setAgent(e.target.value)}>
              <option value="" disabled>select…</option>
              {connected.map((a) => (
                <option key={a.name} value={a.name}>{a.name}</option>
              ))}
            </select>
          )}
        </Field>

        <Field label="source">
          <Segment<"command" | "git">
            options={[
              { value: "command", label: "command" },
              { value: "git", label: "from git" },
            ]}
            value={sourceType}
            onChange={setSourceType}
          />
        </Field>

        <Field label="name">
          <Input value={name} onChange={(e) => setName(e.target.value)} placeholder="web" />
        </Field>

        {sourceType === "git" && (
          <Field label="repo" required hint="Full HTTPS or SSH URL of the repository to clone">
            <Input value={repo} onChange={(e) => setRepo(e.target.value)} placeholder="https://github.com/user/repo" />
          </Field>
        )}
        {sourceType === "git" && (
          <Field label="ref" hint="Branch, tag, or commit to check out — leave blank for the default branch">
            <Input value={ref} onChange={(e) => setRef(e.target.value)} placeholder="default branch" />
          </Field>
        )}
        {sourceType === "git" && (
          <Field label="build command" hint="Runs once after clone to build the project — leave blank to auto-detect">
            <Input value={build} onChange={(e) => setBuild(e.target.value)} placeholder="auto-detect" />
          </Field>
        )}
        {sourceType === "git" && (
          <Field label="credential">
            <select className="inp" value={credential} onChange={(e) => setCredential(e.target.value)}>
              <option value="">None (public repo / host auth)</option>
              {creds.map((c) => (
                <option key={c.name} value={c.name}>{c.name} ({c.username})</option>
              ))}
            </select>
          </Field>
        )}

        <Field label={sourceType === "git" ? "start command" : "command"}>
          <Input value={cmd} onChange={(e) => setCmd(e.target.value)} placeholder="/usr/bin/node" />
        </Field>
        <Field label="args">
          <Input value={args} onChange={(e) => setArgs(e.target.value)} placeholder="server.js --port 3000" />
        </Field>
        {sourceType === "command" && (
          <Field label="working dir">
            <Input value={cwd} onChange={(e) => setCwd(e.target.value)} placeholder="/srv/app" />
          </Field>
        )}
        <Field label="instances">
          <Input value={instances} onChange={(e) => setInstances(e.target.value)} placeholder="1" inputMode="numeric" />
        </Field>

        <button type="button" className="adv-toggle" onClick={() => setShowAdv((v) => !v)}>
          {showAdv ? "▾" : "▸"} advanced
        </button>
        {showAdv && (
          <div className="adv">
            {sourceType === "git" && (
              <Field label="subdir">
                <Input value={subdir} onChange={(e) => setSubdir(e.target.value)} placeholder="packages/server" />
              </Field>
            )}
            <div className="field">
              <label>env</label>
              {env.map((row, i) => (
                <div className="env-row" key={i}>
                  <Input
                    placeholder="KEY"
                    value={row.key}
                    onChange={(e) =>
                      setEnv((rs) => rs.map((r, j) => (j === i ? { ...r, key: e.target.value } : r)))
                    }
                  />
                  <Input
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
            <Field label="restart">
              <select className="inp" value={restart} onChange={(e) => setRestart(e.target.value)}>
                <option value="always">always</option>
                <option value="on-failure">on-failure</option>
                <option value="no">no</option>
              </select>
            </Field>
            {sourceType === "command" && (
              <>
                <Field label="max restarts">
                  <Input value={maxRestarts} onChange={(e) => setMaxRestarts(e.target.value)} placeholder="16" inputMode="numeric" />
                </Field>
                <Field label="kill timeout">
                  <Input value={killTimeout} onChange={(e) => setKillTimeout(e.target.value)} placeholder="5s" />
                </Field>
              </>
            )}
          </div>
        )}

      </form>
    </Modal>
  );
}
