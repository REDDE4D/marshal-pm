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
