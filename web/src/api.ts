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
