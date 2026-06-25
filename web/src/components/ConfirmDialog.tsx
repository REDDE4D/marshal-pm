import { useState } from "react";
import { Modal } from "./Modal";
import { Button } from "./Controls";

export function ConfirmDialog({
  title,
  body,
  confirmLabel,
  danger,
  onConfirm,
  onCancel,
}: {
  title: string;
  body: string;
  confirmLabel: string;
  danger?: boolean;
  onConfirm: () => void;
  onCancel: () => void;
}) {
  return (
    <Modal
      title={title}
      onClose={onCancel}
      footer={
        <>
          <Button variant="ghost" onClick={onCancel}>Cancel</Button>
          <Button variant={danger ? "dgr" : undefined} onClick={onConfirm}>{confirmLabel}</Button>
        </>
      }
    >
      <p className="dialog-body">{body}</p>
    </Modal>
  );
}

export function PromptDialog({
  title,
  label,
  initial,
  confirmLabel,
  onConfirm,
  onCancel,
}: {
  title: string;
  label: string;
  initial?: string;
  confirmLabel: string;
  onConfirm: (value: string) => void;
  onCancel: () => void;
}) {
  const [value, setValue] = useState(initial ?? "");
  return (
    <Modal
      title={title}
      onClose={onCancel}
      footer={
        <>
          <Button variant="ghost" onClick={onCancel}>Cancel</Button>
          <Button
            onClick={() => onConfirm(value)}
            disabledReason={value.trim() ? undefined : "Enter a value"}
          >
            {confirmLabel}
          </Button>
        </>
      }
    >
      <label className="dialog-label">{label}</label>
      <input
        className="inp"
        value={value}
        autoFocus
        onChange={(e) => setValue(e.target.value)}
      />
    </Modal>
  );
}
