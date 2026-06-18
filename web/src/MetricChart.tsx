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
  const color = metric === "cpu" ? "#2DD4BF" : "#5B6BD8";

  return (
    <svg width={W} height={H} viewBox={`0 0 ${W} ${H}`} className="metric-chart" role="img">
      {/* Y gridlines + labels at lo and hi */}
      <line x1={PAD} y1={y(hi)} x2={W - PAD} y2={y(hi)} className="grid" />
      <line x1={PAD} y1={y(lo)} x2={W - PAD} y2={y(lo)} className="grid" />
      <text x={4} y={y(hi) + 4} className="axis">{fmt(metric, hi)}</text>
      <text x={4} y={y(lo) + 4} className="axis">{fmt(metric, lo)}</text>
      {/* max series (faint), then avg series */}
      <polyline points={line(max)} fill="none" stroke={color} strokeWidth={1} opacity={0.35} />
      <polyline points={line(avg)} fill="none" stroke={color} strokeWidth={1.75} />
    </svg>
  );
}
