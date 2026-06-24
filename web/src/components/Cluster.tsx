import { ReactNode } from "react";

type CellColor = "teal" | "indigo" | "olive" | "amber" | "rose" | "sky";

interface CellProps {
  label: string;
  value: ReactNode;
  unit?: string;
  sub?: ReactNode;
  color?: CellColor;
}

export function Cell({ label, value, unit, sub, color }: CellProps) {
  return (
    <div className="cell">
      <div className="l">{label}</div>
      <div className={"v" + (color ? ` ${color}` : "")}>
        {value}
        {unit && <small>{unit}</small>}
      </div>
      {sub && <div className="d">{sub}</div>}
    </div>
  );
}

interface MetricClusterProps {
  cols: 4 | 6;
  children: ReactNode;
}

export function MetricCluster({ cols, children }: MetricClusterProps) {
  return <div className={"cluster c" + cols}>{children}</div>;
}
