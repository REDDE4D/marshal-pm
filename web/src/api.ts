export type Proc = {
  name: string;
  state: string;
  pid: number;
  uptime_ms: number;
  restarts: number;
  cpu: number;
  mem: number;
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
  opts: { stream: string; limit: number; after: number },
): Promise<LogsResponse> {
  const q = new URLSearchParams({
    agent,
    selector,
    stream: opts.stream,
    limit: String(opts.limit),
    after: String(opts.after),
  });
  const r = await fetch(`/api/logs?${q.toString()}`);
  if (r.status === 401) throw new Error("unauthorized");
  return (await r.json()) as LogsResponse;
}
