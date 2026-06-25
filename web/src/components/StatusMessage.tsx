import { useCallback, useRef, useState } from "react";

export type Status = { kind: "success" | "error" | "info"; text: string } | null;

export function useStatus() {
  const [status, setStatus] = useState<Status>(null);
  const timer = useRef<number | undefined>(undefined);
  const clear = useCallback(() => {
    if (timer.current) window.clearTimeout(timer.current);
    timer.current = undefined;
    setStatus(null);
  }, []);
  const show = useCallback((kind: NonNullable<Status>["kind"], text: string) => {
    if (timer.current) window.clearTimeout(timer.current);
    timer.current = undefined;
    setStatus({ kind, text });
    if (kind === "success") {
      timer.current = window.setTimeout(() => setStatus(null), 4000);
    }
  }, []);
  return { status, show, clear };
}

export function StatusMessage({ status }: { status: Status }) {
  if (!status) return null;
  return <span className={`status-msg ${status.kind}`}>{status.text}</span>;
}
