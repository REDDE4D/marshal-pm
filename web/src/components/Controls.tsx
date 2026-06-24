import type { ButtonHTMLAttributes, InputHTMLAttributes, ReactNode } from "react";

// ---------------------------------------------------------------------------
// Segment<T> — segmented toggle control
// ---------------------------------------------------------------------------

interface SegmentOption<T> {
  value: T;
  label: string;
}

interface SegmentProps<T> {
  options: SegmentOption<T>[];
  value: T;
  onChange: (v: T) => void;
}

export function Segment<T>({ options, value, onChange }: SegmentProps<T>) {
  return (
    <div className="seg">
      {options.map((opt) => {
        const selected = opt.value === value;
        return (
          <span
            key={String(opt.value)}
            className={selected ? "on" : undefined}
            role="button"
            aria-pressed={selected}
            tabIndex={0}
            onClick={() => onChange(opt.value)}
            onKeyDown={(e) => {
              if (e.key === "Enter" || e.key === " ") {
                e.preventDefault();
                onChange(opt.value);
              }
            }}
          >
            {opt.label}
          </span>
        );
      })}
    </div>
  );
}

// ---------------------------------------------------------------------------
// Toggle — on/off switch, optionally with label + description
// ---------------------------------------------------------------------------

interface ToggleProps {
  on: boolean;
  onChange: (next: boolean) => void;
  label?: string;
  desc?: string;
}

export function Toggle({ on, onChange, label, desc }: ToggleProps) {
  const handleActivate = () => onChange(!on);
  const handleKeyDown = (e: React.KeyboardEvent) => {
    if (e.key === "Enter" || e.key === " ") {
      e.preventDefault();
      handleActivate();
    }
  };

  const switchEl = (
    <div
      className={`tg${on ? " on" : ""}`}
      role="switch"
      aria-checked={on}
      tabIndex={0}
      onClick={handleActivate}
      onKeyDown={handleKeyDown}
    />
  );

  if (!label && !desc) {
    return switchEl;
  }

  return (
    <div className="tgrow">
      {switchEl}
      <div className="meta">
        {label && <div className="h">{label}</div>}
        {desc && <div className="p">{desc}</div>}
      </div>
    </div>
  );
}

// ---------------------------------------------------------------------------
// Chip — toggleable tag
// ---------------------------------------------------------------------------

interface ChipProps {
  label: string;
  on: boolean;
  onClick: () => void;
}

export function Chip({ label, on, onClick }: ChipProps) {
  return (
    <span
      className={`chip${on ? " on" : ""}`}
      role="button"
      aria-pressed={on}
      tabIndex={0}
      onClick={onClick}
      onKeyDown={(e) => {
        if (e.key === "Enter" || e.key === " ") {
          e.preventDefault();
          onClick();
        }
      }}
    >
      {label}
    </span>
  );
}

// ---------------------------------------------------------------------------
// Field — labeled wrapper for form controls
// ---------------------------------------------------------------------------

interface FieldProps {
  label: string;
  children: ReactNode;
}

export function Field({ label, children }: FieldProps) {
  return (
    <div className="field">
      <label>{label}</label>
      {children}
    </div>
  );
}

// ---------------------------------------------------------------------------
// Input — styled <input> forwarding all standard props
// ---------------------------------------------------------------------------

type InputProps = InputHTMLAttributes<HTMLInputElement>;

export function Input({ className, ...rest }: InputProps) {
  return (
    <input
      className={`inp${className ? ` ${className}` : ""}`}
      {...rest}
    />
  );
}

// ---------------------------------------------------------------------------
// Button — styled <button> with variant + size modifiers
// ---------------------------------------------------------------------------

interface ButtonProps extends ButtonHTMLAttributes<HTMLButtonElement> {
  variant?: "warn" | "dgr" | "ghost";
  size?: "sm";
}

export function Button({ variant, size, className, ...rest }: ButtonProps) {
  const parts = ["btn", variant, size].filter(Boolean) as string[];
  if (className) parts.push(className);
  return <button className={parts.join(" ")} {...rest} />;
}
