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
