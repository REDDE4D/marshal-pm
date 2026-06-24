import { Bucket } from "./api";

type MetricChartProps = {
  buckets: Bucket[];
  metric: "cpu" | "mem";
};

const W = 480;
const H = 140;
const PAD = 28;

function fmt(metric: "cpu" | "mem", v: number): string {
  return metric === "cpu" ? `${(v * 100).toFixed(0)}%` : `${(v / (1024 * 1024)).toFixed(0)} MB`;
}

export function MetricChart({ buckets, metric }: MetricChartProps) {
  if (buckets.length === 0) {
    return <p className="chart-empty">No history yet.</p>;
  }
  const avg = buckets.map((b) => (metric === "cpu" ? b.cpu_avg : b.mem_avg));
  const max = buckets.map((b) => (metric === "cpu" ? b.cpu_max : b.mem_max));
  const lo = 0;
  const hi = Math.max(...max) || 1;
  const span = hi - lo || 1;
  const n = buckets.length;
  const x = (i: number) => PAD + (n > 1 ? (i * (W - 2 * PAD)) / (n - 1) : 0);
  const y = (v: number) => H - PAD - ((v - lo) / span) * (H - 2 * PAD);
  const line = (series: number[]) =>
    series.map((v, i) => `${x(i).toFixed(1)},${y(v).toFixed(1)}`).join(" ");
  const color = metric === "cpu" ? "#34D0BA" : "#8189EC";
  const gradId = `mc-grad-${metric}`;

  // Build the closed area path: avg line points + baseline closing segment
  const areaPoints = [
    ...avg.map((v, i) => `${x(i).toFixed(1)},${y(v).toFixed(1)}`),
    `${x(n - 1).toFixed(1)},${y(lo).toFixed(1)}`,
    `${x(0).toFixed(1)},${y(lo).toFixed(1)}`,
  ].join(" ");

  return (
    <svg width={W} height={H} viewBox={`0 0 ${W} ${H}`} className="metric-chart" role="img" preserveAspectRatio="none">
      <defs>
        <linearGradient id={gradId} x1="0" x2="0" y1="0" y2="1">
          <stop offset="0" stopColor={color} stopOpacity={0.35} />
          <stop offset="1" stopColor={color} stopOpacity={0} />
        </linearGradient>
      </defs>
      {/* Y gridlines + labels at lo and hi */}
      <line x1={PAD} y1={y(hi)} x2={W - PAD} y2={y(hi)} className="grid" />
      <line x1={PAD} y1={y(lo)} x2={W - PAD} y2={y(lo)} className="grid" />
      <text x={4} y={y(hi) + 4} className="axis">{fmt(metric, hi)}</text>
      <text x={4} y={y(lo) + 4} className="axis">{fmt(metric, lo)}</text>
      {/* gradient area fill under avg series */}
      <polyline fill={`url(#${gradId})`} stroke="none" points={areaPoints} />
      {/* faint max series so peak info isn't lost */}
      <polyline points={line(max)} fill="none" stroke={color} strokeWidth={1} strokeOpacity={0.35} />
      {/* avg series on top */}
      <polyline points={line(avg)} fill="none" stroke={color} strokeWidth={1.6} />
    </svg>
  );
}
