type BarSparklineProps = {
  points: number[];
  color?: string;
};

const VB_W = 120;
const VB_H = 22;

export function BarSparkline({ points, color = "var(--rose)" }: BarSparklineProps) {
  const maxVal = points.length > 0 ? Math.max(...points) : 0;

  if (points.length === 0 || maxVal === 0) {
    return (
      <svg
        width="100%"
        height={VB_H}
        viewBox={`0 0 ${VB_W} ${VB_H}`}
        preserveAspectRatio="none"
        aria-label="no data"
      />
    );
  }

  const step = VB_W / points.length;
  const barW = Math.max(1, step - 1);

  const bars = points.map((v, i) => {
    const barH = Math.round((v / maxVal) * VB_H);
    const bx = i * step;
    const by = VB_H - barH;
    return <rect key={i} x={bx} y={by} width={barW} height={barH} />;
  });

  return (
    <svg
      width="100%"
      height={VB_H}
      viewBox={`0 0 ${VB_W} ${VB_H}`}
      preserveAspectRatio="none"
      role="img"
    >
      <g fill={color}>{bars}</g>
    </svg>
  );
}
