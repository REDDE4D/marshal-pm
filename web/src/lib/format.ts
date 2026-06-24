// relativeTime renders a compact "ago"/duration string from a unix-seconds
// timestamp. 0/absent → em dash. nowSec defaults to current time (inject for tests).
export function relativeTime(unixSec: number, nowSec: number = Date.now() / 1000): string {
  if (!unixSec) return "—";
  let s = Math.max(0, Math.floor(nowSec - unixSec));
  if (s < 60) return `${s}s`;
  if (s < 3600) return `${Math.floor(s / 60)}m`;
  if (s < 86400) return `${Math.floor(s / 3600)}h`;
  const d = Math.floor(s / 86400);
  const h = Math.floor((s % 86400) / 3600);
  return h ? `${d}d ${h}h` : `${d}d`;
}

export function formatBytes(n: number): string {
  if (n <= 0) return "0 B";
  const gb = n / 1024 ** 3;
  if (gb >= 1) return `${gb.toFixed(1)} GB`;
  const mb = n / 1024 ** 2;
  if (mb >= 1) return `${Math.round(mb)} MB`;
  const kb = n / 1024;
  if (kb >= 1) return `${Math.round(kb)} KB`;
  return `${Math.round(n)} B`;
}

export function formatDateShort(unixSec: number): string {
  return new Date(unixSec * 1000).toLocaleDateString(undefined, { month: "short", day: "numeric" });
}
