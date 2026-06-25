import { describe, it, expect } from "vitest";
import { render, screen } from "@testing-library/react";
import { Button } from "./Controls";

describe("Button (harness smoke)", () => {
  it("renders its label", () => {
    render(<Button>click me</Button>);
    expect(screen.getByRole("button", { name: "click me" })).toBeInTheDocument();
  });
});

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
