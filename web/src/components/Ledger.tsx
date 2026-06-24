import { ReactNode, MouseEvent } from "react";

export type QuickAction = {
  icon: string;
  label: string;
  variant?: "warn" | "dgr";
  onClick: (e: MouseEvent) => void;
  title?: string;
};

function QuickActions({ actions }: { actions: QuickAction[] }) {
  return (
    <div className="qa">
      {actions.map((a, i) => (
        <button
          key={i}
          className={"qbtn" + (a.variant ? ` ${a.variant}` : "")}
          title={a.title}
          aria-label={a.label}
          onClick={(e) => {
            e.stopPropagation();
            a.onClick(e);
          }}
        >
          <span className="qi">{a.icon}</span> {a.label}
        </button>
      ))}
    </div>
  );
}

export function SectionHeader({
  index,
  title,
  right,
  count,
}: {
  index: string;
  title: string;
  right?: ReactNode;
  count?: ReactNode;
}) {
  return (
    <div className="sec">
      <span className="ix">{index}</span>
      <span className="t">{title}</span>
      <span className="rule"></span>
      {right}
      {count != null && <span className="ct">{count}</span>}
    </div>
  );
}

export function LedgerHeader({
  cols,
  children,
}: {
  cols: string;
  children: ReactNode;
}) {
  return (
    <div className="lh" style={{ gridTemplateColumns: cols }}>
      {children}
    </div>
  );
}

export function LedgerRow({
  cols,
  onClick,
  actions,
  children,
}: {
  cols: string;
  onClick?: (e: MouseEvent<HTMLDivElement>) => void;
  actions?: QuickAction[];
  children: ReactNode;
}) {
  return (
    <div
      className={"lr" + (onClick ? " clk" : "")}
      style={{ gridTemplateColumns: cols }}
      onClick={onClick}
    >
      {children}
      {actions && <QuickActions actions={actions} />}
    </div>
  );
}
