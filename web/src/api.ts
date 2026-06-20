export type Proc = {
  name: string;
  state: string;
  pid: number;
  uptime_ms: number;
  restarts: number;
  cpu: number;
  mem: number;
  source?: "command" | "git";
  detail?: string;
  credential?: string;
};

export type Agent = {
  name: string;
  connected: boolean;
  last_seen_unix: number;
  procs: Proc[];
};

export async function getSession(): Promise<string | null> {
  const r = await fetch("/api/session");
  if (r.status === 200) {
    const j = await r.json();
    return j.user as string;
  }
  return null;
}

export async function login(user: string, pass: string): Promise<boolean> {
  const r = await fetch("/api/login", {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ User: user, Pass: pass }),
  });
  return r.status === 200;
}

export async function logout(): Promise<void> {
  await fetch("/api/logout", { method: "POST" });
}

export async function getFleet(): Promise<Agent[]> {
  const r = await fetch("/api/fleet");
  if (r.status === 401) throw new Error("unauthorized");
  return (await r.json()) as Agent[];
}

export type Bucket = {
  ts: number;
  cpu_avg: number;
  cpu_max: number;
  mem_avg: number;
  mem_max: number;
};

export type ProcMetrics = { name: string; buckets: Bucket[] };
export type AgentMetrics = { agent: string; procs: ProcMetrics[] };

export async function getMetrics(sinceMs: number): Promise<AgentMetrics[]> {
  const r = await fetch(`/api/metrics?since=${sinceMs}`);
  if (r.status === 401) throw new Error("unauthorized");
  return (await r.json()) as AgentMetrics[];
}

export async function getMetricsForProc(
  agent: string,
  selector: string,
  sinceMs: number,
  bucketMs: number,
): Promise<AgentMetrics[]> {
  const q = new URLSearchParams({
    agent,
    selector,
    since: String(sinceMs),
    bucket: String(bucketMs),
  });
  const r = await fetch(`/api/metrics?${q.toString()}`);
  if (r.status === 401) throw new Error("unauthorized");
  return (await r.json()) as AgentMetrics[];
}

export type LogLine = {
  ts: number;
  name: string;
  instance: number;
  stderr: boolean;
  text: string;
};

export type LogsResponse = { cursor: number; lines: LogLine[] };

export async function getLogs(
  agent: string,
  selector: string,
  opts: { stream: string; limit: number; after: number; q: string },
): Promise<LogsResponse> {
  const q = new URLSearchParams({
    agent,
    selector,
    stream: opts.stream,
    limit: String(opts.limit),
    after: String(opts.after),
    q: opts.q,
  });
  const r = await fetch(`/api/logs?${q.toString()}`);
  if (r.status === 401) throw new Error("unauthorized");
  return (await r.json()) as LogsResponse;
}

export async function getLogStats(agent: string): Promise<Record<string, number>> {
  const r = await fetch(`/api/logstats?agent=${encodeURIComponent(agent)}`);
  if (r.status === 401) throw new Error("unauthorized");
  const j = (await r.json()) as { counts: Record<string, number> };
  return j.counts ?? {};
}

export type ControlResult = { ok: boolean; error?: string };

// control posts a Restart/Stop/Delete and surfaces server errors as values — it
// never throws, so a failed control call cannot trigger a logout (only the fleet
// poll owns auth). 200 -> the agent's result; 400/502 -> {ok:false,error}.
export async function control(
  agent: string,
  selector: string,
  action: "restart" | "stop" | "delete",
): Promise<ControlResult> {
  const r = await fetch("/api/control", {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ agent, selector, action }),
  });
  if (r.status === 200) return (await r.json()) as ControlResult;
  try {
    const j = await r.json();
    return { ok: false, error: (j.error as string) ?? `error ${r.status}` };
  } catch {
    return { ok: false, error: `error ${r.status}` };
  }
}

// CommandSource mirrors the backend "command" app source (maps 1:1 to AppSpec).
// Only name and cmd are required; omitted fields use backend defaults.
export type CommandSource = {
  type: "command";
  name: string;
  cmd: string;
  args?: string[];
  cwd?: string;
  instances?: number;
  env?: Record<string, string>;
  restart?: string;
  max_restarts?: number;
  kill_timeout?: string;
};

// GitSource mirrors the backend "git" app source. repo and cmd are required;
// all other fields are optional and use backend defaults.
export interface GitSource {
  type: "git";
  name: string;
  cmd: string;
  args?: string[];
  instances?: number;
  env?: Record<string, string>;
  restart?: string;
  repo: string;
  ref?: string;
  build?: string;
  subdir?: string;
  credential?: string;
}

// addApp creates a new app on an agent via POST /api/apps. Like control() it
// never throws — server errors surface as {ok:false,error}, so a failed add
// cannot trigger a logout (only the fleet poll owns auth).
export async function addApp(agent: string, source: CommandSource | GitSource): Promise<ControlResult> {
  const r = await fetch("/api/apps", {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ agent, source }),
  });
  if (r.status === 200) return (await r.json()) as ControlResult;
  try {
    const j = await r.json();
    return { ok: false, error: (j.error as string) ?? `error ${r.status}` };
  } catch {
    return { ok: false, error: `error ${r.status}` };
  }
}

// redeploy triggers a git re-clone and rebuild for an existing git app.
// Throws on 401 like control() (the fleet poll owns auth); all other failures
// surface as {ok:false,error} so callers never get an unhandled rejection.
export async function redeploy(
  agent: string,
  name: string,
  credential?: string,
): Promise<{ ok: boolean; error?: string }> {
  const res = await fetch("/api/apps/redeploy", {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ agent, name, credential }),
  });
  if (res.status === 401) throw new Error("error 401");
  try {
    return (await res.json()) as { ok: boolean; error?: string };
  } catch {
    return { ok: false, error: `error ${res.status}` };
  }
}

export interface CredentialMeta {
  name: string;
  type: string;
  username: string;
  created_at: number;
}

export async function listCredentials(): Promise<CredentialMeta[]> {
  const r = await fetch("/api/credentials");
  if (r.status !== 200) return [];
  return (await r.json()) as CredentialMeta[];
}

export async function createCredential(
  name: string,
  username: string,
  token: string,
): Promise<{ ok: boolean; error?: string }> {
  const r = await fetch("/api/credentials", {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ name, username, token }),
  });
  if (r.status === 201 || r.status === 200) return { ok: true };
  try {
    const j = await r.json();
    return { ok: false, error: (j.error as string) ?? `error ${r.status}` };
  } catch {
    return { ok: false, error: `error ${r.status}` };
  }
}

export async function deleteCredential(name: string): Promise<{ ok: boolean; error?: string }> {
  const r = await fetch(`/api/credentials/${encodeURIComponent(name)}`, { method: "DELETE" });
  if (r.status === 204) return { ok: true };
  return { ok: false, error: `error ${r.status}` };
}

export type DirEntry = { name: string; is_dir: boolean; size: number; mod_unix: number; mode: number };
export type DirListing = { path: string; entries: DirEntry[] };
export type FileContent = { path: string; content: string; size: number; truncated: boolean; binary: boolean };

export async function listDir(agent: string, app: string, path: string): Promise<DirListing> {
  const q = new URLSearchParams({ path });
  const r = await fetch(`/api/fleet/${encodeURIComponent(agent)}/apps/${encodeURIComponent(app)}/dir?${q}`);
  if (r.status === 401) throw new Error("unauthorized");
  if (!r.ok) throw new Error((await r.json().catch(() => ({}))).error || `dir failed (${r.status})`);
  return r.json();
}

export async function readFile(agent: string, app: string, path: string): Promise<FileContent> {
  const q = new URLSearchParams({ path });
  const r = await fetch(`/api/fleet/${encodeURIComponent(agent)}/apps/${encodeURIComponent(app)}/file?${q}`);
  if (r.status === 401) throw new Error("unauthorized");
  if (!r.ok) throw new Error((await r.json().catch(() => ({}))).error || `file failed (${r.status})`);
  return r.json();
}

export function fileDownloadURL(agent: string, app: string, path: string): string {
  const q = new URLSearchParams({ path, raw: "1" });
  return `/api/fleet/${encodeURIComponent(agent)}/apps/${encodeURIComponent(app)}/file?${q}`;
}

export type CommitResult = { sha: string; branch: string };

export async function writeFile(
  agent: string, app: string, path: string, content: string, message: string, credential?: string,
): Promise<CommitResult> {
  const q = new URLSearchParams({ path });
  const r = await fetch(`/api/fleet/${encodeURIComponent(agent)}/apps/${encodeURIComponent(app)}/file?${q}`, {
    method: "PUT",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ content, message, credential: credential || "" }),
  });
  if (r.status === 401) throw new Error("unauthorized");
  if (!r.ok) throw new Error((await r.json().catch(() => ({}))).error || `save failed (${r.status})`);
  return r.json();
}

export async function createFile(
  agent: string, app: string, path: string, content: string, message: string, credential?: string,
): Promise<CommitResult> {
  const q = new URLSearchParams({ path, create: "1" });
  const r = await fetch(`/api/fleet/${encodeURIComponent(agent)}/apps/${encodeURIComponent(app)}/file?${q}`, {
    method: "PUT",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ content, message, credential: credential || "" }),
  });
  if (r.status === 401) throw new Error("unauthorized");
  if (!r.ok) throw new Error((await r.json().catch(() => ({}))).error || `save failed (${r.status})`);
  return r.json();
}

export async function deleteFile(
  agent: string, app: string, path: string, message: string, credential?: string,
): Promise<CommitResult> {
  const q = new URLSearchParams({ path });
  const r = await fetch(`/api/fleet/${encodeURIComponent(agent)}/apps/${encodeURIComponent(app)}/file?${q}`, {
    method: "DELETE",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ message, credential: credential || "" }),
  });
  if (r.status === 401) throw new Error("unauthorized");
  if (!r.ok) throw new Error((await r.json().catch(() => ({}))).error || `delete failed (${r.status})`);
  return r.json();
}

export async function renameFile(
  agent: string, app: string, from: string, to: string, message: string, credential?: string,
): Promise<CommitResult> {
  const r = await fetch(`/api/fleet/${encodeURIComponent(agent)}/apps/${encodeURIComponent(app)}/rename`, {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ from, to, message, credential: credential || "" }),
  });
  if (r.status === 401) throw new Error("unauthorized");
  if (!r.ok) throw new Error((await r.json().catch(() => ({}))).error || `rename failed (${r.status})`);
  return r.json();
}
