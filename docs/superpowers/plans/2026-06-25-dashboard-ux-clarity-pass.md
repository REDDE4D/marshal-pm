# Dashboard UX-Clarity Pass Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make the Marshal dashboard self-explanatory — every action names what it commits, every disabled control explains itself, every outcome is visibly reported, empty lists guide the user — by upgrading shared primitives and rolling them across all pages.

**Architecture:** Primitive-first. Phase 1 builds/upgrades reusable components (`Field`, `Button`, `EmptyState`, `StatusMessage`, `ConfirmDialog`/`PromptDialog`) with TDD. Phases 2–3 adopt them page-by-page. Backward-compatible: new props are optional, so untouched call sites keep working.

**Tech Stack:** React 18 + TypeScript, Vite, Vitest. New dev-deps: `@testing-library/react`, `@testing-library/jest-dom`, `jsdom`. Backend (Go) untouched.

## Global Constraints

- Spec: `docs/superpowers/specs/2026-06-25-dashboard-ux-clarity-pass-design.md`.
- Branch: `ui-clarity-pass` (off `dev`). Already created; spec already committed there.
- No visual re-theme, no new features, no backend/API changes. Clarity only.
- Color semantics: `var(--teal)` = success, `var(--rose)` = error/danger, `var(--dim)` = info/help. Reuse existing CSS vars; do not invent new palette.
- All component upgrades MUST stay backward-compatible (new props optional).
- `StatusMessage` success auto-clears after **4000 ms**; errors persist until replaced/cleared.
- Only **irreversible** actions (delete/kill) use `ConfirmDialog`; recoverable actions (restart) keep inline confirm.
- Commit-message trailer: `Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>`.
- Per phase: `npx tsc -b` clean, `npx vitest run` green, and a Playwright smoke pass before the phase is considered done.
- Frontend build outputs to `../internal/dashboard/dist` (via `npm run build`); rebuild before any binary demo.

---

## PHASE 1 — Primitives

### Task 1: Test infrastructure for component tests

**Files:**
- Modify: `web/package.json` (devDependencies + nothing else)
- Modify: `web/vitest.config.ts`
- Create: `web/src/test/setup.ts`
- Create: `web/src/components/Button.test.tsx` (smoke, proves harness works)

**Interfaces:**
- Produces: a working `jsdom` + Testing Library harness so later tasks can render components and assert on DOM. Render helper is just `@testing-library/react`'s `render`/`screen`.

- [ ] **Step 1: Add dev dependencies**

Run:
```bash
cd web && npm install -D @testing-library/react@^16 @testing-library/jest-dom@^6 jsdom@^25
```
Expected: installs without peer-dep errors (React 18 present).

- [ ] **Step 2: Configure vitest for jsdom + tsx + setup file**

Replace `web/vitest.config.ts` with:
```ts
import { defineConfig } from "vitest/config";
import react from "@vitejs/plugin-react";

export default defineConfig({
  plugins: [react()],
  test: {
    environment: "jsdom",
    globals: true,
    setupFiles: ["./src/test/setup.ts"],
    include: ["src/**/*.test.{ts,tsx}"],
  },
});
```

- [ ] **Step 3: Create the setup file**

Create `web/src/test/setup.ts`:
```ts
import "@testing-library/jest-dom/vitest";
```

- [ ] **Step 4: Write a smoke test**

Create `web/src/components/Button.test.tsx`:
```tsx
import { describe, it, expect } from "vitest";
import { render, screen } from "@testing-library/react";
import { Button } from "./Controls";

describe("Button (harness smoke)", () => {
  it("renders its label", () => {
    render(<Button>click me</Button>);
    expect(screen.getByRole("button", { name: "click me" })).toBeInTheDocument();
  });
});
```

- [ ] **Step 5: Run the whole suite (old + new) to verify green under jsdom**

Run: `cd web && npx vitest run`
Expected: PASS — existing `lib/*.test.ts` (42 tests) still green under jsdom, plus the Button smoke test.

- [ ] **Step 6: Commit**
```bash
git add web/package.json web/package-lock.json web/vitest.config.ts web/src/test/setup.ts web/src/components/Button.test.tsx
git commit -m "test(web): jsdom + Testing Library harness for component tests"
```

---

### Task 2: `Button` — `disabledReason`

**Files:**
- Modify: `web/src/components/Controls.tsx:159-168` (ButtonProps + Button)
- Test: `web/src/components/Button.test.tsx` (extend)

**Interfaces:**
- Produces: `Button` accepts `disabledReason?: string`. When non-empty, the button is `disabled` and its `title` is set to the reason (caller need not also pass `disabled`/`title`). Explicit `disabled`/`title` props still honored when `disabledReason` is absent.

- [ ] **Step 1: Write failing tests**

Append to `web/src/components/Button.test.tsx`:
```tsx
it("disables and sets tooltip when disabledReason is set", () => {
  render(<Button disabledReason="Enter a name first">save</Button>);
  const b = screen.getByRole("button", { name: "save" });
  expect(b).toBeDisabled();
  expect(b).toHaveAttribute("title", "Enter a name first");
});

it("is enabled when disabledReason is undefined", () => {
  render(<Button>save</Button>);
  expect(screen.getByRole("button", { name: "save" })).toBeEnabled();
});
```

- [ ] **Step 2: Run to verify failure**

Run: `cd web && npx vitest run src/components/Button.test.tsx`
Expected: FAIL — disabled assertion fails (prop ignored).

- [ ] **Step 3: Implement**

Replace the Button block in `web/src/components/Controls.tsx`:
```tsx
interface ButtonProps extends ButtonHTMLAttributes<HTMLButtonElement> {
  variant?: "warn" | "dgr" | "ghost";
  size?: "sm";
  /** When set, the button renders disabled with this text as its tooltip. */
  disabledReason?: string;
}

export function Button({ variant, size, className, disabledReason, disabled, title, ...rest }: ButtonProps) {
  const parts = ["btn", variant, size].filter(Boolean) as string[];
  if (className) parts.push(className);
  return (
    <button
      className={parts.join(" ")}
      disabled={disabled || !!disabledReason}
      title={disabledReason || title}
      {...rest}
    />
  );
}
```

- [ ] **Step 4: Run to verify pass**

Run: `cd web && npx vitest run src/components/Button.test.tsx`
Expected: PASS (all Button tests).

- [ ] **Step 5: Commit**
```bash
git add web/src/components/Controls.tsx web/src/components/Button.test.tsx
git commit -m "feat(web): Button disabledReason — explain why a control is disabled"
```

---

### Task 3: `Field` — `required`, `hint`, `error`

**Files:**
- Modify: `web/src/components/Controls.tsx:126-138` (FieldProps + Field)
- Modify: `web/src/styles.css` (add `.field .hint`, `.field .err`, `.field .req` rules)
- Test: `web/src/components/Field.test.tsx`

**Interfaces:**
- Produces: `Field` accepts `required?: boolean`, `hint?: string`, `error?: string`. Renders a dim `·` required marker after the label when `required`; renders a `<p class="hint">` under the children, replaced by `<p class="err">` (rose) when `error` is set.

- [ ] **Step 1: Write failing tests**

Create `web/src/components/Field.test.tsx`:
```tsx
import { describe, it, expect } from "vitest";
import { render, screen } from "@testing-library/react";
import { Field, Input } from "./Controls";

describe("Field", () => {
  it("shows hint text when provided", () => {
    render(<Field label="name" hint="A label, e.g. tgbot"><Input /></Field>);
    expect(screen.getByText("A label, e.g. tgbot")).toBeInTheDocument();
  });
  it("shows error instead of hint when error is set", () => {
    render(<Field label="name" hint="help" error="Required"><Input /></Field>);
    expect(screen.getByText("Required")).toBeInTheDocument();
    expect(screen.queryByText("help")).not.toBeInTheDocument();
  });
  it("marks required fields", () => {
    render(<Field label="name" required><Input /></Field>);
    expect(screen.getByText("name").parentElement?.querySelector(".req")).not.toBeNull();
  });
});
```

- [ ] **Step 2: Run to verify failure**

Run: `cd web && npx vitest run src/components/Field.test.tsx`
Expected: FAIL — hint/error/req not rendered.

- [ ] **Step 3: Implement Field**

Replace the Field block in `web/src/components/Controls.tsx`:
```tsx
interface FieldProps {
  label: string;
  children: ReactNode;
  required?: boolean;
  hint?: string;
  error?: string;
}

export function Field({ label, children, required, hint, error }: FieldProps) {
  return (
    <div className="field">
      <label>
        {label}
        {required && <span className="req" title="Required"> ·</span>}
      </label>
      {children}
      {error ? <p className="err">{error}</p> : hint ? <p className="hint">{hint}</p> : null}
    </div>
  );
}
```

- [ ] **Step 4: Add styles**

Append to `web/src/styles.css`:
```css
.field .req { color: var(--dim); }
.field .hint { margin: 4px 0 0; font-size: 11px; color: var(--dim); }
.field .err { margin: 4px 0 0; font-size: 11px; color: var(--rose); }
```

- [ ] **Step 5: Run to verify pass**

Run: `cd web && npx vitest run src/components/Field.test.tsx`
Expected: PASS.

- [ ] **Step 6: Commit**
```bash
git add web/src/components/Controls.tsx web/src/components/Field.test.tsx web/src/styles.css
git commit -m "feat(web): Field required/hint/error affordances"
```

---

### Task 4: `EmptyState` component

**Files:**
- Create: `web/src/components/EmptyState.tsx`
- Modify: `web/src/styles.css` (add `.empty-state`)
- Test: `web/src/components/EmptyState.test.tsx`

**Interfaces:**
- Produces: `EmptyState({ message, action }: { message: string; action?: ReactNode })` — renders a muted block with the message and optional action node beneath.

- [ ] **Step 1: Write failing test**

Create `web/src/components/EmptyState.test.tsx`:
```tsx
import { describe, it, expect } from "vitest";
import { render, screen } from "@testing-library/react";
import { EmptyState } from "./EmptyState";

describe("EmptyState", () => {
  it("renders the message", () => {
    render(<EmptyState message="No channels yet." />);
    expect(screen.getByText("No channels yet.")).toBeInTheDocument();
  });
  it("renders an optional action node", () => {
    render(<EmptyState message="No agents." action={<span>do thing</span>} />);
    expect(screen.getByText("do thing")).toBeInTheDocument();
  });
});
```

- [ ] **Step 2: Run to verify failure**

Run: `cd web && npx vitest run src/components/EmptyState.test.tsx`
Expected: FAIL — module not found.

- [ ] **Step 3: Implement**

Create `web/src/components/EmptyState.tsx`:
```tsx
import type { ReactNode } from "react";

export function EmptyState({ message, action }: { message: string; action?: ReactNode }) {
  return (
    <div className="empty-state">
      <p>{message}</p>
      {action && <div className="empty-state-action">{action}</div>}
    </div>
  );
}
```

- [ ] **Step 4: Add styles**

Append to `web/src/styles.css`:
```css
.empty-state { padding: 22px; color: var(--dim); font-size: 13px; }
.empty-state-action { margin-top: 8px; }
```

- [ ] **Step 5: Run to verify pass**

Run: `cd web && npx vitest run src/components/EmptyState.test.tsx`
Expected: PASS.

- [ ] **Step 6: Commit**
```bash
git add web/src/components/EmptyState.tsx web/src/components/EmptyState.test.tsx web/src/styles.css
git commit -m "feat(web): EmptyState component for empty lists"
```

---

### Task 5: `StatusMessage` + `useStatus`

**Files:**
- Create: `web/src/components/StatusMessage.tsx`
- Modify: `web/src/styles.css` (add `.status-msg` + kind colors)
- Test: `web/src/components/StatusMessage.test.tsx`

**Interfaces:**
- Produces:
  - `type Status = { kind: "success" | "error" | "info"; text: string } | null`
  - `useStatus(): { status: Status; show: (kind, text) => void; clear: () => void }` — `show("success", …)` auto-clears after 4000 ms; `show("error" | "info", …)` persists.
  - `StatusMessage({ status }: { status: Status })` — renders nothing when null; otherwise a `<span class="status-msg <kind>">`.

- [ ] **Step 1: Write failing tests (use fake timers for auto-clear)**

Create `web/src/components/StatusMessage.test.tsx`:
```tsx
import { describe, it, expect, vi, afterEach } from "vitest";
import { render, screen, act, renderHook } from "@testing-library/react";
import { StatusMessage, useStatus } from "./StatusMessage";

afterEach(() => vi.useRealTimers());

describe("StatusMessage", () => {
  it("renders nothing when status is null", () => {
    const { container } = render(<StatusMessage status={null} />);
    expect(container.firstChild).toBeNull();
  });
  it("renders text with the kind class", () => {
    render(<StatusMessage status={{ kind: "error", text: "boom" }} />);
    const el = screen.getByText("boom");
    expect(el).toHaveClass("status-msg", "error");
  });
});

describe("useStatus", () => {
  it("auto-clears success after 4s but keeps errors", () => {
    vi.useFakeTimers();
    const { result } = renderHook(() => useStatus());
    act(() => result.current.show("success", "saved"));
    expect(result.current.status?.text).toBe("saved");
    act(() => vi.advanceTimersByTime(4000));
    expect(result.current.status).toBeNull();

    act(() => result.current.show("error", "nope"));
    act(() => vi.advanceTimersByTime(4000));
    expect(result.current.status?.text).toBe("nope");
  });
});
```

- [ ] **Step 2: Run to verify failure**

Run: `cd web && npx vitest run src/components/StatusMessage.test.tsx`
Expected: FAIL — module not found.

- [ ] **Step 3: Implement**

Create `web/src/components/StatusMessage.tsx`:
```tsx
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
```

- [ ] **Step 4: Add styles**

Append to `web/src/styles.css`:
```css
.status-msg { margin-left: 12px; font-size: 12px; }
.status-msg.success { color: var(--teal); }
.status-msg.error { color: var(--rose); }
.status-msg.info { color: var(--dim); }
```

- [ ] **Step 5: Run to verify pass**

Run: `cd web && npx vitest run src/components/StatusMessage.test.tsx`
Expected: PASS.

- [ ] **Step 6: Commit**
```bash
git add web/src/components/StatusMessage.tsx web/src/components/StatusMessage.test.tsx web/src/styles.css
git commit -m "feat(web): StatusMessage + useStatus for consistent inline feedback"
```

---

### Task 6: `ConfirmDialog` + `PromptDialog`

**Files:**
- Create: `web/src/components/ConfirmDialog.tsx`
- Test: `web/src/components/ConfirmDialog.test.tsx`
- Reference (read, do not modify): `web/src/components/Modal.tsx`

**Interfaces:**
- Produces:
  - `ConfirmDialog({ title, body, confirmLabel, danger, onConfirm, onCancel })` — renders a `Modal` with a body and Cancel / confirm buttons; `danger` styles confirm as `variant="dgr"`.
  - `PromptDialog({ title, label, initial, confirmLabel, onConfirm, onCancel })` — same, with a text `Input`; `onConfirm(value: string)`.
- Consumes: `Modal` from `./Modal` (props: verify by reading `Modal.tsx` — it exposes `onClose` + children; match its actual signature).

- [ ] **Step 1: Read Modal.tsx to get its real props**

Run: `sed -n '1,60p' web/src/components/Modal.tsx` — note the exact prop names (e.g. `open`, `onClose`, `title`) and use them verbatim below. If Modal has no `title` prop, render the title inside children.

- [ ] **Step 2: Write failing tests**

Create `web/src/components/ConfirmDialog.test.tsx`:
```tsx
import { describe, it, expect, vi } from "vitest";
import { render, screen, fireEvent } from "@testing-library/react";
import { ConfirmDialog, PromptDialog } from "./ConfirmDialog";

describe("ConfirmDialog", () => {
  it("calls onConfirm when the confirm button is clicked", () => {
    const onConfirm = vi.fn();
    render(<ConfirmDialog title="Delete?" body="Sure?" confirmLabel="Delete" danger onConfirm={onConfirm} onCancel={() => {}} />);
    fireEvent.click(screen.getByRole("button", { name: "Delete" }));
    expect(onConfirm).toHaveBeenCalledOnce();
  });
  it("calls onCancel when Cancel is clicked", () => {
    const onCancel = vi.fn();
    render(<ConfirmDialog title="Delete?" body="Sure?" confirmLabel="Delete" onConfirm={() => {}} onCancel={onCancel} />);
    fireEvent.click(screen.getByRole("button", { name: "Cancel" }));
    expect(onCancel).toHaveBeenCalledOnce();
  });
});

describe("PromptDialog", () => {
  it("passes the typed value to onConfirm", () => {
    const onConfirm = vi.fn();
    render(<PromptDialog title="Rename" label="New name" initial="a" confirmLabel="Rename" onConfirm={onConfirm} onCancel={() => {}} />);
    fireEvent.change(screen.getByDisplayValue("a"), { target: { value: "b" } });
    fireEvent.click(screen.getByRole("button", { name: "Rename" }));
    expect(onConfirm).toHaveBeenCalledWith("b");
  });
});
```

- [ ] **Step 3: Run to verify failure**

Run: `cd web && npx vitest run src/components/ConfirmDialog.test.tsx`
Expected: FAIL — module not found.

- [ ] **Step 4: Implement (adapt Modal props to what Step 1 found)**

Create `web/src/components/ConfirmDialog.tsx` (this template assumes `Modal` takes `onClose` + children; adjust to the real signature):
```tsx
import { useState } from "react";
import { Modal } from "./Modal";
import { Button } from "./Controls";

export function ConfirmDialog({
  title, body, confirmLabel, danger, onConfirm, onCancel,
}: {
  title: string; body: string; confirmLabel: string; danger?: boolean;
  onConfirm: () => void; onCancel: () => void;
}) {
  return (
    <Modal onClose={onCancel}>
      <h3 className="dialog-title">{title}</h3>
      <p className="dialog-body">{body}</p>
      <div className="actions">
        <Button variant="ghost" onClick={onCancel}>Cancel</Button>
        <Button variant={danger ? "dgr" : undefined} onClick={onConfirm}>{confirmLabel}</Button>
      </div>
    </Modal>
  );
}

export function PromptDialog({
  title, label, initial, confirmLabel, onConfirm, onCancel,
}: {
  title: string; label: string; initial?: string; confirmLabel: string;
  onConfirm: (value: string) => void; onCancel: () => void;
}) {
  const [value, setValue] = useState(initial ?? "");
  return (
    <Modal onClose={onCancel}>
      <h3 className="dialog-title">{title}</h3>
      <label className="dialog-label">{label}</label>
      <input className="inp" value={value} autoFocus onChange={(e) => setValue(e.target.value)} />
      <div className="actions">
        <Button variant="ghost" onClick={onCancel}>Cancel</Button>
        <Button onClick={() => onConfirm(value)} disabledReason={value.trim() ? undefined : "Enter a value"}>{confirmLabel}</Button>
      </div>
    </Modal>
  );
}
```

- [ ] **Step 5: Run to verify pass**

Run: `cd web && npx vitest run src/components/ConfirmDialog.test.tsx`
Expected: PASS. (If Modal requires an `open` prop, pass `open` and render conditionally in tests.)

- [ ] **Step 6: Add minimal dialog styles**

Append to `web/src/styles.css`:
```css
.dialog-title { margin: 0 0 8px; font-size: 15px; }
.dialog-body { margin: 0 0 16px; color: var(--dim); font-size: 13px; }
.dialog-label { display: block; margin-bottom: 6px; font-size: 11px; color: var(--dim); text-transform: uppercase; letter-spacing: .08em; }
```

- [ ] **Step 7: Commit**
```bash
git add web/src/components/ConfirmDialog.tsx web/src/components/ConfirmDialog.test.tsx web/src/styles.css
git commit -m "feat(web): ConfirmDialog/PromptDialog on the accessible Modal"
```

---

### Task 7: Phase 1 verification gate

- [ ] **Step 1: Typecheck + full test run**

Run: `cd web && npx tsc -b && npx vitest run`
Expected: tsc clean; all tests pass (lib + new component tests).

- [ ] **Step 2: Build the bundle**

Run: `cd web && npm run build`
Expected: builds into `../internal/dashboard/dist`.

- [ ] **Step 3: Commit any build output (if the repo tracks dist)**
```bash
git add internal/dashboard/dist
git commit -m "build(web): rebuild dashboard for Phase 1 primitives" || echo "nothing to commit"
```

---

## PHASE 2 — High-confusion pages

Each task: read the file, apply the listed primitive adoptions, run `tsc -b`, commit. End the phase with a Playwright smoke (Task 11).

### Task 8: Notifications page

**Files:**
- Modify: `web/src/Notifications.tsx`

**Interfaces:**
- Consumes: `Button` (`disabledReason`), `Field` (`required`/`hint`), `EmptyState`, `useStatus`/`StatusMessage` from Phase 1.

- [ ] **Step 1: Channels — button, field hint, empty state, status.**
  - Change the add button label `+ add channel` → `Add channel`; replace `disabled={!name.trim()}` with `disabledReason={name.trim() ? undefined : "Enter a channel name first"}`.
  - On the `name` Field, pass `required` and `hint="A label for this channel, e.g. tgbot — separate from the bot token"`.
  - When `cfg.channels.length === 0`, render `<EmptyState message="No channels yet — fill the form below and click Add channel." />` in place of the (currently absent) table.
  - Replace the channel-section `msg` span with `useStatus()` + `<StatusMessage status={status} />`; call `show("success","saved")` / `show("error", res.error)` instead of setting raw strings.
- [ ] **Step 2: Rules — mirror the same treatment.** `+ add rule` → `Add rule`; `disabledReason`; `EmptyState message="No rules yet — events won't notify until a rule routes them to a channel."`; `useStatus`.
- [ ] **Step 3: Settings — keep "save settings" but ensure it is visually inside its section (it already is); convert its feedback to `useStatus`/`StatusMessage`; keep the v0.9.0 error-surfacing behavior.**
- [ ] **Step 4: Typecheck.** Run: `cd web && npx tsc -b` → clean.
- [ ] **Step 5: Commit.**
```bash
git add web/src/Notifications.tsx
git commit -m "feat(web): clarity pass on Notifications (labels, hints, empty states, status)"
```

### Task 9: Credentials page

**Files:**
- Modify: `web/src/Credentials.tsx`

- [ ] **Step 1:** Replace `disabled={!canSubmit}` on the submit button with a computed `disabledReason` that names the first missing field, type-aware (e.g. `!name ? "Enter a name" : !username ? "Enter a username" : (!token ? "Enter a token" : undefined)`). Keep the existing `busy` → "saving…" label.
- [ ] **Step 2:** Add a sub-label/hint under the type selector clarifying that the action button generates a key (ssh-key) vs stores a token (https-token).
- [ ] **Step 3:** Add `useStatus()` and show `"credential added"` on success (today the form silently clears).
- [ ] **Step 4:** Run `cd web && npx tsc -b` → clean.
- [ ] **Step 5:** Commit `feat(web): clarity pass on Credentials (disabledReason, success feedback)`.

### Task 10: Add-app modal

**Files:**
- Modify: `web/src/AddAppModal.tsx`

- [ ] **Step 1:** Replace `disabled={!canSubmit}` on "add app" with a `disabledReason` naming the missing field (agent/name/cmd, and repo when git).
- [ ] **Step 2:** Move the `{error && <div className="modal-error">…}` to render at the **top** of the form body (above the fields) so it's visible without scrolling.
- [ ] **Step 3:** Add `required` markers + short `hint`s to the conditional git fields (repo, ref, build command) clarifying which apply to the selected source type.
- [ ] **Step 4:** Run `cd web && npx tsc -b` → clean.
- [ ] **Step 5:** Commit `feat(web): clarity pass on Add-app modal (top error, disabledReason, field hints)`.

### Task 11: Phase 2 Playwright smoke

**Files:**
- Create: `/tmp/pw-phase2.js` (not committed)

- [ ] **Step 1:** Build + run a scratch demo server (notifications enabled) on :9000/:9001 per `CLAUDE.md` (passwd while down, then `marshal server --http-listen :9001`).
- [ ] **Step 2:** Script: login → Notifications → confirm the add-channel button is labeled "Add channel" and shows a tooltip when name empty; add a webhook channel; confirm EmptyState disappears and StatusMessage shows success; → Credentials add path shows success message.
- [ ] **Step 3:** Run the script; capture pass/fail. Tear down the demo; confirm no orphan `marshal` procs from the demo data dir.

---

## PHASE 3 — Remaining pages

### Task 12: Overview / Fleet
**Files:** Modify `web/src/Overview.tsx`, `web/src/RestartAllButton.tsx`
- [ ] Add `EmptyState` for "no agents" with action text pointing at **+ connect agent**.
- [ ] Convert `className="lnk"` `+ add app` / `+ connect agent` to the `Button` component (keep placement).
- [ ] Make `RestartAllButton` use the same inline-confirm shape as `ControlButtons` (see Task 15) — no separate ad-hoc pattern.
- [ ] `npx tsc -b` clean; commit `feat(web): clarity pass on Overview (empty state, real buttons)`.

### Task 13: FileBrowser
**Files:** Modify `web/src/FileBrowser.tsx`
- [ ] Replace `window.confirm` (delete) with `ConfirmDialog` (danger) and `window.prompt` (rename) with `PromptDialog`, wiring their `onConfirm`/`onCancel` to the existing handlers; render the dialog from local state.
- [ ] Replace Save `disabled={busy || draft === open.content}` with `disabledReason={busy ? "Saving…" : draft === open.content ? "No changes to save" : undefined}`.
- [ ] Route success/“✓ Pushed…” text through `useStatus` so it auto-clears.
- [ ] `npx tsc -b` clean; commit `feat(web): clarity pass on FileBrowser (real dialogs, disabledReason)`.

### Task 14: Errors, Logs, ProcessDetail
**Files:** Modify `web/src/Errors.tsx`, `web/src/Logs.tsx`, `web/src/ProcessDetail.tsx`
- [ ] Errors: add `title="Acknowledge — stops this error from nagging"` (and "Un-acknowledge" when acked) to the ack button.
- [ ] Logs: replace `disabled={procs.length === 0}` on the selector with a `Button`/`select` `disabledReason`/`title="No processes for this agent"`.
- [ ] ProcessDetail: add `disabledReason` to disconnected control buttons ("Agent offline"); add `EmptyState` for empty recent logs; add a small "last 8 lines · live ›" caption to the recent-logs box.
- [ ] `npx tsc -b` clean; commit `feat(web): clarity pass on Errors/Logs/ProcessDetail`.

### Task 15: ControlButtons + Login + conventions note
**Files:** Modify `web/src/ControlButtons.tsx`, `web/src/Login.tsx`, Create `web/src/components/README.md`
- [ ] ControlButtons: remove the silent 3s auto-dismiss `setTimeout`; the pending confirm stays until the user clicks confirm or clicks away; relabel the pending button to "click to confirm <op>".
- [ ] Login: after a failed attempt, re-enable submit and refocus the password field; keep the generic error text.
- [ ] Create `web/src/components/README.md` documenting the conventions: commit-button labeling, `disabledReason`, `EmptyState`, `StatusMessage` color/timing, ConfirmDialog-for-irreversible-only.
- [ ] `npx tsc -b` clean; commit `feat(web): clarity pass on ControlButtons/Login + conventions doc`.

### Task 16: Phase 3 verification + release
- [ ] **Step 1:** `cd web && npx tsc -b && npx vitest run` → all green. `cd .. && go test ./... -race -count=1` → green (no backend change, but verify).
- [ ] **Step 2:** `cd web && npm run build`; commit dist.
- [ ] **Step 3:** Playwright smoke for FileBrowser delete-with-confirm + ControlButtons no-auto-dismiss against a scratch demo; tear down; confirm no orphans.
- [ ] **Step 4:** Update `CHANGELOG.md` `[Unreleased]` (Changed: clarity across dashboard; Added: EmptyState/StatusMessage/ConfirmDialog primitives). Cut **v0.10.0**: move to a dated section, update compare links, merge `ui-clarity-pass` → `dev` (`--no-ff`), then `dev` → `main` (`--no-ff`), tag `v0.10.0`, push, GitHub release. (Phase 3 may instead ship as v0.10.1 if 1+2 released first.)

---

## Notes for the implementer
- Read each page file before editing; match its existing import style and the `actions`/`SectionHeader` patterns already present.
- Do not change backend Go code, API shapes, or the notify persistence — they are verified correct.
- When a step says "adjust to the real Modal signature," that means read `Modal.tsx` and use its actual props; the template is illustrative.
- Keep each page's existing behavior; only clarity affordances change.
