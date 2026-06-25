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
