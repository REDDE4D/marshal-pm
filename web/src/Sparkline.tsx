type SparklineProps = {
  points: number[];
  width?: number;
  height?: number;
  color?: string;
};

export function Sparkline({
  points,
  width = 80,
  height = 20,
  color = "#4ade80",
}: SparklineProps) {
  if (points.length === 0) {
    return <svg width={width} height={height} className="sparkline" aria-label="no data" />;
  }
  const min = Math.min(...points);
  const max = Math.max(...points);
  const span = max - min || 1;
  const stepX = points.length > 1 ? width / (points.length - 1) : 0;
  const coords = points.map((v, i) => {
    const x = i * stepX;
    const y = height - ((v - min) / span) * height;
    return `${x.toFixed(1)},${y.toFixed(1)}`;
  });
  return (
    <svg
      width={width}
      height={height}
      viewBox={`0 0 ${width} ${height}`}
      className="sparkline"
      preserveAspectRatio="none"
      role="img"
    >
      <polyline points={coords.join(" ")} fill="none" stroke={color} strokeWidth={1.5} />
    </svg>
  );
}
