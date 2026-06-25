import { describe, it, expect } from "vitest";
import { render, screen } from "@testing-library/react";
import { Button } from "./Controls";

describe("Button (harness smoke)", () => {
  it("renders its label", () => {
    render(<Button>click me</Button>);
    expect(screen.getByRole("button", { name: "click me" })).toBeInTheDocument();
  });
});
