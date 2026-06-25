# Dashboard component conventions

This file captures the clarity conventions established during the UI consistency pass
(2026-06) so future pages and features stay coherent.

## Commit / action buttons

- Name what they do: "save log settings" not "save"; "delete process" not "delete".
- Scope them to their section: place the button inside the form or panel it controls.
- Avoid two identically-labelled save buttons on the same page without clear section
  headers distinguishing them.

## Disabled-state explanations

- Use `Button`'s `disabledReason` prop (not bare `disabled`) so a greyed button shows a
  tooltip explaining why it is unavailable.
- For native elements (`<select>`, `<input>`, etc.) use the `title` attribute to the same
  end.

## Form fields

- Use the `Field` wrapper for every form input; pass `required`, `hint`, and `error` as
  appropriate so the label, helper text, and validation message stay visually consistent.

## Empty states

- Use `EmptyState` (from `./EmptyState`) for any list or table that can be empty.
  Do not leave a blank section with no explanation.

## Feedback / status messages

Use `useStatus` / `StatusMessage` (from `./StatusMessage`) for in-place feedback —
no toasts, no alerts.

| Color  | Meaning          | Persistence          |
|--------|------------------|----------------------|
| teal   | success          | auto-clears ~4 s     |
| rose   | error            | persists until next action |
| dim    | info / in-flight | caller-controlled    |

Place the `StatusMessage` inline, near the action that produced it.

## Destructive-action confirmation

- **Irreversible actions** (delete, kill, force-stop): use `ConfirmDialog` (which wraps
  the shared `Modal`) so the user gets an explicit modal confirmation step.
- **Recoverable actions** (restart, reload, stop): use lightweight inline
  "click to confirm" — replace the button row with a "confirm \<op\>" button and a ✕
  cancel, no modal needed.
- **No silent auto-dismiss**: never use a `setTimeout` to cancel a pending confirmation.
  The user must resolve it explicitly (confirm or cancel). Auto-clearing transient
  *result* messages (~4 s) is fine; auto-cancelling a *pending* confirmation is not.
