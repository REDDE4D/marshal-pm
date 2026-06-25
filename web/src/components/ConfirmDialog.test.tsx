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
